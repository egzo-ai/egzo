# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""What an agent may reach, as the agent sees it: `access` (the agent API's `GET /v1/access`, the MCP tool, and `egzo agent
access`) answers the egress profile of the agent's template, the hosts it allows, each service with how to use it, and the ssh
servers, and never a secret or where one is kept. When `up` changes what it answers for a running agent, the agent gets a
request from egzo that says what changed; the agents whose access did not change get nothing. One project is up for the
module; a spec that changes the file puts it back."""

import json

import pytest

from agentapi import Agent
from support import custom, spec

GH_TOKEN = "ghp_spec-never-shown-0123456789"
SITE_SECRET = "site-never-shown-0123456789"


def document(image, session_image, team_allow=("example.com",)):
    team = {
        "allow": list(team_allow),
        "services": {
            "github": "env/GH_TOKEN",
            "site": {"hosts": ["httpbin.org"], "credential": "env/SITE_SECRET", "placeholder": "SITE_PASSWORD"},
            "docs": {"hosts": ["example.net"], "inspect": True},
        },
    }
    return spec(
        egress={"team": team, "plain": {"allow": ["example.org"]}},
        agents={
            "team": custom(image, egress="team"),
            "plain": custom(image, egress="plain"),
            "session": custom(session_image, egress="team", env={"FAKE_TUI": "cat"}),
        },
    )


@pytest.fixture(scope="module")
def sandbox(shared_project, engine, egzo):
    shared_project.env.update({"GH_TOKEN": GH_TOKEN, "SITE_SECRET": SITE_SECRET})
    shared_project.up(document(engine.agent_image(), engine.session_image(egzo.binary)))
    return shared_project


@pytest.fixture(autouse=True)
def start_from_no_instance(sandbox, engine, egzo):
    sandbox.retire()
    sandbox.write(document(engine.agent_image(), engine.session_image(egzo.binary)))
    yield


def access_of(agent):
    code, body = agent.call("GET", "/v1/access")
    assert code == 200, body
    return body


def from_egzo(agent):
    """The messages egzo sent the agent, oldest first, whatever their state: the agents of this module have no session holder
    to announce them, so they are read from the project's events."""
    events = [json.loads(line) for line in agent.project.run("events").stdout.splitlines() if line.startswith("{")]
    return [
        {"id": e["id"], "text": e["text"], "kind": e["data"]["kind"]}
        for e in events
        if e["type"] == "message" and e.get("actor") == "egzo" and e["data"]["to"] == agent.address
    ]


def closed_by_egzo(agent):
    """The messages egzo itself closed for the agent (id to outcome): the notices a later change replaced."""
    events = [json.loads(line) for line in agent.project.run("events").stdout.splitlines() if line.startswith("{")]
    return {e["id"]: e["data"]["outcome"] for e in events if e["type"] == "resolved" and e.get("actor") == "egzo" and e.get("agent") == agent.name}


def test_an_agent_sees_its_profile_its_services_and_how_to_use_them_and_never_a_secret(sandbox, engine):
    team = Agent(engine, sandbox, sandbox.spawn("team"))
    plain = Agent(engine, sandbox, sandbox.spawn("plain"))

    access = access_of(team)
    assert access["profile"] == "team"
    assert access["allow"] == ["example.com"]
    assert "refused" in access["other_hosts"], "it says that every other host is refused"
    services = {s["name"]: s for s in access["services"]}
    assert set(services) == {"github", "site", "docs"}
    assert services["github"]["credential"] == "injected" and sorted(services["github"]["hosts"]) == ["api.github.com", "github.com"]
    assert "GH_TOKEN" in services["github"]["how"], "a hint for the well-known client: any token, the proxy sets the real one"
    assert services["site"]["credential"] == "placeholder" and services["site"]["variable"] == "SITE_PASSWORD"
    assert services["docs"]["credential"] == "none" and services["docs"]["inspect"] is True
    assert access["ssh"] == []

    raw = json.dumps(access)
    assert GH_TOKEN not in raw and SITE_SECRET not in raw and "env/" not in raw, "never a secret, never where one is kept"

    assert access_of(plain) == {**access_of(plain), "profile": "plain", "allow": ["example.org"], "services": []}, "each agent its own"

    tool = team.tool("access")
    assert not tool.get("isError"), tool
    assert json.loads(tool["content"][0]["text"]) == access, "the MCP tool answers the same"


def test_egzo_agent_access_says_the_same_in_words(sandbox, engine):
    session = Agent(engine, sandbox, sandbox.spawn("session"))
    printed = session.exec("egzo agent access")
    assert printed.returncode == 0, printed.stderr
    lines = printed.stdout.splitlines()
    assert "egress profile: team" in lines and "allowed hosts: example.com" in lines
    assert any(line.startswith("service site: httpbin.org: ") and "$SITE_PASSWORD" in line for line in lines), printed.stdout
    assert GH_TOKEN not in printed.stdout and SITE_SECRET not in printed.stdout
    as_json = session.exec("egzo agent access --json")
    assert as_json.returncode == 0, as_json.stderr
    answered = json.loads(as_json.stdout)
    assert answered["profile"] == "team" and answered["allow"] == ["example.com"], "--json is what the tool answers"


def test_egzo_agent_access_outside_an_agent_says_it_runs_inside_one(sandbox):
    for outside in (sandbox.sh("agent access"), sandbox.sh("agent access --json")):
        assert outside.returncode != 0 and "agent container" in outside.stderr, outside.stderr


def test_when_up_changes_what_an_agent_can_reach_the_agent_is_told_what_changed_and_the_others_are_not(sandbox, engine, egzo):
    team = Agent(engine, sandbox, sandbox.spawn("team"))
    plain = Agent(engine, sandbox, sandbox.spawn("plain"))
    assert from_egzo(team) == [] and from_egzo(plain) == [], "a new agent gets its access without a message"

    sandbox.write(document(engine.agent_image(), engine.session_image(egzo.binary), team_allow=("example.com", "pypi.org")))
    try:
        sandbox.ok("project up", timeout=300)
        assert access_of(team)["allow"] == ["example.com", "pypi.org"], "the running agent sees it at once"
        (told,) = from_egzo(team)
        assert told["kind"] == "request" and "hosts now allowed: pypi.org." in told["text"] and "access" in told["text"]
        assert from_egzo(plain) == [], "an agent whose access did not change is not told"

        sandbox.ok("project up", timeout=300)
        assert len(from_egzo(team)) == 1, "an up that changes nothing tells nobody"
    finally:
        sandbox.write(document(engine.agent_image(), engine.session_image(egzo.binary)))
        sandbox.ok("project up", timeout=300)
    assert access_of(team)["allow"] == ["example.com"]
    assert len(from_egzo(team)) == 1, "back to the access it knew before the notice it did not resolve: nothing new to say"
    assert closed_by_egzo(team) == {told["id"]: "superseded"}, "and that notice is closed: it says nothing true any more"

    # once the agent resolved what it was told, taking a host back is told like any change
    sandbox.write(document(engine.agent_image(), engine.session_image(egzo.binary), team_allow=("example.com", "pypi.org")))
    try:
        sandbox.ok("project up", timeout=300)
        added = from_egzo(team)[-1]
        assert team.call("GET", f"/v1/messages/{added['id']}")[0] == 200
        assert team.resolve(added["id"], "read it")[0] == 200
    finally:
        sandbox.write(document(engine.agent_image(), engine.session_image(egzo.binary)))
        sandbox.ok("project up", timeout=300)
    assert "hosts no longer allowed: pypi.org." in from_egzo(team)[-1]["text"], "taking it back is a change too"


def test_an_agent_cannot_message_egzo(sandbox, engine):
    team = Agent(engine, sandbox, sandbox.spawn("team"))
    code, body = team.send("egzo", "hello")
    assert code == 400, body
