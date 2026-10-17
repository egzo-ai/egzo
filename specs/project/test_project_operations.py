# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""The operator's commands on a project: `up --dry-run`, `rotate-ca`, `proxy log` and `proxy rules`, the operator API under /v1, and the
project lock (a lease held in the engine) with `project unlock`.

The first specs share one project that is up for the whole module (`sandbox`, with two egress profiles and a secret to rotate); the
last, which changes what exists, has its own. Staleness is in test_agent_stale.py and `up` itself in test_project_up_down.py."""

import json
import os
import subprocess

import pytest

from conftest import LABEL_PREFIX
from support import custom, spec

SECRETS = {"DEPLOY_TOKEN": "first-value", "API_SECRET": "very-secret-value-1234567890"}


@pytest.fixture(scope="module")
def sandbox(shared_project, engine):
    image = engine.agent_image()
    shared_project.env.update(SECRETS)
    shared_project.up(spec(
        vaults={"main": {"backend": "env", "secrets": list(SECRETS)}},
        egress={
            "default": {"allow": ["example.com", "docs.example.org"], "services": {"echo": {"hosts": ["example.net"], "inject": {"header": "X-Key"}, "secret": "main/DEPLOY_TOKEN"}}},
            "wide": {"extend": "default", "services": {"svc": {"hosts": ["api.test"], "inject": {"header": "x-key"}, "secret": "main/API_SECRET"}}},
        },
        agents={"coder": custom(image), "reviewer": custom(image, egress="wide")},
    ))
    return shared_project


@pytest.fixture(autouse=True)
def start_from_no_instance(sandbox):
    sandbox.retire()
    yield


def sidecar(engine, project, service):
    return next(c for c in engine.containers(project.name) if c.labels.get(f"{LABEL_PREFIX}service") == service)


def test_up_dry_run_says_nothing_to_do_when_converged_sees_a_rotated_secret_and_a_restarted_proxy(sandbox, engine):
    p = sandbox
    p.spawn("coder")
    converged = p.sh("project up --dry-run")
    assert converged.returncode == 0 and converged.stdout.strip().splitlines()[-1] == "nothing to do"

    rotated = p.sh("project up --dry-run", env={"DEPLOY_TOKEN": "second-value"})
    assert rotated.returncode == 1 and "policy" in rotated.stdout, rotated.stdout
    assert "second-value" not in rotated.stdout + rotated.stderr, "a dry run never prints a secret"

    p.ok("restart proxy")
    assert p.sh("project up --dry-run").returncode == 0, "a proxy restarted through egzo keeps the dry run clean"


def test_rotate_ca_issues_a_new_ca_restarts_the_agents_and_the_proxy_still_serves_the_policy(sandbox, engine):
    p = sandbox
    name = p.spawn("coder")

    def ca():
        return engine.exec(engine.instance(p.name, name).name, "cat", "/etc/egzo/ca/ca.crt").stdout

    old, started = ca(), engine.instance(p.name, name).raw["State"]["StartedAt"]
    p.ok("project rotate-ca", timeout=300)
    new = ca()
    assert new.startswith("-----BEGIN CERTIFICATE-----") and new != old
    assert engine.instance(p.name, name).raw["State"]["StartedAt"] != started, "the agents that trust the CA are restarted"
    assert "nothing to do" in p.ok("project up").stdout
    reached = engine.exec(engine.instance(p.name, name).name, "sh", "-c", 'curl -sS -m 20 -o /dev/null -w "%{http_code}" https://example.com/')
    assert reached.stdout.strip() == "200", reached.stderr


def test_proxy_log_can_be_filtered_by_agent_and_proxy_rules_show_what_each_agent_may_reach_without_any_secret(sandbox, engine):
    p = sandbox
    coder, reviewer = p.spawn("coder"), p.spawn("reviewer")
    for name in (coder, reviewer):
        p.sh(f"exec {name} -- curl -sS -m 10 -o /dev/null", f"https://{name}.example.org/")
    both = [json.loads(line) for line in p.ok("proxy log").stdout.splitlines() if line.startswith("{")]
    assert {e["agent"] for e in both} >= {coder, reviewer}
    only = [json.loads(line) for line in p.ok("proxy log --agent", coder).stdout.splitlines() if line.startswith("{")]
    assert only and {e["agent"] for e in only} == {coder}

    rules = p.ok("proxy rules")
    assert "docs.example.org" in rules.stdout and "api.test" in rules.stdout
    assert "very-secret-value" not in rules.stdout + rules.stderr
    plain = p.ok("proxy rules --agent", coder).stdout
    assert "docs.example.org" in plain and "api.test" not in plain
    assert "api.test" in p.ok("proxy rules --agent", reviewer).stdout


def test_the_operator_api_lives_under_v1_and_the_unversioned_paths_are_gone(sandbox, engine):
    p = sandbox
    p.spawn("coder")

    def request(path):
        return subprocess.run(
            engine.command("exec", sidecar(engine, p, "control").name, "/egzo", "control", "request", "GET", path),
            text=True, capture_output=True, env={**os.environ, **engine.env},
        )

    versioned = request("/v1/agents")
    assert versioned.returncode == 0, versioned.stderr
    assert "coder-1" in versioned.stdout
    assert request("/agents").returncode != 0
    assert request("/healthz").returncode == 0, "the healthcheck is not versioned"


def test_up_publishes_templates_and_starts_no_agent_and_the_lock_is_a_lease_in_the_engine(live_project, engine, agent_image):
    p = live_project
    document = spec(agents={"coder": custom(agent_image), "second": custom(agent_image)})
    p.write(document)
    dry = p.sh("project up --dry-run")
    assert dry.returncode == 1 and "control" in dry.stdout and "create" in dry.stdout

    p.ok("project up", timeout=300)
    assert {c.labels[f"{LABEL_PREFIX}kind"] for c in engine.containers(p.name)} == {"control", "proxy"} and engine.instances(p.name) == []
    (p.root / "egzo.yaml").write_text("# emptied after up\n")
    assert p.spawn("second") == "second-1", "a published template can be spawned without the file's help"
    assert not (p.root / ".egzo").exists(), "the lock does not need the project directory"

    def lease():
        found = engine.run("ps", "-a", "--filter", f"name={p.name}.lock", "--format", "{{.Names}}").stdout.split()
        return found[0] if found else None

    assert lease() is None, "a finished command leaves no lease"
    assert p.sh("agent rm no-such-instance").returncode != 0 and lease() is None, "a failed command releases it too"

    def hold(holder, expires):
        labels = ["--label", f"{LABEL_PREFIX}project={p.name}", "--label", f"{LABEL_PREFIX}kind=lock",
                  "--label", f"{LABEL_PREFIX}lock-holder={holder}", "--label", f"{LABEL_PREFIX}lock-expires={expires}"]
        assert engine.run("create", "--name", f"{p.name}.lock", *labels, agent_image).returncode == 0

    hold("another-command", 9999999999)  # what another command that is changing the project does
    for command in ("project up", "project down --force"):
        refused = p.sh(command, timeout=300)
        assert refused.returncode != 0 and "another egzo command" in refused.stderr and "another-command" in refused.stderr, (command, refused.stderr)
    unlocked = p.ok("project unlock")
    assert "another-command" in unlocked.stdout and lease() is None, "unlock breaks the lease and says whose it was"
    p.write(document)
    p.ok("project up", timeout=300)

    hold("long-gone", 1)  # a lease whose time ran out is taken over
    assert p.spawn("coder", "taken") == "taken" and lease() is None
