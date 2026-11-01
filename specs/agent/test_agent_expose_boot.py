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
    return [e["port"] for e in next(r for r in rows if r["name"] == name).get("exposed") or []]


@pytest.fixture(scope="module")
def worker(shared_project, engine, egzo):
    image = engine.session_image(egzo.binary)
    shared_project.up(spec(agents={"web": agent(harness="custom", image=image, env={"FAKE_TUI": "exposes"})}))
    return Agent(engine, shared_project, shared_project.spawn("web", "host"))


def test_what_a_harness_exposes_first_outlives_the_boot_and_the_rest_does_not(worker, shared_project):
    assert eventually(lambda: ports(shared_project, worker.name) == [3200], seconds=30)
    first = worker.tool("expose", port=3201)["structuredContent"]["id"]
    assert ports(shared_project, worker.name) == [3200, 3201]
    shared_project.ok("agent stop", worker.name)
    shared_project.ok("agent start", worker.name)
    assert eventually(lambda: ports(shared_project, worker.name) == [3200], seconds=30)
    time.sleep(3)  # whatever would end the exposures after the harness started has had the time to
    assert ports(shared_project, worker.name) == [3200]
    assert worker.tool("expose", port=3201)["structuredContent"]["id"] == first, "a port gets its id back"
