# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""A restart ends what an agent exposed, and a harness may expose a port as the first thing it does: the end of the old exposures comes
before the harness, so it cannot undo that."""

import json
import time

import pytest

from agentapi import Agent
from hubkit import eventually
from support import agent, spec


def ports(project, name):
    rows = json.loads(project.ok("ps --json").stdout)
    return [e["name"] for e in next(r for r in rows if r["name"] == name).get("exposed") or []]


@pytest.fixture(scope="module")
def worker(shared_project, engine, egzo):
    image = engine.session_image(egzo.binary)
    shared_project.up(spec(
        local_ports={"bind": "127.0.0.1"},
        agents={"web": agent(harness="custom", image=image, env={"FAKE_TUI": "exposes"}, ports={"early": "3200/tcp", "late": "3201/tcp"})},
    ))
    return Agent(engine, shared_project, shared_project.spawn("web", "host"))


def test_what_a_harness_exposes_first_outlives_the_boot_and_the_rest_does_not(worker, shared_project):
    assert eventually(lambda: ports(shared_project, worker.name) == ["early"], seconds=30)
    late = worker.tool("expose", name="late", where="local")["structuredContent"]["address"]
    assert ports(shared_project, worker.name) == ["early", "late"]
    shared_project.ok("agent stop", worker.name)
    shared_project.ok("agent start", worker.name)
    assert eventually(lambda: ports(shared_project, worker.name) == ["early"], seconds=30)
    time.sleep(3)  # whatever would end the exposures after the harness started has had the time to
    assert ports(shared_project, worker.name) == ["early"]
    assert worker.tool("expose", name="late", where="local")["structuredContent"]["address"] == late, "a port keeps its address"
