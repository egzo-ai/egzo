"""`egzo up` / `egzo down`: reconciliation of the project file with the engine."""

import pytest

from conftest import LABEL_PREFIX
from support import agent, spec


def test_up_has_no_detach_flag(project):
    project.write(spec())
    result = project.run("up", "-d")
    assert result.returncode != 0
    assert "-d" in result.stderr


def test_up_documents_dry_run_with_a_long_flag_only(project):
    result = project.run("up", "--help")
    assert result.returncode == 0
    assert "--dry-run" in result.stdout
    assert "-n," not in result.stdout


def test_dry_run_creates_nothing(live_project, engine):
    live_project.write(spec())
    result = live_project.run("up", "--dry-run")
    assert result.returncode == 0, result.stderr
    assert engine.resources(live_project.name) == []


def test_up_creates_the_control_sidecar(live_project, engine):
    live_project.write(spec())
    assert live_project.run("up").returncode == 0
    assert engine.containers(live_project.name)


def test_a_second_up_changes_nothing(live_project, engine):
    live_project.write(spec())
    assert live_project.run("up").returncode == 0
    before = {c.name for c in engine.containers(live_project.name)}
    assert live_project.run("up").returncode == 0
    assert {c.name for c in engine.containers(live_project.name)} == before


def test_up_returns_once_converged(live_project):
    live_project.write(spec())
    result = live_project.run("up", timeout=300)
    assert result.returncode == 0, result.stderr


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
    live_project.run("up", "--recreate", timeout=300)
    assert engine.instance(live_project.name, name).raw["Id"] == before


def test_up_dry_run_with_templates_creates_nothing_and_says_what_it_would_do(live_project, engine, agent_image):
    live_project.write(spec(agents={"coder": custom(agent_image)}))
    result = live_project.run("up", "--dry-run")
    assert result.returncode == 0, result.stderr
    assert "control" in result.stdout and "would" in result.stdout
    assert engine.resources(live_project.name) == []


def test_down_removes_the_instances_with_their_networks(live_project, engine, agent_image):
    live_project.up(spec(agents={"coder": custom(agent_image)}))
    first, second = live_project.spawn("coder"), live_project.spawn("coder", "issue-7")
    assert live_project.run("down").returncode == 0
    remaining = engine.resources(live_project.name)
    assert not [r for r in remaining if r.kind in ("container", "network")], [r.name for r in remaining]
    assert first and second


def test_down_volumes_removes_the_home_volumes_of_the_instances_too(live_project, engine, agent_image):
    live_project.up(spec(agents={"coder": custom(agent_image)}))
    live_project.spawn("coder")
    assert live_project.run("down", "--volumes").returncode == 0
    assert engine.resources(live_project.name) == []


def test_down_removes_the_containers_and_networks(live_project, engine):
    live_project.write(spec())
    assert live_project.run("up").returncode == 0
    assert live_project.run("down").returncode == 0
    remaining = engine.resources(live_project.name)
    assert not [r for r in remaining if r.kind in ("container", "network")]


def test_down_volumes_removes_the_volumes_too(live_project, engine):
    live_project.write(spec())
    assert live_project.run("up").returncode == 0
    assert live_project.run("down", "--volumes").returncode == 0
    assert engine.resources(live_project.name) == []


def test_down_never_deletes_workspace_directories(live_project, engine):
    live_project.write(
        spec(workspaces={"repo": {"git": {"url": "https://github.com/acme/shop.git"}, "path": "./clones/repo"}})
    )
    marker = live_project.mkdir("clones/repo/agent") / "unpushed-work.txt"
    marker.write_text("do not lose me")
    live_project.run("down", "--volumes")
    assert marker.read_text() == "do not lose me"
