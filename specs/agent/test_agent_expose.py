# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""The `expose` and `unexpose` tools: an agent says a port of its own loopback is worth opening, and gets what to hand to its user.

Without a hub that is the `egzo agent port-forward` command. Nothing is checked at `expose` time, so the specs need no server in the
agent. With a hub: test_agent_expose_hub.py."""

import json
import re

import pytest

from hubkit import eventually


@pytest.fixture
def pair(team):
    """The module's two agents with nothing exposed: every agent is a network of its own, and a machine has a limited number of them."""
    yield team.coder, team.reviewer
    for worker in (team.coder, team.reviewer):
        for exposure in exposed(team.project, worker.name):
            worker.tool("unexpose", port=exposure["port"])


@pytest.fixture
def fresh(team):
    """Spawn an agent and take it away again: for a spec that spends a budget, or removes what it made."""
    spawned = []

    def spawn(name=None):
        spawned.append(team.spawn(name))
        return spawned[-1]

    yield spawn
    for worker in spawned:
        team.project.run("agent", "rm", "--force", worker.name)


def answer(result):
    """What a tool answered, or what it refused."""
    assert "structuredContent" in result or result.get("isError"), result
    return result.get("structuredContent", result)


def expose(worker, port, **arguments):
    return answer(worker.tool("expose", port=port, **arguments))


def exposed(project, name):
    return next(r for r in json.loads(project.ok("ps --json").stdout) if r["name"] == name).get("exposed") or []


def status_lines(engine, project):
    logs = engine.run("logs", f"{project.name}-control-1").stdout
    return [line for line in map(json.loads, filter(None, logs.splitlines())) if line.get("type") == "status"]


def latest_exposed(engine, project, name):
    agents = status_lines(engine, project)[-1]["agents"]
    return next(a for a in agents if a["agent"] == name).get("exposed") or []


def test_without_a_hub_the_answer_is_the_command_a_person_runs(pair):
    a, _ = pair
    got = expose(a, 3000)
    assert got["command"] == f"egzo agent port-forward {a.name} 3000"
    assert not got.get("address")
    assert got["protocol"] == "http"


def test_exposing_again_changes_the_name_and_the_protocol_and_keeps_the_id(team, pair):
    a, _ = pair
    first = expose(a, 3001, name="shop")
    again = expose(a, 3001, name="store", protocol="tcp")
    assert again["id"] == first["id"]
    row = exposed(team.project, a.name)
    assert [(e["port"], e["name"], e["protocol"], e["id"]) for e in row] == [(3001, "store", "tcp", first["id"])]


def test_each_port_has_an_id_of_its_own_and_it_tells_nothing(pair):
    a, _ = pair
    one, two = expose(a, 3002)["id"], expose(a, 3003)["id"]
    assert one != two
    assert re.fullmatch(r"[a-z2-7]{16}", one) and re.fullmatch(r"[a-z2-7]{16}", two)


@pytest.mark.parametrize("arguments", [{"port": 0}, {"port": 65536}, {"port": 3000, "protocol": "udp"}, {"port": 3000, "protocol": "HTTP"}, {"port": 3000, "name": "a\u001b[2Jb"}, {"port": 3000, "name": "x" * 65}])
def test_what_is_not_an_exposure_is_refused(pair, arguments):
    a, _ = pair
    assert a.tool("expose", **arguments).get("isError"), arguments


def test_an_agent_has_a_limit_of_exposures(fresh):
    a = fresh()
    answers = [a.tool("expose", port=port) for port in range(4000, 4033)]
    assert not any(r.get("isError") for r in answers[:32])
    assert answers[32].get("isError")


def test_unexpose_ends_it_and_another_port_is_not_touched(team, pair):
    a, _ = pair
    expose(a, 3004)
    expose(a, 3005)
    assert not a.tool("unexpose", port=3004).get("isError")
    assert [e["port"] for e in exposed(team.project, a.name)] == [3005]
    assert not a.tool("unexpose", port=3004).get("isError"), "it is gone, which is what was asked"


def test_exposing_after_unexpose_is_a_new_id(pair):
    a, _ = pair
    first = expose(a, 3006)["id"]
    a.tool("unexpose", port=3006)
    assert expose(a, 3006)["id"] != first


def test_an_agent_exposes_for_itself_only(team, pair):
    a, b = pair
    expose(a, 3007)
    assert exposed(team.project, b.name) == []
    assert not b.tool("unexpose", port=3007).get("isError")
    assert [e["port"] for e in exposed(team.project, a.name)] == [3007]


def test_the_http_verbs_are_the_same_verbs(team, pair):
    a, _ = pair
    code, body = a.call("POST", "/v1/expose", {"port": 3008, "name": "shop"})
    assert code == 200 and body["id"] and body["protocol"] == "http"
    assert [e["port"] for e in exposed(team.project, a.name)] == [3008]
    assert a.call("DELETE", "/v1/expose/3008")[0] == 204
    assert exposed(team.project, a.name) == []


def test_the_exposures_are_in_the_status_log_within_a_moment(team, engine, pair):
    a, _ = pair
    got = expose(a, 3009, name="shop")
    assert eventually(lambda: latest_exposed(engine, team.project, a.name), seconds=5)
    assert latest_exposed(engine, team.project, a.name)[0]["id"] == got["id"]
    a.tool("unexpose", port=3009)
    assert eventually(lambda: not latest_exposed(engine, team.project, a.name), seconds=5)


def test_ps_shows_the_exposed_ports(team, pair):
    a, _ = pair
    expose(a, 3010)
    expose(a, 3011, protocol="tcp")
    assert team.project.ps()[a.name]["PORTS"] == "3010/http,3011/tcp"


def test_an_agent_ls_shows_them_too(team, pair):
    a, _ = pair
    expose(a, 3012, name="shop")
    rows = json.loads(team.project.ok("agent ls --json").stdout)
    assert next(r for r in rows if r["name"] == a.name)["exposed"][0]["port"] == 3012


def test_the_id_survives_a_restart_of_the_agent_and_of_the_sidecar(team, engine, fresh):
    a = fresh()
    first = expose(a, 3013)["id"]
    team.project.ok("agent stop", a.name)
    team.project.ok("agent start", a.name)
    assert expose(a, 3013)["id"] == first
    assert engine.run("restart", f"{team.project.name}-control-1").returncode == 0
    assert eventually(lambda: exposed(team.project, a.name), seconds=30)
    assert expose(a, 3013)["id"] == first


def test_a_new_instance_of_a_name_inherits_nothing(team, fresh):
    name = "again"
    a = team.spawn(name)
    first = expose(a, 3014)["id"]
    team.project.ok("agent rm --force", name)
    b = fresh(name)
    assert exposed(team.project, name) == []
    assert expose(b, 3014)["id"] != first


def test_the_instructions_of_an_agent_say_how(team):
    tools = team.coder.mcp("tools/list")["result"]["tools"]
    described = {tool["name"]: tool["description"] for tool in tools}
    assert "HTTP" in described["expose"] and "unexpose" in described
