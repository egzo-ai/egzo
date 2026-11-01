# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""Local ports: a port an agent declared is reached from the machine, through a relay, once the agent exposes it with where=local.

The relay is a container of its own for each instance that declares ports, on a network the project's relays share and on the agent's. The
agent's network stays internal. Each spec follows the address `expose` answered, from this machine, to a server the agent runs on 0.0.0.0.

The module shares one project and two instances of a template with ports; the machine has a limited number of networks."""

import json
import socket
import time
import urllib.request

import pytest

from agentapi import Agent
from hubkit import eventually
from support import agent, spec

PORTS = {"site": "8080/http", "db": "5432/tcp", "voice": "5432/udp"}
NAMES = ("alpha", "beta")


def template(image, **fields):
    return agent(harness="custom", image=image, env={"FAKE_TUI": "cat"}, **fields)


@pytest.fixture(scope="module")
def project(shared_project, engine, egzo):
    image = engine.forward_image(egzo.binary)
    shared_project.up(spec(local_ports={"bind": "127.0.0.1", "max_exposed": 3}, agents={"web": template(image, ports=PORTS), "plain": template(image)}))
    return shared_project


@pytest.fixture(scope="module")
def alpha(project, engine):
    return serve(engine, Agent(engine, project, project.spawn("web", "alpha")), "alpha")


@pytest.fixture(scope="module")
def beta(project, engine):
    return serve(engine, Agent(engine, project, project.spawn("web", "beta")), "beta")


def serve(engine, worker, name):
    """Start the agent's servers, on 0.0.0.0 as its instructions tell it, and wait until they answer inside."""
    assert engine.exec(worker.container.name, "local-server", name, "8080", "5432", "5432", detach=True).returncode == 0
    assert eventually(lambda: engine.exec(worker.container.name, "nc", "-z", "127.0.0.1", "5432").returncode == 0, seconds=30)
    if worker not in SERVING:
        SERVING.append(worker)
    return worker


SERVING = []


def internal(network):
    """Whether a network is internal, in Docker's format or in Podman's own."""
    return network.raw.get("Internal", network.raw.get("internal", False))


@pytest.fixture(autouse=True)
def nothing_open():
    """Whatever a spec opened is closed after it, for the agents the module has made so far."""
    yield
    for worker in SERVING:
        if worker.container is None:
            continue  # the project was taken down
        code, open_now = worker.call("GET", "/v1/expose")
        for exposure in open_now if code == 200 else []:
            worker.tool("unexpose", name=exposure["name"])


def expose(worker, name):
    result = worker.tool("expose", name=name, where="local")
    assert "structuredContent" in result, result
    return result["structuredContent"]["address"]


def hostport(address):
    host, _, port = address.removeprefix("http://").rstrip("/").rpartition(":")
    return host, int(port)


def talk_tcp(address, line=b"hello\n"):
    """One line to a TCP port and what comes back; b"" when the connection is closed on us."""
    try:
        with socket.create_connection(hostport(address), timeout=3) as connection:
            connection.settimeout(3)
            connection.sendall(line)
            return connection.recv(1024)
    except OSError:
        return b""


def talk_udp(address, data=b"ping"):
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sock:
        sock.settimeout(2)
        sock.sendto(data, hostport(address))
        try:
            return sock.recv(1024)
        except OSError:
            return b""


def get_http(address):
    try:
        with urllib.request.urlopen(address, timeout=3) as response:
            return response.read()
    except OSError:
        return b""


def relay_of(engine, project, name):
    return next((c for c in engine.containers(project.name) if c.labels.get("ai.egzo.kind") == "relay" and c.labels.get("ai.egzo.instance") == name), None)


# --- what exists ----------------------------------------------------------------------------------------------------


def test_a_project_without_local_ports_has_no_relay_and_no_network_for_them(live_project, engine, egzo):
    image = engine.forward_image(egzo.binary)
    live_project.up(spec(agents={"web": template(image)}))
    live_project.spawn("web", "solo")
    assert not [c for c in engine.containers(live_project.name) if c.labels.get("ai.egzo.kind") == "relay"]
    assert not [r for r in engine.resources(live_project.name) if r.name.endswith("_local_ports") or r.name.endswith("-relay-1")]


def control_env(engine, project):
    return dict(e.split("=", 1) for e in json.loads(engine.run("inspect", f"{project.name}-control-1").stdout)[0]["Config"]["Env"])


def test_the_default_bound_is_ten(live_project, engine, egzo):
    # before the module's project exists: a machine has a limited number of networks
    image = engine.forward_image(egzo.binary)
    live_project.write(spec(local_ports={"bind": "127.0.0.1"}, agents={"web": template(image, ports={"db": "5432/tcp"})}))
    resolved = live_project.ok("project check --resolved").yaml()
    assert resolved["local_ports"]["max_exposed"] == 10
    live_project.up()
    assert control_env(engine, live_project)["EGZO_LOCAL_MAX_EXPOSED"] == "10"


def test_a_project_with_local_ports_has_one_network_for_them_and_a_relay_for_each_instance_with_ports_only(project, engine, alpha, beta):
    plain = project.spawn("plain", "plain-one")
    try:
        networks = [r for r in engine.resources(project.name) if r.kind == "network" and r.name.endswith("_local_ports")]
        assert len(networks) == 1 and not internal(networks[0])
        assert relay_of(engine, project, "alpha") and relay_of(engine, project, "beta")
        assert relay_of(engine, project, plain) is None, "a template with no ports has no relay"
        rows = {row["name"]: row for row in json.loads(project.ok("ps --json").stdout)}
        assert rows[f"{project.name}-alpha-relay-1"]["kind"] == "relay"
    finally:
        project.ok("agent rm --force", plain)


def test_the_relay_is_hardened_and_holds_nothing_of_the_project(project, engine, alpha):
    relay = relay_of(engine, project, "alpha")
    host = relay.raw["HostConfig"]
    assert host["ReadonlyRootfs"] and "no-new-privileges" in host["SecurityOpt"]
    assert host["Memory"] == 64 << 20 and host["PidsLimit"] == 64 and host["RestartPolicy"]["Name"] == "unless-stopped"
    assert relay.raw["Config"]["User"].split(":")[0] == "65532"
    assert not relay.raw["Mounts"], "no volume and no engine socket"
    env = relay.raw["Config"]["Env"]
    assert not [e for e in env if "SECRET" in e or "KEY" in e], env
    assert sorted(relay.raw["NetworkSettings"]["Networks"]) == sorted([f"{project.name}_local_ports", f"{project.name}_alpha"])
    published = host["PortBindings"]
    assert set(published) == {"8080/tcp", "5432/tcp", "5432/udp"}
    assert all(b[0]["HostIp"] == "127.0.0.1" for b in published.values())


def test_the_agent_stays_on_its_internal_network_alone(project, engine, alpha):
    container = alpha.container
    assert list(container.raw["NetworkSettings"]["Networks"]) == [f"{project.name}_alpha"]
    network = next(r for r in engine.resources(project.name) if r.name == f"{project.name}_alpha")
    assert internal(network)


# --- closed until expose, then open ------------------------------------------------------------------------------------


def test_a_port_refuses_until_expose_and_works_after_for_http_tcp_and_udp(alpha):
    site, db, voice = (expose(alpha, n) for n in ("site", "db", "voice"))
    alpha.tool("unexpose", name="site")
    alpha.tool("unexpose", name="db")
    alpha.tool("unexpose", name="voice")
    assert get_http(site) == b"" and talk_tcp(db) == b"" and talk_udp(voice) == b"", "closed until the agent exposes it"
    assert expose(alpha, "site") == site and expose(alpha, "db") == db and expose(alpha, "voice") == voice, "the address does not change"
    assert get_http(site) == b"http:alpha"
    assert talk_tcp(db, b"hello\n") == b"tcp:alpha:hello\n"
    assert talk_udp(voice, b"ping") == b"udp:alpha:ping"


def test_unexpose_stops_new_connections_and_leaves_an_open_one(alpha):
    address = expose(alpha, "db")
    with socket.create_connection(hostport(address), timeout=3) as held:
        held.settimeout(3)
        held.sendall(b"one\n")
        assert held.recv(1024) == b"tcp:alpha:one\n"
        answer = alpha.tool("unexpose", name="db")["structuredContent"]
        assert answer == {"ok": True, "note": "Active connections may remain active - close your server to end them."}
        assert talk_tcp(address) == b"", "a new connection is refused"
        held.sendall(b"two\n")
        assert held.recv(1024) == b"tcp:alpha:two\n", "the one that was open goes on"


def test_two_instances_expose_the_same_name_and_get_different_addresses_each_reaching_its_own_server(alpha, beta):
    one, two = expose(alpha, "db"), expose(beta, "db")
    assert one != two
    assert talk_tcp(one) == b"tcp:alpha:hello\n" and talk_tcp(two) == b"tcp:beta:hello\n"
    alpha.tool("unexpose", name="db")
    beta.tool("unexpose", name="db")
    site_one, site_two = expose(alpha, "site"), expose(beta, "site")
    assert get_http(site_one) == b"http:alpha" and get_http(site_two) == b"http:beta"


def test_the_address_is_the_same_after_the_relay_restarts(project, engine, alpha):
    address = expose(alpha, "db")
    assert talk_tcp(address) == b"tcp:alpha:hello\n"
    assert engine.run("restart", relay_of(engine, project, "alpha").name).returncode == 0
    assert eventually(lambda: talk_tcp(address) == b"tcp:alpha:hello\n", seconds=30), "the same published port, and what control kept"
    assert expose(alpha, "db") == address


def test_the_address_is_the_same_after_stop_and_start_and_the_relay_stays_up(project, engine, beta):
    first = expose(beta, "db")
    relay = relay_of(engine, project, "beta")
    started = relay.raw["State"]["StartedAt"]
    project.ok("agent stop", "beta")
    assert relay_of(engine, project, "beta").raw["State"]["Running"], "the relay lives as long as the instance"
    assert talk_tcp(first) == b"", "a connection while the agent is down just fails"
    project.ok("agent start", "beta")
    assert relay_of(engine, project, "beta").raw["State"]["StartedAt"] == started, "stop and start do not touch the relay"
    serve(engine, beta, "beta")
    assert expose(beta, "db") == first, "a boot closed the port; opening it again gives the same address"
    assert talk_tcp(first) == b"tcp:beta:hello\n"


# --- no way in for the agent ---------------------------------------------------------------------------------------------


def test_the_agent_has_no_route_out_and_reaches_nothing_through_the_relay(project, engine, alpha):
    expose(alpha, "db")
    expose(alpha, "voice")
    relay = relay_of(engine, project, "alpha")
    inside = relay.raw["NetworkSettings"]["Networks"][f"{project.name}_alpha"]["IPAddress"]
    outside = relay.raw["NetworkSettings"]["Networks"][f"{project.name}_local_ports"]["IPAddress"]
    # The relay listens on both its networks, because rootless Podman may deliver to either: on the agent's, the agent connects and is closed at once.
    assert not alpha.exec(f"echo hello | nc -w 2 {inside} 5432").stdout.strip(), "the agent's bytes went through the relay"
    assert not alpha.exec(f"echo ping | nc -u -w 2 {inside} 5432").stdout.strip(), "the agent's datagram went through the relay"
    assert alpha.exec(f"nc -z -w 2 {outside} 5432").returncode != 0, "the agent has no route to the network for local ports"
    assert alpha.exec("nc -z -w 2 1.1.1.1 53").returncode != 0, "an agent has no route out"
    assert alpha.exec("ip route | grep -c default").stdout.strip() in ("0", ""), "no default route"


def test_nothing_the_agent_sends_to_a_udp_port_starts_a_session(alpha):
    voice = expose(alpha, "voice")
    # the server answers only a datagram it was sent: it has no way to speak first to a client the relay does not know
    assert talk_udp(voice, b"ping") == b"udp:alpha:ping"
    alpha.tool("unexpose", name="voice")
    assert talk_udp(voice, b"ping") == b""


# --- the bound on open local ports -------------------------------------------------------------------------------------


def test_the_bound_comes_from_the_file_and_one_more_than_it_is_refused(alpha, beta):
    opened = {expose(alpha, name) for name in PORTS}
    assert len(opened) == 3
    refused = beta.tool("expose", name="db", where="local")
    assert refused.get("isError") and "unexpose one first" in refused["content"][0]["text"], "three are open in the project, which is the most"
    alpha.tool("unexpose", name="db")
    assert expose(beta, "db"), "room again"


def test_control_is_told_the_bound_when_it_is_made(project, engine):
    assert control_env(engine, project)["EGZO_LOCAL_MAX_EXPOSED"] == "3"


# --- the end of an instance and of the project -------------------------------------------------------------------------


def test_agent_rm_removes_the_relay(project, engine, alpha):
    spare = project.spawn("web", "spare")
    assert relay_of(engine, project, spare)
    project.ok("agent rm --force", spare)
    assert relay_of(engine, project, spare) is None
    assert not [r for r in engine.resources(project.name) if "spare" in r.name], "nothing of the instance is left"


def test_project_down_leaves_no_relay_and_no_network_for_local_ports(project, engine, alpha, beta):
    project.ok("project down --force")
    assert not [r for r in engine.resources(project.name)], "nothing of the project is left"
