# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""The spec snapshot: the resolved project, kept on the control volume so any tool can read it. One story: what `up` stores, what it
leaves out, when it stores a new one, and what `down` removes."""

import json

import yaml

from conftest import LABEL_PREFIX
from support import custom, spec

SECRET = "sk-spec-snapshot-secret-0123456789"


def test_up_stores_the_resolved_project_on_the_control_volume_without_secrets_and_keeps_the_history_until_down(live_project, engine, agent_image):
    p = live_project

    def control(*request):
        sidecar = next(r for r in engine.containers(p.name) if r.labels.get(f"{LABEL_PREFIX}service") == "control")
        return engine.exec(sidecar.name, "/egzo", "control", "request", *request)

    def snapshots():
        return control("GET", "/v1/specs").stdout.split()

    def up(document, **env):
        p.write(document)
        return p.ok("project up", env=env, timeout=300)

    document = spec(
        egress={"default": {"services": {"anthropic": "main/ANTHROPIC_API_KEY"}, "allow": ["platform.claude.com"]}},
        agents={"coder": custom(agent_image)},
    )
    up(document, ANTHROPIC_API_KEY=SECRET)
    [name] = snapshots()
    text = control("GET", f"/v1/specs/{name}").stdout
    stored = yaml.safe_load(text)
    assert stored["config"] == p.sh("project check --resolved").yaml()
    assert stored["spec-version"] == "1" and stored["created-by"].startswith("egzo/")
    for resource in engine.containers(p.name):
        assert stored["config-hashes"][resource.name] == resource.labels[f"{LABEL_PREFIX}config-hash"]
    assert SECRET not in text and "main/ANTHROPIC_API_KEY" in text, "the reference is kept, never the value"

    assert "nothing to do" in up(document, ANTHROPIC_API_KEY=SECRET).stdout
    assert snapshots() == [name], "a second up with the same configuration stores nothing new"
    changed = spec(workspaces={"scratch": {}}, egress=document["egress"], agents=document["agents"])
    up(changed, ANTHROPIC_API_KEY=SECRET)
    assert len(snapshots()) == 2 and name in snapshots(), "a changed configuration adds a snapshot and keeps the history"

    assert control("PUT", "/v1/specs/..%2F..%2Fetc").returncode != 0, "snapshot names are validated"
    assert len(snapshots()) == 2

    p.ok("project down")
    up(changed, ANTHROPIC_API_KEY=SECRET)
    assert len(snapshots()) == 1, "the history of the project that was taken down is not kept"
