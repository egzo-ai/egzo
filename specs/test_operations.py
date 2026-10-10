# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""The operator commands: doctor, diff, ca rotate, proxy rules. (`egzo secrets` is in test_secrets.py.)"""

import json
import re
import subprocess

import pytest

from conftest import LABEL_PREFIX
from support import agent, spec

pytestmark = pytest.mark.usefixtures("engine")


def custom(image, **fields):
    return agent(harness="custom", image=image, **fields)


def up(project, document, *args):
    """`up`, then one instance of every template in the document (`coder-1`, `reviewer-1`, ...)."""
    project.write(document)
    result = project.run("up", *args, timeout=300)
    assert result.returncode == 0, result.stderr
    for template in document.get("agents", {}):
        project.spawn(template)
    return result


def container(engine, project, name):
    """The container of an instance (`coder-1`) or of a sidecar (`control`, `proxy`), or None."""
    found = [
        r for r in engine.containers(project.name)
        if r.labels.get(f"{LABEL_PREFIX}instance") == name
        or (r.labels.get(f"{LABEL_PREFIX}kind") != "agent" and r.labels.get(f"{LABEL_PREFIX}service") == name)
    ]
    return found[0] if found else None


# --- doctor -----------------------------------------------------------------------------------------------


def test_doctor_checks_the_engine_and_exits_zero_when_nothing_fails(project, engine):
    result = project.run("doctor", env=engine.env)
    assert result.returncode == 0, result.stdout + result.stderr
    lines = [l for l in result.stdout.splitlines() if l.strip()]
    assert all(re.match(r"(ok|warn|fail)\s", l) for l in lines), result.stdout
    assert any(l.startswith("ok") and "engine" in l for l in lines)


def test_doctor_fails_loudly_when_no_engine_answers(project):
    result = project.run("doctor", env={"DOCKER_HOST": "unix:///nonexistent.sock"})
    assert result.returncode != 0
    assert "fail" in result.stdout and "engine" in result.stdout


def test_doctor_reports_the_isolation_runtimes(project, engine):
    out = project.run("doctor", env=engine.env).stdout
    assert re.search(r"(ok|warn)\s+.*gVisor|runsc", out), out
    assert re.search(r"(ok|warn)\s+.*(rootless|rootful)", out), out


def test_doctor_warns_when_the_egzo_directory_is_not_git_ignored(project, engine):
    subprocess.run(["git", "init", "-q"], cwd=project.root, check=True)
    project.write(spec())
    result = project.run("doctor", env=engine.env)
    assert re.search(r"warn\s+.*\.egzo", result.stdout), result.stdout
    (project.root / ".gitignore").write_text(".egzo/\n")
    assert not re.search(r"warn\s+.*\.egzo", project.run("doctor", env=engine.env).stdout)


# --- diff -------------------------------------------------------------------------------------------------


def test_diff_lists_what_up_would_do_and_exits_one_when_there_is_a_difference(live_project, engine, agent_image):
    live_project.write(spec(agents={"coder": custom(agent_image)}))
    result = live_project.run("diff")
    assert result.returncode == 1
    assert "control" in result.stdout and "create" in result.stdout


def test_diff_is_silent_and_exits_zero_when_converged(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image)}))
    result = live_project.run("diff")
    assert result.returncode == 0, result.stdout
    assert result.stdout.strip() == ""


def test_diff_names_the_instances_whose_template_changed(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image), "reviewer": custom(agent_image)}))
    live_project.write(spec(agents={"coder": custom(agent_image, env={"CHANGED": "yes"}), "reviewer": custom(agent_image)}))
    result = live_project.run("diff")
    assert result.returncode == 1
    assert "stale: coder-1" in result.stdout and "reviewer-1" not in result.stdout
    assert live_project.run("up", "--dry-run").returncode == 0  # same algorithm, no exit code semantics


# --- ca rotate --------------------------------------------------------------------------------------------


def read_ca(engine, project, service="coder-1"):
    return engine.exec(container(engine, project, service).name, "cat", "/etc/egzo/ca/ca.crt").stdout


def test_ca_rotate_issues_a_new_ca_and_restarts_the_agents_that_trust_it(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image)}))
    old_ca = read_ca(engine, live_project)
    started = container(engine, live_project, "coder-1").raw["State"]["StartedAt"]
    result = live_project.run("ca", "rotate", timeout=300)
    assert result.returncode == 0, result.stderr
    new_ca = read_ca(engine, live_project)
    assert new_ca.startswith("-----BEGIN CERTIFICATE-----") and new_ca != old_ca
    assert container(engine, live_project, "coder-1").raw["State"]["StartedAt"] != started
    assert "nothing to do" in live_project.run("up").stdout


def test_after_rotating_the_proxy_still_serves_the_policy(live_project, engine, agent_image):
    up(live_project, spec(egress={"default": {"allow": ["example.com"]}}, agents={"coder": custom(agent_image)}))
    assert live_project.run("ca", "rotate", timeout=300).returncode == 0
    reached = engine.exec(container(engine, live_project, "coder-1").name, "sh", "-c", 'curl -sS -m 20 -o /dev/null -w "%{http_code}" https://example.com/')
    assert reached.stdout.strip() == "200", reached.stderr


# --- up takes no agent: there are templates, and nothing to select ----------------------------------------------


def test_up_takes_no_agent_names(live_project, engine, agent_image):
    live_project.write(spec(agents={"coder": custom(agent_image)}))
    result = live_project.run("up", "coder")
    assert result.returncode != 0
    assert "unexpected argument" in result.stderr or "unknown command" in result.stderr or "accepts 0 arg" in result.stderr
    assert engine.containers(live_project.name) == []


def test_up_starts_no_agent_and_publishes_the_templates(live_project, engine, agent_image):
    live_project.write(spec(agents={"coder": custom(agent_image), "reviewer": custom(agent_image)}))
    assert live_project.run("up", timeout=300).returncode == 0
    assert container(engine, live_project, "control") and container(engine, live_project, "proxy")
    assert engine.instances(live_project.name) == []
    # a published template can be spawned without the file's help
    (live_project.root / "egzo.yaml").write_text("# emptied after up\n")
    assert live_project.run("spawn", "reviewer", timeout=300).returncode == 0


# --- the proxy's audit trail and rules ---------------------------------------------------------------------------


def test_proxy_log_can_be_filtered_by_agent(live_project, engine, agent_image):
    up(live_project, spec(egress={"default": {"allow": []}}, agents={"coder": custom(agent_image), "reviewer": custom(agent_image)}))
    for name in ("coder-1", "reviewer-1"):
        live_project.run("exec", name, "--", "curl", "-sS", "-m", "10", "-o", "/dev/null", f"https://{name}.example.org/")
    both = [json.loads(l) for l in live_project.run("proxy", "log").stdout.splitlines() if l.startswith("{")]
    assert {e["agent"] for e in both} >= {"coder-1", "reviewer-1"}
    only = [json.loads(l) for l in live_project.run("proxy", "log", "--agent", "coder-1").stdout.splitlines() if l.startswith("{")]
    assert only and {e["agent"] for e in only} == {"coder-1"}


def test_proxy_rules_show_what_each_agent_may_reach_without_any_secret(live_project, engine, agent_image):
    live_project.env["API_SECRET"] = "very-secret-value-1234567890"
    document = spec(
        vaults={"main": {"backend": "env", "secrets": ["API_SECRET"]}},
        egress={
            "default": {"allow": ["docs.example.org"]},
            "wide": {"extend": "default", "services": {"svc": {"hosts": ["api.test"], "inject": {"header": "x-key"}, "secret": "main/API_SECRET"}}},
        },
        agents={"coder": custom(agent_image), "reviewer": custom(agent_image, egress="wide")},
    )
    up(live_project, document)
    rules = live_project.run("proxy", "rules")
    assert rules.returncode == 0, rules.stderr
    assert "docs.example.org" in rules.stdout and "api.test" in rules.stdout
    assert "very-secret-value" not in rules.stdout + rules.stderr
    only = live_project.run("proxy", "rules", "--agent", "coder-1").stdout
    assert "docs.example.org" in only and "api.test" not in only


# --- diff sees what up would do: secrets and prompts too -------------------------------------------------------------


def injecting(image, **extra):
    return spec(
        egress={"default": {"services": {"echo": {"hosts": ["example.com"], "inject": {"header": "X-Key"}, "secret": "main/DEPLOY_TOKEN"}}}},
        agents={"coder": custom(image, **extra)},
    )


def test_diff_sees_a_rotated_secret(live_project, engine, agent_image):
    live_project.write(injecting(agent_image))
    assert live_project.run("up", env={"DEPLOY_TOKEN": "first-value"}, timeout=300).returncode == 0
    assert live_project.run("diff", env={"DEPLOY_TOKEN": "first-value"}).returncode == 0
    changed = live_project.run("diff", env={"DEPLOY_TOKEN": "second-value"})
    assert changed.returncode == 1, changed.stdout
    assert "policy" in changed.stdout
    assert "second-value" not in changed.stdout + changed.stderr


def test_editing_a_prompt_makes_the_running_instances_stale_and_the_next_spawn_new(live_project, engine, agent_image):
    prompt = live_project.root / "prompt.md"
    prompt.write_text("be brief\n")
    up(live_project, spec(agents={"coder": custom(agent_image, prompt="./prompt.md")}))
    before = container(engine, live_project, "coder-1").labels[f"{LABEL_PREFIX}template-hash"]
    assert live_project.run("diff").returncode == 0
    prompt.write_text("be verbose\n")
    changed = live_project.run("diff")
    assert changed.returncode == 1 and "stale: coder-1" in changed.stdout
    assert live_project.run("rm", "--force", "coder-1").returncode == 0
    assert live_project.run("up", timeout=300).returncode == 0
    live_project.spawn("coder")
    assert container(engine, live_project, "coder-1").labels[f"{LABEL_PREFIX}template-hash"] != before


def test_a_restarted_proxy_through_egzo_keeps_diff_clean(live_project, engine, agent_image):
    live_project.write(injecting(agent_image))
    assert live_project.run("up", env={"DEPLOY_TOKEN": "v"}, timeout=300).returncode == 0
    live_project.spawn("coder")
    assert live_project.run("restart", "proxy", env={"DEPLOY_TOKEN": "v"}).returncode == 0
    assert live_project.run("diff", env={"DEPLOY_TOKEN": "v"}).returncode == 0


def test_doctor_checks_that_the_engine_can_make_the_internal_networks_isolation_needs(project, engine):
    result = project.run("doctor", env=engine.env)
    assert "internal networks work" in result.stdout, result.stdout


def test_two_commands_do_not_change_one_project_at_once(live_project, engine, agent_image):
    """While one command holds the project's lock, `up` and `down` refuse, and they work again once it is released."""
    import fcntl

    live_project.write(spec(agents={"coder": custom(agent_image), "second": custom(agent_image)}))
    lock_dir = live_project.root / ".egzo"
    lock_dir.mkdir(exist_ok=True)
    with open(lock_dir / "lock", "w") as holder:
        fcntl.flock(holder, fcntl.LOCK_EX)  # what another egzo command that is changing the project does
        for command in ("up", "down"):
            refused = live_project.run(command, timeout=300)
            assert refused.returncode != 0 and "another egzo command" in refused.stderr, (command, refused.returncode, refused.stderr)
    assert live_project.run("up", timeout=300).returncode == 0, "the lock was not released"
