"""Labels: the public contract between egzo and every tool that reads the engine."""

import json
import re

import pytest

from conftest import LABEL_PREFIX
from support import agent, spec

REQUIRED = {"project", "service", "kind", "config-hash", "spec-version", "project-dir", "created-by"}


def up(live_project, image=None, **env):
    """`up`, and with an image one instance of a template `coder`: `coder-1`, whose resources are labelled too."""
    template = {"coder": agent(harness="custom", image=image)} if image else {}
    live_project.write(spec(agents=template))
    result = live_project.run("up", env=env, timeout=300)
    assert result.returncode == 0, result.stderr
    if image:
        live_project.spawn("coder")


def test_the_control_sidecar_carries_the_required_labels(live_project, engine):
    up(live_project)
    control = [r for r in engine.containers(live_project.name) if r.labels.get(f"{LABEL_PREFIX}kind") == "control"]
    assert len(control) == 1
    assert {f"{LABEL_PREFIX}{key}" for key in REQUIRED} <= set(control[0].labels)
    assert control[0].labels[f"{LABEL_PREFIX}project"] == live_project.name
    assert control[0].labels[f"{LABEL_PREFIX}project-dir"] == str(live_project.root)


def test_every_label_follows_the_dns_convention(live_project, engine, agent_image):
    up(live_project, agent_image)
    for resource in engine.resources(live_project.name):
        for key in resource.labels:
            assert not key.startswith("egzo."), f"{resource.kind} {resource.name}: {key}"


def test_label_values_are_short_and_plain(live_project, engine, agent_image):
    up(live_project, agent_image)
    for resource in engine.resources(live_project.name):
        for key, value in resource.labels.items():
            if not key.startswith(LABEL_PREFIX):
                continue
            assert len(value) <= 256, key
            assert not value.lstrip().startswith(("{", "[")), f"{key} looks like JSON"
            looks_base64 = re.fullmatch(r"[A-Za-z0-9+/=]{64,}", value) and not re.fullmatch(r"[0-9a-f]+", value)
            assert not looks_base64, f"{key} looks encoded"


def test_every_resource_of_the_project_is_labelled(live_project, engine, agent_image):
    up(live_project, agent_image)
    assert engine.resources(live_project.name), "nothing was created"
    for resource in engine.resources(live_project.name):
        assert f"{LABEL_PREFIX}project" in resource.labels


def test_no_provider_secret_appears_in_any_resource(live_project, engine, agent_image):
    secret = "sk-spec-secret-0123456789abcdef"
    up(live_project, agent_image, ANTHROPIC_API_KEY=secret)
    for resource in engine.resources(live_project.name):
        assert secret not in json.dumps(resource.raw), f"{resource.kind} {resource.name}"


def instance_resources(engine, project):
    return [r for r in engine.resources(project.name) if r.labels.get(f"{LABEL_PREFIX}instance") == "coder-1"]


def test_an_instance_carries_the_required_labels_and_its_own(live_project, engine, agent_image):
    up(live_project, agent_image)
    container = engine.instance(live_project.name, "coder-1")
    assert {f"{LABEL_PREFIX}{key}" for key in REQUIRED} <= set(container.labels)
    labels = {key.removeprefix(LABEL_PREFIX): value for key, value in container.labels.items()}
    assert labels["kind"] == "agent" and labels["service"] == "coder" and labels["instance"] == "coder-1"
    assert labels["actor"] == "operator"
    assert re.fullmatch(r"[0-9a-f]{16,128}", labels["template-hash"]), labels["template-hash"]
    assert labels["agent.harness"] == "custom"


def test_the_container_the_network_and_the_home_volume_of_an_instance_are_all_labelled_as_its_own(live_project, engine, agent_image):
    up(live_project, agent_image)
    found = {(r.kind, r.name) for r in instance_resources(engine, live_project)}
    assert found == {
        ("container", f"{live_project.name}-coder-1"),
        ("network", f"{live_project.name}_coder-1"),
        ("volume", f"{live_project.name}_coder-1-home"),
    }
    hashes = {r.labels[f"{LABEL_PREFIX}template-hash"] for r in instance_resources(engine, live_project)}
    assert len(hashes) == 1


def test_two_instances_of_a_template_share_its_template_hash(live_project, engine, agent_image):
    up(live_project, agent_image)
    live_project.spawn("coder")
    first, second = (engine.instance(live_project.name, n) for n in ("coder-1", "coder-2"))
    assert first.labels[f"{LABEL_PREFIX}template-hash"] == second.labels[f"{LABEL_PREFIX}template-hash"]
    assert first.labels[f"{LABEL_PREFIX}instance"] != second.labels[f"{LABEL_PREFIX}instance"]


def test_the_template_hash_changes_when_the_template_does(live_project, engine, agent_image):
    up(live_project, agent_image)
    before = engine.instance(live_project.name, "coder-1").labels[f"{LABEL_PREFIX}template-hash"]
    assert live_project.run("rm", "--force", "coder-1").returncode == 0
    live_project.write(spec(agents={"coder": agent(harness="custom", image=agent_image, env={"CHANGED": "yes"})}))
    assert live_project.run("up", timeout=300).returncode == 0
    live_project.spawn("coder")
    assert engine.instance(live_project.name, "coder-1").labels[f"{LABEL_PREFIX}template-hash"] != before
