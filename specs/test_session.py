"""`egzo attach` and the session backend: the terminal of a harness, untouched.

The session backend is egzo's pty holder (`egzo agent run`): it runs the harness TUI on a terminal
inside the agent container and lets any number of clients attach with a raw pass-through. These
specs run it against a stand-in TUI (fixtures/fake-tui.sh) so every row of the fidelity matrix can
be observed exactly. A regression in the P0 rows is a release blocker.
"""

import base64
import hashlib
import os
import re
import time

import pexpect
import pytest

from conftest import LABEL_PREFIX
from support import agent, spec

pytestmark = pytest.mark.usefixtures("engine")

CTRL_RIGHT_BRACKET = "\x1d"


def tui(image, mode="cat", **env):
    return agent(harness="custom", image=image, env={"FAKE_TUI": mode, **env})


@pytest.fixture
def session(live_project, session_image, egzo, engine):
    """Bring up one agent running the fake TUI in the given mode; returns a function that attaches to it."""

    def start(mode="cat", agents=None, **env):
        # one template per agent (`<name>-template`) and one instance of each, named `<name>`
        agents = agents or {"coder": tui(session_image, mode, **env)}
        # a project holds one set of templates: what an earlier call spawned is stale once they change
        for earlier in engine.instances(live_project.name):
            live_project.run("rm", "--force", earlier.labels[f"{LABEL_PREFIX}instance"])
        live_project.up(spec(agents={f"{name}-template": fields for name, fields in agents.items()}))
        for name in agents:
            live_project.spawn(f"{name}-template", name)

    start.project = live_project
    start.egzo = egzo
    return start


def attach(session, *args, agent_name="coder", dimensions=(24, 80), timeout=30):
    environment = dict(os.environ)
    environment.pop("EGZO_PROJECT_NAME", None)
    environment.update(session.project.env)
    return pexpect.spawn(
        session.egzo.binary, ["attach", *args, agent_name], cwd=session.project.root, env=environment,
        dimensions=dimensions, encoding=None, timeout=timeout,
    )


def detach(client):
    client.send(CTRL_RIGHT_BRACKET)
    client.expect(pexpect.EOF)
    client.close()
    return client.exitstatus


def state_of(project, name):
    for line in project.run("ps").stdout.splitlines()[1:]:
        cells = line.split()
        if len(cells) > 2 and cells[0] == name:
            return cells[2]


# --- P0 rows ------------------------------------------------------------------------------------


def test_attach_shows_the_programs_output_and_passes_typed_input(session):
    session("cat")
    client = attach(session)
    client.expect(b"READY")
    client.send(b"hello there")
    client.expect(b"hello there")
    detach(client)


def test_every_key_byte_passes_through_unchanged(session):
    """Shift+Enter (ESC CR), arrows, Ctrl keys, Alt keys, Tab, Backspace: raw bytes both ways."""
    session("cat")
    client = attach(session)
    client.expect(b"READY")
    sent = bytes(b for b in range(1, 256) if b != 0x1D)  # everything but the detach key
    for start in range(0, len(sent), 16):
        chunk = sent[start : start + 16]
        client.send(chunk)
        seen = b""
        while seen != chunk:
            seen += client.read_nonblocking(len(chunk) - len(seen), timeout=10)
            assert chunk.startswith(seen), f"bytes changed on the way: sent {chunk!r}, got {seen!r}"
    detach(client)


def test_a_large_bracketed_paste_arrives_intact(session):
    session("cat")
    client = attach(session)
    client.expect(b"READY")
    body = b"".join(b"pasted line %05d\r" % i for i in range(6000))
    payload = b"\x1b[200~" + body + b"\x1b[201~"
    client.send(payload)
    received = b""
    deadline = time.time() + 60
    while len(received) < len(payload) and time.time() < deadline:
        try:
            received += client.read_nonblocking(65536, timeout=5)
        except pexpect.TIMEOUT:
            pass
    assert hashlib.sha256(received).hexdigest() == hashlib.sha256(payload).hexdigest()
    detach(client)


def test_the_window_size_reaches_the_program_and_follows_resizes(session):
    session("resize")
    client = attach(session, dimensions=(30, 100))
    client.expect(rb"READY size:24 80")  # what it printed before anyone attached
    client.expect(rb"size:30 100")  # then the size of this terminal
    client.setwinsize(45, 160)
    client.expect(rb"size:45 160")
    detach(client)


def test_the_program_gets_a_truecolor_terminal(session):
    session("env")
    client = attach(session)
    client.expect(rb"READY TERM=xterm-256color COLORTERM=truecolor")
    detach(client)


def test_the_session_adds_nothing_to_the_screen(session):
    """No alternate screen, no status bar, no clear: the user's own scrollback stays theirs."""
    session("cat")
    client = attach(session)
    client.expect(b"READY")
    output = client.before + client.after
    client.send(b"x")
    client.expect(b"x")
    output += client.before + client.after
    for forbidden in (b"\x1b[?1049", b"\x1b[?47", b"\x1b[2J", b"\x1b[?1000", b"\x1b[?2004"):
        assert forbidden not in output, forbidden
    detach(client)


def test_control_c_and_control_d_reach_the_program(session):
    session("sigint")
    client = attach(session)
    client.expect(b"READY")
    client.send(b"\x03")
    client.expect(b"got-int")
    client.send(b"\x03")
    client.expect(b"got-int")  # twice: the attach client is still there
    detach(client)

    session("lines")
    client = attach(session)
    client.expect(b"READY")
    client.send(b"\x04")  # end of input at the start of a line
    client.expect(b"eof-seen")
    detach(client)


def test_escape_sequences_the_terminal_understands_pass_through_untouched(session):
    session("emit")
    client = attach(session)
    client.expect(rb"wide:\xe4\xbd\xa0\xe5\xa5\xbd\xf0\x9f\x98\x80")
    seen = client.before
    assert b"\x1b]52;c;" + base64.b64encode(b"hello") + b"\x07" in seen  # OSC 52 clipboard
    assert b"\x1b]8;;http://example.test/\x07link\x1b]8;;\x07" in seen  # OSC 8 hyperlink
    assert b"\x1b[38;2;1;2;3mtruecolor" in seen  # 24-bit colour
    detach(client)


# --- detach, reattach, several clients ------------------------------------------------------------


def test_the_detach_key_leaves_the_program_running_and_exits_zero(session):
    session("cat")
    client = attach(session)
    client.expect(b"READY")
    assert detach(client) == 0
    assert state_of(session.project, "coder") == "running"


def test_a_custom_detach_key_is_honoured(session):
    session("cat")
    client = attach(session, "--detach-keys", "ctrl-q")
    client.expect(b"READY")
    client.send(b"\x1d")  # no longer the detach key: it goes to the program
    client.expect(b"\x1d")
    client.send(b"\x11")
    client.expect(pexpect.EOF)
    client.close()
    assert client.exitstatus == 0


def test_reattaching_repaints_the_screen(session):
    session("draw")
    first = attach(session, dimensions=(24, 80))
    first.expect(rb"FRAME:\d+")
    detach(first)
    second = attach(session, dimensions=(30, 100))
    second.expect(rb"FRAME:\d+ size:30 100")  # a fresh frame at the new size, not stale bytes
    detach(second)


def test_two_clients_see_the_same_session_and_both_can_type(session):
    session("cat")
    one, two = attach(session), attach(session)
    one.expect(b"READY")
    two.expect(b"READY")
    one.send(b"from-one")
    one.expect(b"from-one")
    two.expect(b"from-one")
    two.send(b"from-two")
    one.expect(b"from-two")
    detach(one)
    detach(two)


def test_a_read_only_observer_sees_everything_and_types_nothing(session):
    session("cat")
    writer, observer = attach(session), attach(session, "--read-only")
    writer.expect(b"READY")
    observer.expect(b"READY")
    observer.send(b"ignored-input")
    writer.send(b"real-input")
    observer.expect(b"real-input")
    writer.expect(b"real-input")
    assert b"ignored-input" not in (writer.before + writer.after)
    detach(observer)
    detach(writer)


def test_an_agent_whose_program_exits_at_once_fails_spawn_with_its_exit_code(live_project, session_image):
    live_project.up(spec(agents={"coder": tui(session_image, "exit")}))
    result = live_project.run("spawn", "coder", timeout=300)
    assert result.returncode != 0
    assert "code 3" in result.stderr and "bye" in result.stderr


def test_when_the_program_exits_attach_exits_with_its_code(session):
    session("lines")
    client = attach(session)
    client.expect(b"READY")
    session.project.run("exec", "coder", "--", "sh", "-c", "pkill -9 -f '^/bin/sh /usr/local/bin/fake-tui'")
    client.expect(pexpect.EOF, timeout=30)
    client.close()
    assert state_of(session.project, "coder") == "exited"


# --- refusals --------------------------------------------------------------------------------------


def test_attach_needs_a_terminal(session):
    session("cat")
    result = session.project.run("attach", "coder", input="")
    assert result.returncode != 0
    assert "terminal" in result.stderr


def test_attach_to_an_unknown_agent_names_the_known_ones(session):
    session("cat")
    client = attach(session, agent_name="nobody")
    client.expect(pexpect.EOF)
    client.close()
    assert client.exitstatus != 0
    assert b"nobody" in client.before and b"coder" in client.before


def test_attach_to_an_agent_without_a_session_explains_what_is_missing(live_project, engine, agent_image, egzo):
    live_project.up(spec(agents={"coder-template": agent(harness="custom", image=agent_image)}))
    live_project.spawn("coder-template", "coder")
    client = pexpect.spawn(egzo.binary, ["attach", "coder"], cwd=live_project.root, env={**os.environ, **live_project.env}, encoding=None)
    client.expect(pexpect.EOF)
    client.close()
    assert client.exitstatus != 0
    assert b"session" in client.before


def test_detaching_turns_off_the_terminal_modes_the_program_turned_on(session):
    """A TUI that enabled mouse tracking, bracketed paste or focus events cannot reset them once the user
    has detached: the user's shell must not be left garbled."""
    session("modes")
    client = attach(session)
    client.expect(b"READY")
    client.send(CTRL_RIGHT_BRACKET)
    client.expect(pexpect.EOF)
    tail = client.before
    client.close()
    for off in (b"\x1b[?1000l", b"\x1b[?1006l", b"\x1b[?2004l", b"\x1b[?1004l", b"\x1b[?25h"):
        assert off in tail, f"{off!r} missing after detach: {tail!r}"
