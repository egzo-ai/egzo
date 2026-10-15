# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""The egress proxy as the only way out: what an agent is given to reach it, who can use it, which addresses it never connects to, and
how it answers Encrypted Client Hello. None of it needs the internet (the hosts that matter are private addresses and a name that never
resolves). One project is up for the whole module; the profiles are in proxykit.py and a spec spawns the one it is about."""

from pathlib import Path

import pytest

from conftest import LABEL_PREFIX
from proxykit import ECH_HOST, Probe, document
from support import custom, spec

FIXTURES = Path(__file__).parent.parent / "fixtures"


@pytest.fixture(scope="module")
def sandbox(shared_project, engine):
    shared_project.env.update({"DEPLOY_TOKEN": "x", "SITE_SECRET": "y"})
    shared_project.up(document(engine.agent_image(), "open", "none", "ech"))
    return shared_project


@pytest.fixture(scope="module")
def probe(sandbox, engine):
    return Probe(engine, sandbox)


@pytest.fixture(autouse=True)
def start_from_no_instance(sandbox):
    sandbox.retire()
    yield


def hello(probe, name, host, *flags):
    """What a bare TLS client sees after sending one ClientHello through the proxy (fixtures/tls_client.py)."""
    container = probe.container(name).name
    copied = probe.engine.run("cp", str(FIXTURES / "tls_client.py"), f"{container}:/tmp/tls_client.py")
    assert copied.returncode == 0, copied.stderr
    return probe.engine.exec(container, "python3", "/tmp/tls_client.py", host, *flags).stdout.strip()


def test_agents_are_pointed_at_the_proxy_trust_its_ca_and_cannot_use_it_without_their_own_credentials(sandbox, engine, probe):
    p = sandbox
    name = p.spawn("open")
    proxy = probe.container("proxy")
    assert proxy.labels[f"{LABEL_PREFIX}kind"] == "proxy"
    assert f"{p.name}_egress" in {r.name for r in engine.resources(p.name) if r.kind == "network"}

    env = probe.env(name)
    assert env["HTTPS_PROXY"].startswith(f"http://{name}:") and env["HTTPS_PROXY"].endswith("@proxy:3128")
    assert env["SSL_CERT_FILE"] == "/etc/egzo/ca/ca-bundle.crt" and env["NODE_EXTRA_CA_CERTS"] == "/etc/egzo/ca/ca.crt"
    assert "control" in env["NO_PROXY"]

    container = probe.container(name).name
    assert sorted(engine.exec(container, "ls", "/etc/egzo/ca").stdout.split()) == ["ca-bundle.crt", "ca.crt"]
    assert "PRIVATE KEY" not in engine.exec(container, "cat", "/etc/egzo/ca/ca.crt", "/etc/egzo/ca/ca-bundle.crt").stdout
    assert engine.exec(container, "touch", "/etc/egzo/ca/planted").returncode != 0
    private = {m["Destination"]: m["Name"] for m in proxy.raw["Mounts"]}["/ca-private"]
    for service in ("control", name):
        assert private not in {m.get("Name") for m in probe.container(service).raw["Mounts"]}, f"the CA key is only in the proxy volume, not in {service}"

    assert probe.status(name, "example.com", "--noproxy", "*", "-m", "6").returncode != 0, "an agent cannot go around the proxy"

    refused = probe.curl(name, "--proxy", "http://proxy:3128", "-o", "/dev/null", "https://example.com/")
    assert refused.returncode != 0 and "407" in refused.stderr
    # asking for credentials is how a browser starts (it sends them only once challenged), so it is not a denial; wrong ones are
    assert [e["action"] for e in probe.audit(host="example.com")] == ["challenge"]
    wrong = probe.curl(name, "--proxy", "http://" + name + ":wrong@proxy:3128", "-o", "/dev/null", "https://example.com/")
    assert "407" in wrong.stderr
    assert [e["agent"] for e in probe.audit(host="example.com") if e["action"] == "deny"] == [name]

    huge = "h" * 4000  # the audit log cannot be filled by one long value
    probe.curl(name, "-o", "/dev/null", "--proxy", f"http://{huge}:x@proxy:3128", "https://example.com/")
    assert all(len(line) < 2000 for line in probe.proxy_log().splitlines())


def test_the_proxy_never_connects_to_a_private_address_or_another_agent_even_when_everything_is_allowed(sandbox, probe):
    name, other = sandbox.spawn("open"), sandbox.spawn("open")
    for target in ("10.0.0.1", "169.254.169.254", "192.168.1.1", "172.17.0.1"):
        result = probe.status(name, target)
        assert result.returncode != 0 and "403" in result.stderr, target
    assert "not a public address" in probe.proxy_log()

    address = probe.container(other).raw["NetworkSettings"]["Networks"][f"{sandbox.name}_{other}"]["IPAddress"]
    through = probe.status(name, address)
    assert through.returncode != 0 and "403" in through.stderr, "an agent cannot reach another agent through the proxy"


def test_an_encrypted_client_hello_is_terminated_and_never_tunneled_nor_widens_what_a_profile_allows(sandbox, probe):
    """A client that sends the ECH extension (every Chromium does, as GREASE, to every host) hides nothing the proxy needs when the name it
    sends in the clear is the approved host, and it could hide anything when it is not. The proxy cannot tell the two apart, so it does not
    forward such a hello: it terminates TLS with the project CA. The browser specs do this with a real Chromium; a bare client here sends
    nothing but the hello, so the rules show without the internet."""
    name = sandbox.spawn("ech")
    # without the extension the proxy tunnels, and there is nothing to tunnel to
    assert hello(probe, name, ECH_HOST) == "closed"
    assert [e for e in probe.audit(agent=name, host=ECH_HOST) if e["action"] == "error"]
    # with it, the proxy is the TLS server (a client that sends its first bytes right behind CONNECT included)
    assert hello(probe, name, ECH_HOST, "--ech") == "server-hello"
    assert hello(probe, name, ECH_HOST, "--ech", "--pipelined") == "server-hello"
    events = probe.audit(agent=name, host=ECH_HOST)
    assert len([e for e in events if e["action"] == "intercept"]) == 2
    assert "Encrypted Client Hello" in next(e for e in events if e["action"] == "intercept")["reason"]
    assert [e for e in events if e["action"] == "close" and e.get("bytes_up", 0) > 0 and e.get("bytes_down", 0) > 0], "logged like a tunnel"
    assert not [e for e in events if e["action"] in ("deny", "request")]

    # an allowed name cannot front for another one, with or without encryption
    for flags in (["--sni", "other.egzo-spec.invalid"], ["--no-sni"], ["--ech", "--sni", "other.egzo-spec.invalid"], ["--ech", "--no-sni"]):
        assert hello(probe, name, ECH_HOST, *flags) == "closed", flags
    denied = [e for e in probe.audit(agent=name, host=ECH_HOST) if e["action"] == "deny"]
    assert len(denied) == 4 and all("server name" in e["reason"] for e in denied)

    assert hello(probe, name, "other.egzo-spec.invalid", "--ech") == "refused 403"
    assert [e for e in probe.audit(agent=name, host="other.egzo-spec.invalid") if e["action"] == "deny"]
    assert len([e for e in probe.audit(agent=name) if e["action"] == "intercept"]) == 2, "no more interceptions than the approved host's two"


def test_a_proxy_that_has_no_policy_tells_the_agent_what_to_do_until_egzo_loads_it_again(sandbox, probe):
    name = sandbox.spawn("open")
    probe.engine.run("restart", probe.container("proxy").name)  # not through egzo: nobody reloads the policy
    import time

    time.sleep(3)
    refused = probe.curl(name, "-o", "/dev/null", "https://example.com/")
    assert refused.returncode != 0 and ("503" in refused.stderr or "egzo project up" in refused.stderr)
    assert "407" not in refused.stderr, "a missing policy is not an authentication problem"
    sandbox.ok("restart proxy")  # through egzo it is loaded again
    assert "503" not in probe.curl(name, "--proxy", "http://proxy:3128", "-o", "/dev/null", "https://example.com/").stderr


def test_a_project_without_agent_templates_has_no_proxy(live_project, engine):
    live_project.up(spec())
    assert not [r for r in engine.containers(live_project.name) if r.labels.get(f"{LABEL_PREFIX}kind") == "proxy"]
