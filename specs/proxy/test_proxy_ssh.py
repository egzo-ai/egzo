# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""ssh from an agent: `ssh <server>` reaches a server named in the agent's egress profile through the proxy, which holds the key and
the pinned host key; the agent holds neither, only a server of its own profile can be reached, forwarding is refused, and the
proxy records what ran. The server is a real sshd in a container (`ssh_server`), so none of it needs the internet. One project is
up for the whole module; a spec spawns the agent of the profile it is about."""

import json

import pytest

from conftest import LABEL_PREFIX
from support import agent, spec

HOME = "/tmp/agent-home"
TOKEN_FREE = "ssh-agent"


def profiles(server):
    def entry(**fields):
        return {"host": server.host, "port": server.port, "user": server.user, "key": "main/PROD_SSH_KEY", "host_key": server.fingerprint, **fields}

    other, _, other_fingerprint = server.keygen()
    return {
        "ops": {"ssh": {"prod": entry()}},
        "pubkey": {"ssh": {"prod": entry(host_key=server.host_key)}},
        "wrongkey": {"ssh": {"prod": entry(host_key=other_fingerprint)}},
        "metadata": {"ssh": {"prod": entry(host="169.254.169.254", port=22)}},
        "refused": {"ssh": {"prod": entry(key="main/OTHER_SSH_KEY")}},
        "dead": {"ssh": {"prod": entry(port=1)}},
        "none": {},
    }


@pytest.fixture(scope="module")
def sandbox(shared_project, engine, egzo, ssh_server):
    shared_project.env["PROD_SSH_KEY"] = ssh_server.private_key
    shared_project.env["OTHER_SSH_KEY"] = ssh_server.keygen()[0]  # a key the server does not know
    image = engine.ssh_agent_image(egzo.binary)
    egress = profiles(ssh_server)
    shared_project.up(
        spec(
            vaults={"main": {"backend": "env", "secrets": ["PROD_SSH_KEY", "OTHER_SSH_KEY"]}},
            egress=egress,
            agents={name: agent(harness="custom", image=image, egress=name, env={"FAKE_TUI": "cat", "HOME": HOME}) for name in egress},
        )
    )
    return shared_project


@pytest.fixture(autouse=True)
def start_from_no_instance(sandbox):
    sandbox.retire()
    yield


class Ssh:
    """The ssh an agent runs, and what the server and the proxy saw of it."""

    def __init__(self, sandbox, engine, server, template):
        self.sandbox, self.engine, self.server = sandbox, engine, server
        # an address can come back with the next instance: what is told apart by address is what was logged after this one began
        self.since = len(self.events())
        self.name = sandbox.spawn(template)
        self.container = f"{sandbox.name}-{self.name}"
        # `egzo agent run` writes the identity as it starts.
        ready = self.inside(f"for i in $(seq 100); do [ -s {HOME}/.ssh/egzo_ed25519 ] && exit 0; sleep 0.1; done; exit 1")
        assert ready.returncode == 0, "the agent has no ssh identity"

    def inside(self, script):
        return self.engine.exec(self.container, "sh", "-c", script)

    def ssh(self, *arguments, stdin=None):
        quoted = " ".join(json.dumps(a) for a in arguments)
        return self.inside(f"{'echo ' + json.dumps(stdin) + ' | ' if stdin is not None else ''}ssh -o BatchMode=yes {quoted}")

    def logins(self):
        """How many times the server accepted the key of the profile since the run began."""
        logs = self.engine.run("logs", self.server.name)  # sshd logs to standard error
        return (logs.stdout + logs.stderr).count("Accepted publickey for ops")

    def events(self):
        return [json.loads(line) for line in self.sandbox.ok("proxy log").stdout.splitlines() if line.startswith("{")]

    def audit(self, event=None):
        events = self.events()
        return [e for e in events if e.get("agent") == self.name and e["protocol"] == "ssh" and (event is None or e["event"] == event)]

    def addresses(self):
        raw = json.loads(self.engine.run("inspect", self.container).stdout)[0]
        return {n["IPAddress"] for n in raw["NetworkSettings"]["Networks"].values()}

    def unattributed(self):
        """The ssh events from this agent's container that no identity was proved for."""
        return [e for e in self.events()[self.since:] if e["protocol"] == "ssh" and "agent" not in e and e.get("client") in self.addresses()]

    def failed(self, code):
        """The texts of the upstream failures of the code."""
        return [e["reason"]["text"] for e in self.audit("upstream") if e["decision"] == "error" and e["reason"]["code"] == code]


@pytest.fixture
def ssh_of(sandbox, engine, ssh_server):
    return lambda template="ops": Ssh(sandbox, engine, ssh_server, template)


def test_an_agent_runs_commands_on_the_server_by_its_name_and_the_exit_status_stdin_and_a_terminal_work(ssh_of):
    a = ssh_of()
    ran = a.ssh("prod", "id -un")
    assert ran.returncode == 0, ran.stderr
    assert ran.stdout.strip() == "ops"  # the user of the profile, on the real server

    assert a.ssh("prod", "exit 7").returncode == 7
    assert "oops" in a.ssh("prod", "echo oops >&2").stderr
    assert a.ssh("prod", "cat", stdin="through the proxy").stdout.strip() == "through the proxy"
    assert a.ssh("prod", "head -c 3000000 /dev/zero | wc -c").stdout.strip() == "3000000"
    assert "/dev/pts/" in a.ssh("-tt", "prod", "tty").stdout  # a terminal, as an agent's interactive shell needs


def test_the_agent_never_holds_the_key_and_nothing_records_it(ssh_of, engine, sandbox, ssh_server):
    a = ssh_of()
    assert a.ssh("prod", "true").returncode == 0
    secret_lines = [line for line in ssh_server.private_key.splitlines() if line and "PRIVATE KEY" not in line]
    # nothing the agent can read holds the key: its environment, its ~/.ssh, the files published to it
    held = a.inside(f"env; cat {HOME}/.ssh/* /etc/egzo/ca/ssh_config /etc/egzo/ca/ssh_known_hosts").stdout
    assert all(line not in held for line in secret_lines)
    # and nothing the engine records or the proxy logs
    for resource in engine.resources(sandbox.name):
        assert all(line not in json.dumps(resource.raw) for line in secret_lines), f"{resource.kind} {resource.name}"
    assert all(line not in sandbox.ok("proxy log").stdout for line in secret_lines)


def test_the_proxy_records_the_sessions_and_the_commands_the_agent_ran(ssh_of):
    a = ssh_of()
    before = a.logins()
    a.ssh("prod", "echo audited-command")
    assert a.logins() == before + 1, "the server saw the proxy sign in, with the key of the profile"
    events = a.audit()
    opened = [e for e in events if e["event"] == "connect" and e["decision"] == "allow" and e["server"] == "prod"]
    assert opened and opened[-1]["profile"] == "ops" and opened[-1]["instance"]
    # where it came from and what the client said it is; an ssh client sends no name, so nothing is claimed
    assert opened[-1]["client"] in a.addresses() and opened[-1]["user_agent"].startswith("SSH-2.0-OpenSSH")
    assert "claimed_agent" not in opened[-1]
    conn = opened[-1]["conn"]
    assert [e for e in events if e["conn"] == conn and e["event"] == "exec" and e["command"] == "echo audited-command" and e["decision"] == "allow"]
    (closed,) = [e for e in events if e["conn"] == conn and e["event"] == "close"]
    assert closed["bytes_down"] > 0 and "reason" not in closed


def test_a_server_of_another_profile_is_out_of_reach_and_nothing_reaches_the_server(ssh_of):
    a = ssh_of("none")
    before = a.logins()
    refused = a.ssh("prod", "true")
    assert refused.returncode != 0
    assert "Permission denied" in refused.stderr or "denied" in refused.stderr
    assert a.logins() == before
    # refused without a name, but with the server it asked for, where it came from and what the client said it is
    (signin,) = a.unattributed()
    assert signin["event"] == "connect" and signin["decision"] == "deny" and signin["reason"]["code"] == "identity"
    assert signin["server"] == "prod" and signin["user_agent"].startswith("SSH-2.0-OpenSSH") and "instance" not in signin


def test_a_client_that_leaves_before_signing_in_is_an_aborted_connection_not_a_refused_identity(ssh_of):
    a = ssh_of()
    a.inside("bash -c 'exec 3<>/dev/tcp/proxy/2222; sleep 1'")  # connects, sends nothing, leaves
    (left,) = a.unattributed()
    assert left["event"] == "connect" and left["decision"] == "error" and left["reason"]["code"] == "aborted"
    assert "server" not in left and "user_agent" not in left


def test_the_pinned_host_key_is_checked_before_the_key_is_offered(ssh_of):
    a = ssh_of("wrongkey")
    before = a.logins()
    refused = a.ssh("prod", "true")
    assert refused.returncode == 255
    assert "host key" in refused.stderr
    assert a.logins() == before, "the key was offered to a server that is not the pinned one"
    assert any("pinned" in text for text in a.failed("host-key"))


def test_a_server_can_be_pinned_by_its_public_key_as_well(ssh_of):
    assert ssh_of("pubkey").ssh("prod", "id -un").stdout.strip() == "ops"


def test_the_proxy_never_connects_to_a_cloud_metadata_address(ssh_of):
    a = ssh_of("metadata")
    refused = a.ssh("prod", "true")
    assert refused.returncode == 255
    assert any("metadata" in text for text in a.failed("dial"))


def test_a_server_that_refuses_the_key_or_does_not_answer_is_reported_the_way_ssh_would_and_audited(ssh_of):
    b = ssh_of("refused")
    refused = b.ssh("prod", "true")
    assert refused.returncode == 255 and "refused the key" in refused.stderr
    assert "PRIVATE KEY" not in refused.stderr
    assert b.failed("auth")

    a = ssh_of("dead")
    dead = a.ssh("prod", "true")
    assert dead.returncode == 255 and "could not connect" in dead.stderr
    assert any("could not connect" in text for text in a.failed("dial"))


def test_forwarding_and_every_subsystem_but_sftp_are_refused(ssh_of):
    a = ssh_of()
    assert a.ssh("-o", "ExitOnForwardFailure=yes", "-R", "19000:127.0.0.1:80", "prod", "true").returncode != 0
    assert a.ssh("-W", "10.0.0.1:80", "prod").returncode != 0
    assert a.ssh("-s", "prod", "netconf").returncode != 0  # sftp is the one subsystem that is relayed
    denied = [e for e in a.audit() if e.get("decision") == "deny"]
    by_code = {code: " ".join(e["reason"]["text"] for e in denied if e["reason"]["code"] == code) for code in ("forwarding", "channel", "request")}
    assert "tcpip-forward" in by_code["forwarding"] and "direct-tcpip" in by_code["channel"] and "subsystem" in by_code["request"]
    assert {e["event"] for e in denied if e["reason"]["code"] == "channel"} == {"channel"}


def test_files_move_both_ways_with_scp_and_sftp(ssh_of):
    a = ssh_of()
    assert a.inside("echo from-the-agent > /tmp/up.txt").returncode == 0
    assert a.inside("scp -q /tmp/up.txt prod:/tmp/egzo-up.txt").returncode == 0
    assert a.ssh("prod", "cat /tmp/egzo-up.txt").stdout.strip() == "from-the-agent"
    assert a.inside("scp -q prod:/tmp/egzo-up.txt /tmp/down.txt && cat /tmp/down.txt").stdout.strip() == "from-the-agent"
    listed = a.inside("echo 'ls /tmp' | sftp -q -b - prod")
    assert listed.returncode == 0 and "egzo-up.txt" in listed.stdout
    assert any(e["event"] == "sftp" and e["decision"] == "allow" for e in a.audit())


def test_an_agent_without_the_proxys_host_key_cannot_be_led_to_another_server(ssh_of):
    """The proxy's host key is pinned in the file the proxy publishes: a different one is refused."""
    a = ssh_of()
    other, _, _ = a.server.keygen()
    forged = a.inside("echo 'egzo-proxy ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMGFJIY0eOCEYAre+qhPAFyN2YPymAD03FxBn1v6yyZz' > /tmp/known")
    assert forged.returncode == 0
    result = a.ssh("-o", "GlobalKnownHostsFile=/tmp/known", "prod", "true")
    assert result.returncode == 255 and "host key" in result.stderr.lower()


def test_ssh_setup_can_be_run_again_by_hand_in_an_image_that_does_not_start_egzo(ssh_of):
    a = ssh_of()
    again = a.engine.exec(a.container, "egzo", "agent", "ssh-setup")
    assert again.returncode == 0, again.stderr
    assert a.inside(f"grep -c '^Host prod' {HOME}/.ssh/config").stdout.strip() == "1"


def test_up_refuses_a_key_it_cannot_use_and_never_prints_it(live_project, engine, egzo, ssh_server):
    live_project.env["PROD_SSH_KEY"] = "-----BEGIN OPENSSH PRIVATE KEY-----\nnot-really-a-key-material\n"
    egress = {"ops": {"ssh": {"prod": {"host": ssh_server.host, "port": ssh_server.port, "user": "ops", "key": "main/PROD_SSH_KEY", "host_key": ssh_server.fingerprint}}}}
    document = spec(
        vaults={"main": {"backend": "env", "secrets": ["PROD_SSH_KEY"]}}, egress=egress,
        agents={"ops": agent(harness="custom", image=engine.agent_image(), egress="ops")},
    )
    live_project.write(document)
    result = live_project.run("project", "up", timeout=300)
    assert result.returncode != 0
    assert "main/PROD_SSH_KEY" in result.stderr and "not usable as an ssh key" in result.stderr
    assert "not-really-a-key-material" not in result.stdout + result.stderr
