# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""Labels: the public contract between egzo and every tool that reads the engine. One project is up for the whole module, with an
instance of a custom template and one of a harness that has a home volume."""

import re

import pytest

from conftest import LABEL_PREFIX
from support import agent, custom, spec

REQUIRED = {"project", "service", "kind", "config-hash", "spec-version", "project-dir", "created-by"}


@pytest.fixture(scope="module")
def sandbox(shared_project, engine):
    image = engine.agent_image()
    shared_project.up(spec(agents={"coder": custom(image), "homed": agent(harness="claude-code", image=image)}))
    shared_project.spawn("coder")
    shared_project.spawn("coder")
    shared_project.spawn("homed")
    return shared_project


def test_every_container_network_and_volume_carries_the_labels_of_the_contract(sandbox, engine):
    p = sandbox
    control = [r for r in engine.containers(p.name) if r.labels.get(f"{LABEL_PREFIX}kind") == "control"]
    assert len(control) == 1
    assert {f"{LABEL_PREFIX}{key}" for key in REQUIRED} <= set(control[0].labels)
    assert control[0].labels[f"{LABEL_PREFIX}project"] == p.name
    assert control[0].labels[f"{LABEL_PREFIX}project-dir"] == str(p.root)

    for resource in engine.resources(p.name):
        for key, value in resource.labels.items():
            assert not key.startswith("egzo."), f"{resource.kind} {resource.name}: {key} does not follow the DNS convention"
            if not key.startswith(LABEL_PREFIX):
                continue
            assert len(value) <= 256, key
            assert not value.lstrip().startswith(("{", "[")), f"{key} looks like JSON"
            looks_base64 = re.fullmatch(r"[A-Za-z0-9+/=]{64,}", value) and not re.fullmatch(r"[0-9a-f]+", value)
            assert not looks_base64, f"{key} looks encoded"


def test_an_instance_carries_the_required_labels_and_its_own_on_its_container_network_and_home_volume(sandbox, engine):
    p = sandbox
    container = engine.instance(p.name, "coder-1")
    assert {f"{LABEL_PREFIX}{key}" for key in REQUIRED} <= set(container.labels)
    labels = {key.removeprefix(LABEL_PREFIX): value for key, value in container.labels.items()}
    assert labels["kind"] == "agent" and labels["service"] == "coder" and labels["instance"] == "coder-1"
    assert labels["actor"] == "operator" and labels["agent.harness"] == "custom"
    assert re.fullmatch(r"[0-9a-f]{16,128}", labels["template-hash"]), labels["template-hash"]

    second = engine.instance(p.name, "coder-2")
    assert second.labels[f"{LABEL_PREFIX}template-hash"] == labels["template-hash"], "two instances of a template share its hash"
    assert second.labels[f"{LABEL_PREFIX}instance"] != labels["instance"]

    # a custom harness has no home volume; a harness with a home has one, and all three resources are the instance's own
    own = {(r.kind, r.name) for r in engine.resources(p.name) if r.labels.get(f"{LABEL_PREFIX}instance") == "homed-1"}
    assert own == {("container", f"{p.name}-homed-1"), ("network", f"{p.name}_homed-1"), ("volume", f"{p.name}_homed-1-home")}
    hashes = {r.labels[f"{LABEL_PREFIX}template-hash"] for r in engine.resources(p.name) if r.labels.get(f"{LABEL_PREFIX}instance") == "homed-1"}
    assert len(hashes) == 1
