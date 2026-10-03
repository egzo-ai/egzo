"""`egzo up` / `egzo down`: reconciliation of the project file with the engine."""

import pytest

from support import spec


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


@pytest.mark.todo
def test_dry_run_creates_nothing(live_project, engine):
    live_project.write(spec())
    result = live_project.run("up", "--dry-run")
    assert result.returncode == 0, result.stderr
    assert engine.resources(live_project.name) == []


@pytest.mark.todo
def test_up_creates_the_control_sidecar(live_project, engine):
    live_project.write(spec())
    assert live_project.run("up").returncode == 0
    assert engine.containers(live_project.name)


@pytest.mark.todo
def test_a_second_up_changes_nothing(live_project, engine):
    live_project.write(spec())
    assert live_project.run("up").returncode == 0
    before = {c.name for c in engine.containers(live_project.name)}
    assert live_project.run("up").returncode == 0
    assert {c.name for c in engine.containers(live_project.name)} == before


@pytest.mark.todo
def test_up_returns_once_converged(live_project):
    live_project.write(spec())
    result = live_project.run("up", timeout=300)
    assert result.returncode == 0, result.stderr


@pytest.mark.todo
def test_down_removes_the_containers_and_networks(live_project, engine):
    live_project.write(spec())
    assert live_project.run("up").returncode == 0
    assert live_project.run("down").returncode == 0
    remaining = engine.resources(live_project.name)
    assert not [r for r in remaining if r.kind in ("container", "network")]


@pytest.mark.todo
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
