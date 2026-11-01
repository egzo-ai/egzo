# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""The `expose` and `unexpose` tools: an agent opens one of the ports its template declares, by name, for the hub or for the local machine,
and gets the one address where the app can be reached.

This project has local ports and no hub, so `local` is the place. With a hub: test_agent_expose_hub.py. Nothing here starts a server in the
agent: nothing is checked at `expose` time, and a server may start after it. What reaches the server through the relay: test_agent_local_ports.py."""

import json
import re

import pytest

from agentapi import Agent
from hubkit import eventually
from support import custom, spec

PORTS = {"web": "8080/http", "db": "5432/tcp", "voice": "5432/udp", "docs": "8081/http"}
HTTP_ADDRESS = re.compile(r"^http://127\.0\.0\.1:(\d+)/$")
RAW_ADDRESS = re.compile(r"^127\.0\.0\.1:(\d+)$")


@pytest.fixture(scope="module")
def ported(shared_project, engine, egzo):
    shared_project.up(spec(local_ports={"bind": "127.0.0.1"}, agents={"worker": custom(engine.session_image(egzo.binary), ports=PORTS)}))
    return shared_project


@pytest.fixture(scope="module")
def two(ported, engine):
    return [Agent(engine, ported, ported.spawn("worker", name)) for name in ("coder", "reviewer")]


@pytest.fixture
def pair(ported, two):
    """The module's two agents with nothing exposed: every agent is a network of its own, and a machine has a limited number of them."""
    yield two
    for worker in two:
        for exposure in exposed(ported, worker.name):
            worker.tool("unexpose", name=exposure["name"])


@pytest.fixture
def fresh(ported, engine):
    """Spawn an agent and take it away again: for a spec that removes what it made."""
    spawned = []

    def spawn(name=None):
        spawned.append(Agent(engine, ported, ported.spawn("worker", name)))
        return spawned[-1]

    yield spawn
    for worker in spawned:
        ported.run("agent", "rm", "--force", worker.name)


def answer(result):
    """What a tool answered, or what it refused."""
    assert "structuredContent" in result or result.get("isError"), result
    return result.get("structuredContent", result)


def expose(worker, name, where="local"):
    return answer(worker.tool("expose", name=name, where=where))


def refused(worker, name, where="local"):
    result = worker.tool("expose", name=name, where=where)
    assert result.get("isError"), result
    return result["content"][0]["text"]


def exposed(project, name):
    return next(r for r in json.loads(project.ok("ps --json").stdout) if r["name"] == name).get("exposed") or []


def status_lines(engine, project):
    logs = engine.run("logs", f"{project.name}-control-1").stdout
    return [line for line in map(json.loads, filter(None, logs.splitlines())) if line.get("type") == "status"]


def latest_exposed(engine, project, name):
    agents = status_lines(engine, project)[-1]["agents"]
    return next(a for a in agents if a["agent"] == name).get("exposed") or []


NOTE = "Active connections may remain active - close your server to end them."


def test_a_local_http_port_answers_an_http_address_and_a_raw_one_host_and_port(pair):
    a, _ = pair
    web, db, voice = expose(a, "web"), expose(a, "db"), expose(a, "voice")
    assert HTTP_ADDRESS.fullmatch(web["address"]) and RAW_ADDRESS.fullmatch(db["address"]) and RAW_ADDRESS.fullmatch(voice["address"])
    assert (web["name"], web["port"], web["protocol"], web["where"]) == ("web", 8080, "http", "local")
    assert (db["port"], db["protocol"]) == (5432, "tcp") and (voice["port"], voice["protocol"]) == (5432, "udp")
    ports = {int(HTTP_ADDRESS.fullmatch(web["address"]).group(1)), int(RAW_ADDRESS.fullmatch(db["address"]).group(1)), int(RAW_ADDRESS.fullmatch(voice["address"]).group(1))}
    assert len(ports) == 3 and all(20000 <= port <= 29999 for port in ports), "three different ports of the default pool"
    assert "id" not in web, "only a preview has an id"


def test_the_same_call_again_is_the_same_answer(pair):
    a, _ = pair
    assert expose(a, "web") == expose(a, "web")


def test_the_arguments_are_the_name_and_where_and_nothing_else_is_taken(pair):
    a, _ = pair
    assert refused(a, "web", where="")
    assert refused(a, "web", where="everywhere")
    assert refused(a, "web", where="LOCAL")
    result = a.tool("expose", port=8080)
    assert result.get("isError"), "a port number is not a name"


def test_a_name_that_is_not_declared_is_refused_and_the_answer_lists_the_names(pair):
    a, _ = pair
    message = refused(a, "admin")
    for name in PORTS:
        assert name in message
    assert refused(a, "8080")


def test_hub_is_refused_when_no_hub_serves_the_project(pair):
    a, _ = pair
    assert "no hub" in refused(a, "web", where="hub")


def test_unexpose_closes_it_and_another_port_is_not_touched(ported, pair):
    a, _ = pair
    expose(a, "web")
    expose(a, "db")
    assert not a.tool("unexpose", name="web").get("isError")
    assert [e["name"] for e in exposed(ported, a.name)] == ["db"]
    assert not a.tool("unexpose", name="web").get("isError"), "it is closed, which is what was asked"


def test_unexpose_answers_ok_and_the_note_about_active_connections_over_mcp_and_http(pair):
    a, _ = pair
    expose(a, "docs")
    assert answer(a.tool("unexpose", name="docs")) == {"ok": True, "note": NOTE}
    expose(a, "docs")
    code, body = a.call("DELETE", "/v1/expose/docs")
    assert code == 200 and body == {"ok": True, "note": NOTE}


def test_exposing_after_unexpose_is_the_same_address(pair):
    a, _ = pair
    first = expose(a, "db")["address"]
    a.tool("unexpose", name="db")
    assert expose(a, "db")["address"] == first, "the outside port is the instance's for as long as it lives"


def test_an_agent_exposes_for_itself_only(ported, pair):
    a, b = pair
    first = expose(a, "db")
    assert exposed(ported, b.name) == []
    assert not b.tool("unexpose", name="db").get("isError")
    assert [e["name"] for e in exposed(ported, a.name)] == ["db"]
    assert expose(b, "db")["address"] != first["address"], "each instance has its own outside port"


def test_the_http_verbs_are_the_same_verbs(ported, pair):
    a, _ = pair
    code, body = a.call("POST", "/v1/expose", {"name": "web", "where": "local"})
    assert code == 200 and HTTP_ADDRESS.fullmatch(body["address"]) and body["protocol"] == "http"
    code, listed = a.call("GET", "/v1/expose")
    assert code == 200 and [(e["name"], e["where"]) for e in listed] == [("web", "local")]
    assert [e["name"] for e in exposed(ported, a.name)] == ["web"]
    assert a.call("DELETE", "/v1/expose/web")[0] == 200
    assert exposed(ported, a.name) == []
    assert a.call("GET", "/v1/expose")[1] == []


def test_the_exposures_are_in_the_status_log_within_a_moment(ported, engine, pair):
    a, _ = pair
    got = expose(a, "db")
    assert eventually(lambda: latest_exposed(engine, ported, a.name), seconds=5)
    line = latest_exposed(engine, ported, a.name)[0]
    assert (line["name"], line["where"], line["protocol"], line["port"]) == ("db", "local", "tcp", 5432)
    assert line["address"] == got["address"]
    a.tool("unexpose", name="db")
    assert eventually(lambda: not latest_exposed(engine, ported, a.name), seconds=5)


def test_ps_shows_the_names_and_where_and_json_has_the_address(ported, pair):
    a, _ = pair
    expose(a, "web")
    got = expose(a, "db")
    assert ported.ps()[a.name]["PORTS"] == "db:local,web:local"
    row = {e["name"]: e for e in exposed(ported, a.name)}
    assert row["db"]["address"] == got["address"] and row["db"]["where"] == "local" and row["db"]["port"] == 5432


def test_a_stopped_agent_shows_no_open_port(ported, fresh):
    a = fresh()
    expose(a, "db")
    ported.ok("agent stop", a.name)
    assert ported.ps()[a.name]["PORTS"] == ""
    assert exposed(ported, a.name) == []


def test_an_agent_ls_shows_them_too(ported, pair):
    a, _ = pair
    expose(a, "docs")
    rows = json.loads(ported.ok("agent ls --json").stdout)
    assert next(r for r in rows if r["name"] == a.name)["exposed"][0]["name"] == "docs"


def test_a_restart_of_the_sidecar_ends_nothing(ported, engine, fresh):
    a = fresh()
    first = expose(a, "db")
    assert engine.run("restart", f"{ported.name}-control-1").returncode == 0
    assert eventually(lambda: [e["address"] for e in exposed(ported, a.name)] == [first["address"]], seconds=30)


def test_a_new_instance_of_a_name_inherits_nothing(ported, fresh):
    name = "again"
    a = fresh(name)
    expose(a, "db")
    ported.ok("agent rm --force", name)
    b = fresh(name)
    assert exposed(ported, name) == []
    assert refused(b, "nothing-here"), "its own declared ports are what it can open"
    assert expose(b, "db")["where"] == "local"


def test_the_tools_say_how_and_say_nothing_about_this_agent(two):
    tools = two[0].mcp("tools/list")["result"]["tools"]
    described = {tool["name"]: tool["description"] for tool in tools}
    assert "expose" in described and "unexpose" in described
    for text in described.values():
        assert not any(name in text.split() for name in ("web", "db", "voice")), "the ports of an agent are in its instructions, not in the tools"
