# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""`egzo agent port-forward`: a port of an agent, on the machine where the CLI runs.

Nothing is published and the sandbox does not change: each connection is one exec of `egzo agent forward` in the agent's
container, which connects to the container's own loopback. The server in these specs (fixtures/forward-server.py) binds
127.0.0.1 only, as a dev server does, so reaching it at all proves the route."""

import hashlib
import re
import signal
import socket
import time

import pexpect
import pytest

from support import agent, spec

ECHO, BYE, UPGRADE, DEAF, REFUSE = 7100, 7101, 7102, 7103, 7104
TIMEOUT = 30


@pytest.fixture(scope="module")
def sandbox(shared_project, engine, egzo):
    image = engine.forward_image(egzo.binary)
    shared_project.up(spec(agents={"web": agent(harness="custom", image=image, env={"FAKE_TUI": "cat"})}))
    shared_project.ok("agent spawn web host")
    container = engine.instance(shared_project.name, "host").name
    started = engine.exec(container, "forward-server", str(ECHO), detach=True)
    assert started.returncode == 0, started.stderr
    listening = lambda: engine.exec(container, "nc", "-z", "127.0.0.1", str(ECHO)).returncode == 0
    assert eventually(listening), "the server in the agent did not start"
    return shared_project


@pytest.fixture
def container(sandbox, engine):
    return engine.instance(sandbox.name, "host").name


class Forward:
    """A running `egzo agent port-forward`, on a port the system chose."""

    def __init__(self, project, target, port):
        self.client = project.pty("agent port-forward", target, f"0:{port}")
        self.client.expect(rb"127\.0\.0\.1:(\d+)")
        self.port = int(self.client.match.group(1))

    def connect(self):
        connection = socket.create_connection(("127.0.0.1", self.port), timeout=TIMEOUT)
        connection.settimeout(TIMEOUT)
        return connection

    def stop(self):
        self.client.close(force=True)


@pytest.fixture
def forward(sandbox):
    started = []

    def start(port, target="host"):
        started.append(Forward(sandbox, target, port))
        return started[-1]

    yield start
    for f in started:
        f.stop()


def read_all(connection):
    received = b""
    while data := connection.recv(1 << 16):
        received += data
    return received


def forwards_in(engine, container):
    """The `egzo agent forward` processes running in the container."""
    listing = engine.exec(container, "ps", "-o", "args").stdout
    return [line for line in listing.splitlines() if "agent forward" in line]


def eventually(check, seconds=TIMEOUT):
    deadline = time.time() + seconds
    while time.time() < deadline:
        if check():
            return True
        time.sleep(0.2)
    return check()


def test_a_server_bound_to_loopback_is_reached_with_bytes_both_ways_and_each_connection_stands_alone(forward):
    port = forward(ECHO)
    first, second = port.connect(), port.connect()
    every_byte = bytes(range(256))
    first.sendall(every_byte)
    second.sendall(b"the other one")
    received = b""
    while len(received) < 256 and (data := first.recv(4096)):
        received += data
    assert received == every_byte, "binary, unchanged: no terminal is in the way"
    assert second.recv(4096) == b"the other one"
    first.close()
    second.close()


def test_a_large_body_arrives_whole_and_the_client_ending_its_input_still_gets_the_answer(forward):
    connection = forward(ECHO).connect()
    body = bytes(range(256)) * (1 << 15)  # 8 MiB
    received = []

    import threading

    reader = threading.Thread(target=lambda: received.append(read_all(connection)))
    reader.start()
    connection.sendall(body)
    connection.shutdown(socket.SHUT_WR)  # half closed: the server sees the end and still answers
    reader.join(TIMEOUT * 2)
    assert not reader.is_alive(), "the answer never ended"
    assert received[0][: len(body)] == body
    assert received[0][len(body) :].decode() == f"\n{len(body)} {hashlib.sha256(body).hexdigest()}\n"


def test_a_websocket_upgrade_passes(forward):
    connection = forward(UPGRADE).connect()
    connection.sendall(b"GET /hot HTTP/1.1\r\nHost: localhost\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
    answer = b""
    while b"\r\n\r\n" not in answer and (data := connection.recv(4096)):
        answer += data
    assert answer.startswith(b"HTTP/1.1 101")
    connection.sendall(b"\x82\x05hello")
    assert connection.recv(4096) == b"\x82\x05hello"


def test_the_server_closing_first_ends_the_connection_and_the_exec(forward, engine, container):
    connection = forward(BYE).connect()
    assert read_all(connection) == b"bye\n"
    assert eventually(lambda: not forwards_in(engine, container))


def test_an_answer_that_refuses_an_upload_reaches_a_client_still_sending(forward, engine, container):
    connection = forward(REFUSE).connect()
    try:
        while True:  # until the end of the answer has come back: the client notices it only when it next reads
            connection.sendall(b"x" * (1 << 16))
            connection.settimeout(0.05)
            try:
                first = connection.recv(4096)
                break
            except TimeoutError:
                pass
    except ConnectionError as reset:
        pytest.fail(f"the client was reset before it read the answer: {reset}")
    connection.settimeout(TIMEOUT)
    for _ in range(20):  # what it still sends is taken: a close with this unread would be a reset
        connection.sendall(b"x" * (1 << 16))
    answer = first + read_all(connection)
    assert answer.startswith(b"HTTP/1.1 413")
    assert eventually(lambda: not forwards_in(engine, container))


def test_the_client_closing_first_ends_the_exec(forward, engine, container):
    connection = forward(ECHO).connect()
    connection.sendall(b"x")
    assert connection.recv(1) == b"x"
    assert forwards_in(engine, container)
    connection.close()
    assert eventually(lambda: not forwards_in(engine, container))


def test_a_client_gone_from_a_server_that_ignores_it_leaves_nothing_running_in_the_agent(sandbox, forward, engine, container):
    connection = forward(DEAF).connect()
    assert eventually(lambda: forwards_in(engine, container))
    connection.close()
    assert eventually(lambda: not forwards_in(engine, container), seconds=90)


def test_nothing_listening_is_said_by_the_forward_and_the_port_forward_keeps_going(forward, container, engine):
    said = engine.exec(container, "egzo", "agent", "forward", "7999")
    assert said.returncode == 1
    assert said.stderr == "nothing listens on port 7999\n", "one line, which the client shows"

    port = forward(7999)
    assert read_all(port.connect()) == b"", "a connection to nothing closes"
    port.client.expect(b"nothing listens on port 7999")
    echo = forward(ECHO).connect()
    echo.sendall(b"x")
    assert echo.recv(1) == b"x"
    assert port.client.isalive()


def test_forward_connects_to_loopback_only(sandbox, engine, container):
    """The control sidecar answers on the agent's network: forward takes a port, never an address."""
    for wrong in ("control:8080", "10.0.0.1:80", "0", "65536", "http"):
        result = engine.exec(container, "egzo", "agent", "forward", wrong)
        assert result.returncode != 0 and "port" in result.stderr


def test_the_local_side_is_chosen_like_kubectl(sandbox, forward):
    free = socket.socket()
    free.bind(("127.0.0.1", 0))
    local = free.getsockname()[1]
    free.close()
    for argument, host in ((f"{local}:{ECHO}", "127.0.0.1"), (f"localhost:{local}:{ECHO}", "127.0.0.1")):
        client = sandbox.pty("agent port-forward host", argument)
        client.expect(str(local).encode())
        connection = socket.create_connection((host, local), timeout=TIMEOUT)
        connection.sendall(b"x")
        assert connection.recv(1) == b"x"
        connection.close()
        client.kill(signal.SIGINT)
        client.expect(pexpect.EOF)
        client.close()
        assert client.exitstatus == 0, "Ctrl-C is how it ends"
        with pytest.raises(OSError):
            socket.create_connection((host, local), timeout=1)

    held = socket.socket()
    held.bind(("127.0.0.1", 0))
    held.listen()
    taken = sandbox.run("agent", "port-forward", "host", f"{held.getsockname()[1]}:{ECHO}")
    held.close()
    assert taken.returncode != 0 and "127.0.0.1" in taken.stderr


def test_the_same_number_is_the_default(sandbox):
    client = sandbox.pty("agent port-forward host", str(ECHO))
    client.expect(rb"127\.0\.0\.1:%d" % ECHO)
    connection = socket.create_connection(("127.0.0.1", ECHO), timeout=TIMEOUT)
    connection.sendall(b"x")
    assert connection.recv(1) == b"x"
    connection.close()
    client.close(force=True)


@pytest.mark.parametrize("wrong", ["http", "0", "65536", "1:2:3:4", "a:b:c", "x:3000"])
def test_a_wrong_port_argument_is_refused(sandbox, wrong):
    result = sandbox.run("agent", "port-forward", "host", wrong)
    assert result.returncode != 0 and "port" in result.stderr.lower()


def test_only_a_running_agent_has_ports_and_a_stopped_one_ends_the_forward(sandbox, engine):
    assert "not an agent" in sandbox.run("agent", "port-forward", "control", "3000").stderr
    assert "spawn it first" in sandbox.run("agent", "port-forward", "web", "3000").stderr

    sandbox.ok("agent spawn web stopping")
    client = sandbox.pty("agent port-forward stopping", f"0:{ECHO}")
    client.expect(rb"127\.0\.0\.1:\d+")
    sandbox.ok("agent stop stopping")
    client.expect(pexpect.EOF, timeout=TIMEOUT)
    client.close()
    assert client.exitstatus != 0, "the port is gone: that is not a success"
    assert re.search(rb"stopping.*(stopped|exited)", client.before)

    stopped = sandbox.run("agent", "port-forward", "stopping", "3000")
    assert stopped.returncode != 0 and "egzo agent start stopping" in stopped.stderr
    sandbox.ok("agent rm stopping")


def test_an_image_without_egzo_says_so_before_listening(sandbox, engine):
    sandbox.up(spec(agents={"web": agent(harness="custom", image=engine.forward_image(sandbox.egzo.binary), env={"FAKE_TUI": "cat"}),
                            "plain": agent(harness="custom", image=engine.agent_image())}))
    sandbox.ok("agent spawn plain bare")
    result = sandbox.run("agent", "port-forward", "bare", str(ECHO))
    assert result.returncode != 0 and "does not contain egzo" in result.stderr
    sandbox.ok("agent rm --force bare")
