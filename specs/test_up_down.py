# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""`egzo up` / `egzo down`: reconciliation of the project file with the engine."""

import pytest

from conftest import LABEL_PREFIX
from support import agent, spec


def custom(image, **fields):
    return agent(harness="custom", image=image, **fields)


def test_up_with_templates_starts_the_sidecars_and_no_agent(live_project, engine, agent_image):
    live_project.up(spec(agents={"coder": custom(agent_image), "reviewer": custom(agent_image)}))
    kinds = sorted(r.labels.get(f"{LABEL_PREFIX}kind") for r in engine.containers(live_project.name))
    assert kinds == ["control", "proxy"]
    assert not [r for r in engine.resources(live_project.name) if r.kind == "network" and r.name.endswith(("_coder-1", "_coder", "_reviewer"))]


def test_a_second_up_with_templates_changes_nothing(live_project, engine, agent_image):
    live_project.up(spec(agents={"coder": custom(agent_image)}))
    again = live_project.up()
    assert "nothing to do" in again.stdout
    assert engine.instances(live_project.name) == []


def test_up_does_not_touch_a_running_instance(live_project, engine, agent_image):
    live_project.up(spec(agents={"coder": custom(agent_image)}))
    name = live_project.spawn("coder")
    before = engine.instance(live_project.name, name).raw["Id"]
    live_project.up()
    live_project.sh("project up --recreate", timeout=300)
    assert engine.instance(live_project.name, name).raw["Id"] == before


def test_up_dry_run_with_templates_creates_nothing_and_says_what_it_would_do(live_project, engine, agent_image):
    live_project.write(spec(agents={"coder": custom(agent_image)}))
    result = live_project.sh("project up --dry-run")
    assert result.returncode == 1, result.stderr  # something would change
    assert "control" in result.stdout and "would" in result.stdout
    assert engine.resources(live_project.name) == []


def test_down_removes_the_instances_with_their_networks(live_project, engine, agent_image):
    live_project.up(spec(agents={"coder": custom(agent_image)}))
    first, second = live_project.spawn("coder"), live_project.spawn("coder", "issue-7")
    assert live_project.sh("project down --force").returncode == 0
    remaining = engine.resources(live_project.name)
    assert not [r for r in remaining if r.kind in ("container", "network")], [r.name for r in remaining]
    assert first and second


def test_down_removes_everything_egzo_made_the_home_volumes_the_control_state_and_the_ca(live_project, engine, agent_image):
    live_project.up(spec(agents={"coder": custom(agent_image)}))
    live_project.spawn("coder")
    assert live_project.sh("project down --force").returncode == 0
    assert engine.resources(live_project.name) == []


def test_down_is_refused_while_an_agent_runs_and_removes_nothing(live_project, engine, agent_image):
    live_project.up(spec(agents={"coder": custom(agent_image)}))
    live_project.spawn("coder")
    refused = live_project.sh("project down")
    assert refused.returncode != 0
    assert "coder-1" in refused.stderr and "--force" in refused.stderr
    assert engine.instance(live_project.name, "coder-1") is not None
    assert len(engine.containers(live_project.name)) == 3  # control, proxy, the agent


def test_down_without_force_removes_a_project_whose_agents_are_stopped(live_project, engine, agent_image):
    live_project.up(spec(agents={"coder": custom(agent_image)}))
    live_project.spawn("coder")
    assert live_project.sh("stop coder-1").returncode == 0
    assert live_project.sh("project down").returncode == 0
    assert engine.resources(live_project.name) == []


def test_down_of_a_project_that_is_not_up_succeeds_and_changes_nothing(live_project, engine, agent_image):
    live_project.write(spec(agents={"coder": custom(agent_image)}))
    assert live_project.sh("project down").returncode == 0
    assert engine.resources(live_project.name) == []


