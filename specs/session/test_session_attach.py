# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""`egzo agent attach` and the session backend: the terminal of a harness, untouched.

The session backend is egzo's pty holder (`egzo agent run`): it runs the harness TUI on a terminal inside the agent container and lets any
number of clients attach with a raw pass-through. These specs run it against a stand-in TUI (fixtures/fake-tui.sh, with a mode for each
thing to observe) so every row of the fidelity matrix can be observed exactly. A regression in the P0 rows is a release blocker.

One project is up for the whole module, with a template for each mode of the stand-in; a spec spawns the instances it needs."""

import base64
import hashlib
import os
import time

import pexpect
import pytest

from support import agent, custom, spec

CTRL_RIGHT_BRACKET = "\x1d"
MODES = ["cat", "resize", "env", "sigint", "lines", "emit", "draw", "modes", "exit"]


@pytest.fixture(scope="module")
def sandbox(shared_project, engine, egzo):
    image = engine.session_image(egzo.binary)
    templates = {mode: agent(harness="custom", image=image, env={"FAKE_TUI": mode}) for mode in MODES}
    templates["plain"] = custom(engine.agent_image())
    shared_project.up(spec(agents=templates))
    return shared_project


@pytest.fixture(autouse=True)
def start_from_no_instance(sandbox):
    sandbox.retire()
    yield


def attach(project, *args, name, dimensions=(24, 80), timeout=30):
    environment = dict(os.environ)
    environment.pop("EGZO_PROJECT_NAME", None)
    environment.update(project.env)
    return pexpect.spawn(
        project.egzo.binary, ["agent", "attach", *args, name], cwd=project.root, env=environment,
        dimensions=dimensions, encoding=None, timeout=timeout,
    )


def detach(client):
    client.send(CTRL_RIGHT_BRACKET)
    client.expect(pexpect.EOF)
    client.close()
    return client.exitstatus


def test_attach_passes_every_key_byte_a_large_paste_and_nothing_else_and_detaches_leaving_the_program_running(sandbox):
    p = sandbox
    name = p.spawn("cat")
    client = attach(p, name=name)
    client.expect(b"READY")
    client.send(b"hello there")
    client.expect(b"hello there")
    output = client.before + client.after
    for forbidden in (b"\x1b[?1049", b"\x1b[?47", b"\x1b[2J", b"\x1b[?1000", b"\x1b[?2004"):
        assert forbidden not in output, f"the session adds nothing to the screen: {forbidden!r}"

    # Shift+Enter (ESC CR), arrows, Ctrl keys, Alt keys, Tab, Backspace: raw bytes both ways
    sent = bytes(b for b in range(1, 256) if b != 0x1D)  # everything but the detach key
    for start in range(0, len(sent), 16):
        chunk = sent[start : start + 16]
        client.send(chunk)
        seen = b""
        while seen != chunk:
            seen += client.read_nonblocking(len(chunk) - len(seen), timeout=10)
            assert chunk.startswith(seen), f"bytes changed on the way: sent {chunk!r}, got {seen!r}"

    payload = b"\x1b[200~" + b"".join(b"pasted line %05d\r" % i for i in range(6000)) + b"\x1b[201~"
    client.send(payload)
    received = b""
    deadline = time.time() + 60
    while len(received) < len(payload) and time.time() < deadline:
        try:
            received += client.read_nonblocking(65536, timeout=5)
        except pexpect.TIMEOUT:
            pass
    assert hashlib.sha256(received).hexdigest() == hashlib.sha256(payload).hexdigest(), "a large bracketed paste arrives intact"
    assert detach(client) == 0
    assert p.ps()[name]["STATE"] == "running", "the detach key leaves the program running"


def test_clients_share_a_session_a_custom_detach_key_works_and_an_observer_types_nothing(sandbox):
    p = sandbox
    name = p.spawn("cat")
    one, two = attach(p, name=name), attach(p, name=name)
    one.expect(b"READY")
    two.expect(b"READY")
    one.send(b"from-one")
    one.expect(b"from-one")
    two.expect(b"from-one")
    two.send(b"from-two")
    one.expect(b"from-two")
    observer = attach(p, "--read-only", name=name)
    observer.expect(b"READY")
    observer.send(b"ignored-input")
    one.send(b"real-input")
    observer.expect(b"real-input")
    one.expect(b"real-input")
    assert b"ignored-input" not in (one.before + one.after)
    detach(observer)
    detach(two)
    detach(one)

    custom_key = attach(p, "--detach-keys", "ctrl-q", name=name)
    custom_key.expect(b"READY")
    custom_key.send(b"\x1d")  # no longer the detach key: it goes to the program
    custom_key.expect(b"\x1d")
    custom_key.send(b"\x11")
    custom_key.expect(pexpect.EOF)
    custom_key.close()
    assert custom_key.exitstatus == 0


def test_the_window_size_reaches_the_program_and_a_reattach_repaints_at_the_new_size(sandbox):
    p = sandbox
    sized = p.spawn("resize")
    client = attach(p, name=sized, dimensions=(30, 100))
    client.expect(rb"READY size:24 80")  # what it printed before anyone attached
    client.expect(rb"size:30 100")  # then the size of this terminal
    client.setwinsize(45, 160)
    client.expect(rb"size:45 160")
    detach(client)

    drawn = p.spawn("draw")
    first = attach(p, name=drawn, dimensions=(24, 80))
    first.expect(rb"FRAME:\d+")
    detach(first)
    second = attach(p, name=drawn, dimensions=(30, 100))
    second.expect(rb"FRAME:\d+ size:30 100")  # a fresh frame at the new size, not stale bytes
    detach(second)


def test_the_program_gets_a_truecolor_terminal_its_signals_and_escape_sequences_and_the_modes_it_set_are_reset_on_detach(sandbox):
    p = sandbox
    environment = attach(p, name=p.spawn("env"))
    environment.expect(rb"READY TERM=xterm-256color COLORTERM=truecolor")
    detach(environment)

    sigint = attach(p, name=p.spawn("sigint"))
    sigint.expect(b"READY")
    sigint.send(b"\x03")
    sigint.expect(b"got-int")
    sigint.send(b"\x03")
    sigint.expect(b"got-int")  # twice: the attach client is still there
    detach(sigint)

    lines = p.spawn("lines")
    eof = attach(p, name=lines)
    eof.expect(b"READY")
    eof.send(b"\x04")  # end of input at the start of a line
    eof.expect(b"eof-seen")
    detach(eof)

    emit = attach(p, name=p.spawn("emit"))
    emit.expect(rb"wide:\xe4\xbd\xa0\xe5\xa5\xbd\xf0\x9f\x98\x80")
    seen = emit.before
    assert b"\x1b]52;c;" + base64.b64encode(b"hello") + b"\x07" in seen  # OSC 52 clipboard
    assert b"\x1b]8;;http://example.test/\x07link\x1b]8;;\x07" in seen  # OSC 8 hyperlink
    assert b"\x1b[38;2;1;2;3mtruecolor" in seen  # 24-bit colour
    detach(emit)

    # a TUI that enabled mouse tracking, bracketed paste or focus events cannot reset them once the user has detached
    modes = attach(p, name=p.spawn("modes"))
    modes.expect(b"READY")
    modes.send(CTRL_RIGHT_BRACKET)
    modes.expect(pexpect.EOF)
    tail = modes.before
    modes.close()
    for off in (b"\x1b[?1000l", b"\x1b[?1006l", b"\x1b[?2004l", b"\x1b[?1004l", b"\x1b[?25h"):
        assert off in tail, f"{off!r} missing after detach: {tail!r}"

    # when the program exits, attach exits and the instance is exited
    client = attach(p, name=lines)
    client.expect(b"READY")
    p.sh(f"exec {lines} -- sh -c", "pkill -9 -f '^/bin/sh /usr/local/bin/fake-tui'")
    client.expect(pexpect.EOF, timeout=30)
    client.close()
    assert p.ps()[lines]["STATE"] == "exited"


def test_attach_is_refused_without_a_terminal_for_an_unknown_agent_and_for_an_agent_without_a_session_and_a_program_that_exits_at_once_fails_spawn(sandbox):
    p = sandbox
    name = p.spawn("cat")
    result = p.sh("agent attach", name, input="")
    assert result.returncode != 0 and "terminal" in result.stderr

    unknown = attach(p, name="nobody")
    unknown.expect(pexpect.EOF)
    unknown.close()
    assert unknown.exitstatus != 0 and b"nobody" in unknown.before and name.encode() in unknown.before, "it names the known ones"

    plain = p.spawn("plain")
    sessionless = attach(p, name=plain)
    sessionless.expect(pexpect.EOF)
    sessionless.close()
    assert sessionless.exitstatus != 0 and b"session" in sessionless.before

    exits = p.sh("agent spawn exit", timeout=300)
    assert exits.returncode != 0 and "code 3" in exits.stderr and "bye" in exits.stderr
