"""Where images come from: the default name of a harness image, and pulling it from a registry."""

import pytest

from conftest import LABEL_PREFIX
from support import agent, spec

pytestmark = pytest.mark.usefixtures("engine")


def container(engine, project, service):
    return [r for r in engine.containers(project.name) if r.labels.get(f"{LABEL_PREFIX}service") == service][0]


def test_a_harness_without_an_image_is_pulled_by_its_default_name_from_the_configured_registry(
    live_project, engine, session_image, registry
):
    """EGZO_HARNESS_PREFIX replaces ghcr.io/egzo-ai/egzo-harness-: the name is <prefix><harness>:<egzo version>."""
    version = live_project.run("version").stdout.split()[-1]
    remote = registry.push(session_image, "egzo-harness-opencode", version)
    registry.forget(remote)
    assert engine.run("image", "inspect", remote).returncode != 0
    live_project.env["EGZO_HARNESS_PREFIX"] = f"{registry.host}/egzo-harness-"
    live_project.write(spec(agents={"coder": agent(harness="opencode", env={"FAKE_TUI": "cat"})}))
    result = live_project.run("up", timeout=600)
    assert result.returncode == 0, result.stderr
    assert container(engine, live_project, "coder").raw["Config"]["Image"] == remote


def test_an_unpullable_harness_image_stops_up_naming_the_image_and_the_override(live_project, engine):
    live_project.env["EGZO_HARNESS_PREFIX"] = "localhost:5000/does-not-exist-"
    live_project.write(spec(agents={"coder": agent(harness="opencode")}))
    result = live_project.run("up", timeout=300)
    assert result.returncode != 0
    assert "localhost:5000/does-not-exist-opencode" in result.stderr and "EGZO_HARNESS_PREFIX" in result.stderr


def test_version_prints_the_version(project):
    result = project.run("version")
    assert result.returncode == 0 and result.stdout.strip()
