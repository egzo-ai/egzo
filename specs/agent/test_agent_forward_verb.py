# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""What a hub reaches through the link: the `forward` verb, a stream to a port an agent exposed, and to no other. The server is
fixtures/forward-server.py, bound to 127.0.0.1 in the agent; the hub is fixtures/fakehub, which is core's real hub side of the protocol."""

import pytest

from agentapi import Agent
from hubkit import eventually
from support import agent, spec

ECHO, BYE, DEAF = 7100, 7101, 7103
TIMEOUT = 30


@pytest.fixture(scope="module")
def host(shared_project, engine, egzo, fake_hub):
    image = engine.forward_image(egzo.binary)
    shared_project.up(spec(hub={"url": fake_hub.url, "ca": str(fake_hub.ca)}, agents={"web": agent(harness="custom", image=image, env={"FAKE_TUI": "cat"})}))
    shared_project.ok("project register", input=fake_hub.code("alice") + "\n")
    worker = Agent(engine, shared_project, shared_project.spawn("web", "host"))
    container = engine.instance(shared_project.name, "host").name
    assert engine.exec(container, "forward-server", str(ECHO), detach=True).returncode == 0
    assert eventually(lambda: engine.exec(container, "nc", "-z", "127.0.0.1", str(ECHO)).returncode == 0, seconds=TIMEOUT)
    return worker


@pytest.fixture
def forward(host, fake_hub, shared_project):
    def ask(port, target="host", **fields):
        return fake_hub.verb(shared_project.name, "forward", Agent=target, Port=port, **{"Seconds": 5, **fields})

    return ask


def exposed(worker, forward, port, protocol):
    """Expose a port, and wait for the link to hear of it from the status feed."""
    assert not worker.tool("expose", port=port, protocol=protocol).get("isError")
    assert eventually(lambda: "does not expose" not in (forward(port, Seconds=0.1).get("error") or ""), seconds=10)


def forwards_in(engine, worker):
    return [line for line in engine.exec(worker.container.name, "ps", "-o", "args").stdout.splitlines() if "agent forward" in line]


def test_a_port_that_was_not_exposed_is_refused(host, forward):
    assert "does not expose port 7104" in forward(7104)["error"]


def test_an_exposed_port_is_reached_on_loopback_and_tcp_keeps_a_half_close(host, forward):
    exposed(host, forward, ECHO, "tcp")
    answer = forward(ECHO, Input="hello", HalfClose=True)
    assert not answer.get("error")
    assert answer["result"].startswith("hello\n5 "), "the server saw the end of the input and still answered"


def test_a_server_that_speaks_first_is_heard(host, forward):
    exposed(host, forward, BYE, "http")
    assert forward(BYE)["result"] == "bye\n"


def test_http_ends_with_the_end_of_the_input_and_leaves_nothing_in_the_agent(host, forward, engine):
    exposed(host, forward, DEAF, "http")
    forward(DEAF, Input="x", HalfClose=True, Seconds=0.5)
    assert eventually(lambda: not forwards_in(engine, host), seconds=10), "a client that left holds an exec"


def test_nothing_listening_is_an_answer(host, forward):
    exposed(host, forward, 7150, "http")
    assert "nothing listens on port 7150" in forward(7150)["error"]


def test_unexpose_closes_the_way(host, forward):
    exposed(host, forward, 7151, "http")
    host.tool("unexpose", port=7151)
    assert eventually(lambda: "does not expose" in (forward(7151, Seconds=0.1).get("error") or ""), seconds=10)


def test_a_stopped_agent_is_said_to_be(host, forward, shared_project, engine):
    other = Agent(engine, shared_project, shared_project.spawn("web", "stopped"))
    exposed(other, lambda *a, **k: forward(*a, target="stopped", **k), 7152, "http")
    shared_project.ok("agent stop stopped")
    assert eventually(lambda: "not running" in (forward(7152, target="stopped").get("error") or ""), seconds=10)
