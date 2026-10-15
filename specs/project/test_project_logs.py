# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""Logs. egzo uses the engine's own logging for every container it makes: it picks no log driver and sets no log options, because the
engine's default is the sysadmin's choice. The control sidecar's own logs are JSON, one object per line, and they are split: the
agents' status goes to standard output (so a client can follow just that, through the engine's log API), everything else the
sidecar says goes to standard error. One project is up for the whole module."""

import json
import time
import uuid

import pytest

from agentapi import Agent
from conftest import LABEL_PREFIX
from support import custom, spec

ACTIVITIES = {"starting", "idle", "working", "blocked"}
STATUS_FIELDS = {"agent", "status", "activity", "open", "waiting", "updated"}


@pytest.fixture(scope="module")
def sandbox(shared_project, engine):
    shared_project.up(spec(agents={"coder": custom(engine.agent_image())}))
    shared_project.spawn("coder")
    return shared_project


def control_of(engine, project):
    found = [c for c in engine.containers(project.name) if c.labels.get(f"{LABEL_PREFIX}service") == "control"]
    assert len(found) == 1, f"expected one control container, found {len(found)}"
    return found[0]


def streams(engine, container):
    """What a container logged, as (standard output lines, standard error lines)."""
    result = engine.run("logs", container.name)
    assert result.returncode == 0, f"cannot read the log of {container.name}: {result.stderr}"
    return result.stdout.splitlines(), result.stderr.splitlines()


def status_lines(engine, project):
    out, _ = streams(engine, control_of(engine, project))
    return [json.loads(line) for line in out if line.strip()]


def eventually(check, timeout=15, message="the condition never became true"):
    deadline = time.time() + timeout
    while True:
        value = check()
        if value:
            return value
        if time.time() > deadline:
            pytest.fail(message)
        time.sleep(0.5)


def engine_default_logging(engine, image):
    """What the engine gives a container nobody configured, which is all `docker inspect` and `podman inspect` share: one is made, inspected and removed."""
    name = f"egzo-spec-logdefault-{uuid.uuid4().hex[:8]}"
    made = engine.run("create", "--name", name, "--entrypoint", "true", image)
    assert made.returncode == 0, f"cannot create a reference container: {made.stderr}"
    try:
        config = json.loads(engine.run("inspect", name).stdout)[0]["HostConfig"]["LogConfig"]
        return config.get("Type"), dict(config.get("Config") or {})
    finally:
        engine.run("rm", "-f", name)


def test_every_container_uses_the_engines_default_logging(sandbox, engine):
    expected = engine_default_logging(engine, engine.agent_image())
    containers = engine.containers(sandbox.name)
    assert {c.labels.get(f"{LABEL_PREFIX}kind") for c in containers} >= {"control", "proxy", "agent"}
    for c in containers:
        config = c.raw["HostConfig"]["LogConfig"]
        assert (config.get("Type"), dict(config.get("Config") or {})) == expected, (
            f"{c.name} was given its own logging: egzo must leave the driver and the options to the engine's default"
        )


def test_control_writes_the_status_of_the_agents_as_json_lines_on_standard_output_and_a_line_when_a_state_changes(sandbox, engine):
    first = eventually(lambda: status_lines(engine, sandbox), message="control logged no status")
    for line in first:
        assert line["type"] == "status" and line["v"] == 1, line
        assert isinstance(line["interval"], int) and line["interval"] >= 1, line
        assert line["project"] == sandbox.name and line["time"], line
        assert line["reason"] in {"start", "change", "heartbeat"}, line
        assert isinstance(line["agents"], list), line
        for status in line["agents"]:
            assert STATUS_FIELDS <= set(status), status
            assert status["activity"] in ACTIVITIES | {""}, status
    assert first[0]["reason"] == "start"

    coder = Agent(engine, sandbox, "coder-1")
    assert coder.call("POST", "/v1/activity", {"state": "working"})[0] == 204
    assert coder.call("POST", "/v1/status", {"text": "refactoring the parser"})[0] == 204

    def latest():
        lines = [l for l in status_lines(engine, sandbox) if any(s["agent"] == "coder-1" for s in l["agents"])]
        return next(s for s in lines[-1]["agents"] if s["agent"] == "coder-1") if lines else None

    status = eventually(lambda: (s := latest()) and s["activity"] == "working" and s["status"] == "refactoring the parser" and s,
                        message="the change never reached the log")
    assert status["waiting"] is False
    assert [l for l in status_lines(engine, sandbox) if l["reason"] == "change"], "state changes are logged with the reason `change`"

    interval = first[0]["interval"]
    heartbeats = eventually(lambda: [l for l in status_lines(engine, sandbox) if l["reason"] == "heartbeat"], timeout=interval + 20,
                            message=f"no heartbeat within {interval} seconds")
    assert any(s["agent"] == "coder-1" for s in heartbeats[0]["agents"]), "a heartbeat carries the whole state"


def test_everything_else_control_says_is_json_on_standard_error(sandbox, engine):
    control = control_of(engine, sandbox)
    # a line nobody can read in the event log is something control says on its way up
    assert engine.exec(control.name, "sh", "-c", "echo 'not json' >> /state/events.jsonl").returncode == 0
    assert engine.run("restart", control.name).returncode == 0

    def said():
        _, err = streams(engine, control_of(engine, sandbox))
        return err if any("unreadable" in line for line in err) else None

    err = eventually(said, message="control said nothing on standard error about the unreadable line")
    for line in err:
        entry = json.loads(line)
        assert entry["level"] in {"debug", "info", "warn", "error"} and entry["msg"] and entry["time"], entry
    out, _ = streams(engine, control_of(engine, sandbox))
    assert out and all(json.loads(line)["type"] == "status" for line in out if line.strip()), "only status lines on standard output"
