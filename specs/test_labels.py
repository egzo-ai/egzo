"""Labels: the public contract between egzo and every tool that reads the engine."""

import json
import re

import pytest

from conftest import LABEL_PREFIX
from support import spec

REQUIRED = {"project", "service", "kind", "config-hash", "spec-version", "project-dir", "created-by"}


def up(live_project, **env):
    live_project.write(spec())
    result = live_project.run("up", env=env)
    assert result.returncode == 0, result.stderr


def test_the_control_sidecar_carries_the_required_labels(live_project, engine):
    up(live_project)
    control = [r for r in engine.containers(live_project.name) if r.labels.get(f"{LABEL_PREFIX}kind") == "control"]
    assert len(control) == 1
    assert {f"{LABEL_PREFIX}{key}" for key in REQUIRED} <= set(control[0].labels)
    assert control[0].labels[f"{LABEL_PREFIX}project"] == live_project.name
    assert control[0].labels[f"{LABEL_PREFIX}project-dir"] == str(live_project.root)


def test_every_label_follows_the_dns_convention(live_project, engine):
    up(live_project)
    for resource in engine.resources(live_project.name):
        for key in resource.labels:
            assert not key.startswith("egzo."), f"{resource.kind} {resource.name}: {key}"


def test_label_values_are_short_and_plain(live_project, engine):
    up(live_project)
    for resource in engine.resources(live_project.name):
        for key, value in resource.labels.items():
            if not key.startswith(LABEL_PREFIX):
                continue
            assert len(value) <= 256, key
            assert not value.lstrip().startswith(("{", "[")), f"{key} looks like JSON"
            looks_base64 = re.fullmatch(r"[A-Za-z0-9+/=]{64,}", value) and not re.fullmatch(r"[0-9a-f]+", value)
            assert not looks_base64, f"{key} looks encoded"


def test_every_resource_of_the_project_is_labelled(live_project, engine):
    up(live_project)
    assert engine.resources(live_project.name), "nothing was created"
    for resource in engine.resources(live_project.name):
        assert f"{LABEL_PREFIX}project" in resource.labels


def test_no_provider_secret_appears_in_any_resource(live_project, engine):
    secret = "sk-spec-secret-0123456789abcdef"
    up(live_project, ANTHROPIC_API_KEY=secret)
    for resource in engine.resources(live_project.name):
        assert secret not in json.dumps(resource.raw), f"{resource.kind} {resource.name}"
