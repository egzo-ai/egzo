# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""The spec snapshot: the resolved project, kept on the control volume so any tool can read it."""

import json

import yaml

from conftest import LABEL_PREFIX
from support import agent, spec

SECRET = "sk-spec-snapshot-secret-0123456789"


def custom(image, **fields):
    return agent(harness="custom", image=image, **fields)


def control_request(engine, project, *request):
    control = [r for r in engine.containers(project.name) if r.labels.get(f"{LABEL_PREFIX}service") == "control"][0]
    return engine.exec(control.name, "/egzo", "control", "request", *request)


def snapshots(engine, project):
    return control_request(engine, project, "GET", "/specs").stdout.split()


def up(project, document, **env):
    project.write(document)
    result = project.run("up", env=env, timeout=300)
    assert result.returncode == 0, result.stderr
    return result


def test_up_stores_the_resolved_project_on_the_control_volume(live_project, engine):
    up(live_project, spec())
    [name] = snapshots(engine, live_project)
    stored = yaml.safe_load(control_request(engine, live_project, "GET", f"/specs/{name}").stdout)
    assert stored["config"] == live_project.run("config").yaml()
    assert stored["spec-version"] == "1"
    assert stored["created-by"].startswith("egzo/")


def test_the_snapshot_records_the_config_hash_of_every_container(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image)}))
    [name] = snapshots(engine, live_project)
    stored = yaml.safe_load(control_request(engine, live_project, "GET", f"/specs/{name}").stdout)
    for resource in engine.containers(live_project.name):
        assert stored["config-hashes"][resource.name] == resource.labels[f"{LABEL_PREFIX}config-hash"]


def test_the_snapshot_never_holds_a_secret_value(live_project, engine, agent_image):
    document = spec(
        egress={"default": {"services": {"anthropic": "main/ANTHROPIC_API_KEY"}, "allow": ["platform.claude.com"]}},
        agents={"coder": custom(agent_image)},
    )
    up(live_project, document, ANTHROPIC_API_KEY=SECRET)
    [name] = snapshots(engine, live_project)
    text = control_request(engine, live_project, "GET", f"/specs/{name}").stdout
    assert SECRET not in text
    assert "main/ANTHROPIC_API_KEY" in text  # the reference is kept


def test_a_second_up_with_the_same_configuration_stores_nothing_new(live_project, engine):
    up(live_project, spec())
    again = up(live_project, spec())
    assert "nothing to do" in again.stdout
    assert len(snapshots(engine, live_project)) == 1


def test_a_changed_configuration_adds_a_snapshot_and_keeps_the_history(live_project, engine):
    up(live_project, spec())
    first = snapshots(engine, live_project)
    up(live_project, spec(workspaces={"scratch": {}}))
    assert len(snapshots(engine, live_project)) == 2
    assert set(first) < set(snapshots(engine, live_project))


def test_the_snapshot_survives_down_without_volumes(live_project, engine):
    up(live_project, spec())
    [name] = snapshots(engine, live_project)
    assert live_project.run("down").returncode == 0
    up(live_project, spec())
    assert snapshots(engine, live_project) == [name]


def test_snapshot_names_are_validated(live_project, engine):
    up(live_project, spec())
    refused = control_request(engine, live_project, "PUT", "/specs/..%2F..%2Fetc")
    assert refused.returncode != 0
    assert json.dumps(snapshots(engine, live_project))
