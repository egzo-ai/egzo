"""Delivering queued messages into an agent's terminal: states, the human-quiet rule, acks.

Agent states: starting, idle, busy, blocked (waiting on a human), stopped. Harness hooks report them
to the control sidecar; the session holder inside the agent asks control for the queue when the agent
is idle and no human has typed for `inject.human_quiet`, then types the messages as one bracketed
paste behind a header and waits for the harness's prompt hook to acknowledge them. These specs drive
that with a stand-in harness (fixtures/fake-tui.sh, mode `hooks`).
"""

import json
import os
import re
import time
from datetime import datetime

import pexpect
import pytest

from conftest import LABEL_PREFIX
from support import agent, spec

pytestmark = pytest.mark.usefixtures("engine")

HEADER = re.compile(r"\[egzo msg (m[0-9a-f]+) from ([^\]]+)\]")


def harness(image, *, inject=None, **env):
    fields = {"harness": "custom", "image": image, "env": {"FAKE_TUI": "hooks", **env}}
    fields["inject"] = {"human_quiet": "1s", "ack_timeout": "20s", "idle_signal": "hook", **(inject or {})}
    return agent(**fields)


@pytest.fixture
def run(live_project, session_image):
    def start(**kwargs):
        live_project.write(spec(agents={"coder": harness(session_image, **kwargs)}))
        result = live_project.run("up", timeout=300)
        assert result.returncode == 0, result.stderr
        return live_project

    return start


def events(project, *args):
    out = project.run("events", *args).stdout
    return [json.loads(line) for line in out.splitlines() if line.startswith("{")]


def when(event):
    return datetime.fromisoformat(event["time"].replace("Z", "+00:00")).timestamp()


def table(output):
    """Rows of a column-aligned table as dicts."""
    lines = output.splitlines()
    starts = [m.start() for m in re.finditer(r"\S+", lines[0]) if m.start() == 0 or lines[0][m.start() - 1] == " "]
    names = lines[0].split()
    rows = []
    for line in lines[1:]:
        cells = [line[a:b].strip() for a, b in zip(starts, starts[1:] + [None])]
        rows.append(dict(zip(names, cells)))
    return rows


def activity(project, name="coder"):
    for row in table(project.run("ps").stdout):
        if row["SERVICE"] == name:
            return row["ACTIVITY"]


def wait_for(check, what, timeout=30):
    deadline = time.time() + timeout
    while time.time() < deadline:
        value = check()
        if value:
            return value
        time.sleep(0.3)
    raise AssertionError(f"timed out waiting for {what}")


def wait_activity(project, expected, timeout=30):
    wait_for(lambda: activity(project) == expected, f"the agent to be {expected} (it is {activity(project)})", timeout)


def message_events(project, kind, agent_name="coder"):
    return [e for e in events(project) if e["type"] == kind and e.get("agent") == agent_name]


def prompts(project):
    """The prompts the harness reports having received, in order."""
    return [
        json.loads(e["data"])["prompt"] if isinstance(e["data"], (str, bytes)) else e["data"]["prompt"]
        for e in events(project)
        if e["type"] == "hook" and e["text"] == "UserPromptSubmit"
    ]


def container(engine, project, service):
    return [r for r in engine.containers(project.name) if r.labels.get(f"{LABEL_PREFIX}service") == service][0]


def as_agent(engine, project, verb, path, body=None, name="coder"):
    command = f'curl -sS -m 10 -o /dev/null -w "%{{http_code}}" -u "$EGZO_AGENT:$EGZO_TOKEN" -X {verb} "$EGZO_CONTROL_URL{path}"'
    if body is not None:
        command += f" -H 'content-type: application/json' -d '{json.dumps(body)}'"
    return engine.exec(container(engine, project, name).name, "sh", "-c", command).stdout.strip()


# --- the inject section of the schema -------------------------------------------------------------


def test_inject_settings_are_validated(project):
    base = {"harness": "custom", "image": "alpine"}
    for inject, complaint in (
        ({"human_quiet": "soon"}, "human_quiet"),
        ({"ack_timeout": "-3s"}, "ack_timeout"),
        ({"idle_signal": "vibes"}, "idle_signal"),
        ({"quiescence": "abc"}, "quiescence"),
        ({"nonsense": 1}, "nonsense"),
    ):
        result = project.config(spec(agents={"coder": {**base, "inject": inject}}))
        assert result.returncode != 0, inject
        assert complaint in result.stderr


def test_inject_defaults_are_resolved(project):
    resolved = project.resolved(spec(agents={"coder": {"harness": "claude-code"}}, egress={"default": {"allow": ["x.test"]}}))
    assert resolved["agents"]["coder"]["inject"] == {"human_quiet": "30s", "ack_timeout": "60s", "idle_signal": "hook", "quiescence": "5s"}
    resolved = project.resolved(spec(agents={"coder": {"harness": "custom", "image": "alpine"}}))
    assert resolved["agents"]["coder"]["inject"]["idle_signal"] == "quiescence"


# --- states ----------------------------------------------------------------------------------------


def test_ps_shows_an_activity_column(run):
    project = run()
    assert "ACTIVITY" in project.run("ps").stdout.splitlines()[0]


def test_an_agent_is_idle_once_its_harness_says_the_session_started(run):
    project = run()
    wait_activity(project, "idle")
    assert [e["text"] for e in events(project) if e["type"] == "activity" and e["agent"] == "coder"][-1] == "idle"


def test_hooks_drive_the_state_of_an_agent(live_project, engine, agent_image):
    live_project.write(spec(agents={"coder": agent(harness="custom", image=agent_image)}))
    assert live_project.run("up", timeout=300).returncode == 0
    steps = [
        ("SessionStart", {}, "idle"),
        ("UserPromptSubmit", {"prompt": "work"}, "busy"),
        ("Notification", {"message": "Claude needs your permission to use Bash"}, "blocked"),
        ("UserPromptSubmit", {"prompt": "yes"}, "busy"),
        ("Stop", {}, "idle"),
        ("Notification", {"message": "Claude is waiting for your input"}, "idle"),
    ]
    for hook, payload, expected in steps:
        assert as_agent(engine, live_project, "POST", f"/v1/hooks/{hook}", payload) == "204"
        assert activity(live_project) == expected, f"after {hook}"


def test_a_stopped_agent_shows_as_stopped(run):
    project = run()
    wait_activity(project, "idle")
    assert project.run("stop", "coder").returncode == 0
    assert activity(project) == "stopped"


def test_a_started_agent_goes_through_starting_to_idle_again(run):
    project = run()
    wait_activity(project, "idle")
    project.run("stop", "coder")
    project.run("start", "coder")
    wait_activity(project, "idle")


# --- delivery --------------------------------------------------------------------------------------


def test_a_queued_message_is_typed_into_the_idle_agent_and_acknowledged(run):
    project = run()
    wait_activity(project, "idle")
    assert project.run("send", "coder", "please review the parser").returncode == 0
    wait_for(lambda: message_events(project, "delivered"), "delivery")
    kinds = [e["type"] for e in events(project) if e["type"] in ("message", "delivering", "delivered") and e["agent"] == "coder"]
    assert kinds == ["message", "delivering", "delivered"]
    (prompt,) = prompts(project)
    assert "please review the parser" in prompt


def test_the_message_carries_a_header_naming_its_id_and_sender(run):
    project = run()
    wait_activity(project, "idle")
    project.run("send", "coder", "hello")
    wait_for(lambda: message_events(project, "delivered"), "delivery")
    (prompt,) = prompts(project)
    found = HEADER.search(prompt)
    assert found, prompt
    queued = message_events(project, "message")[0]
    assert found.group(1) == queued["id"] and found.group(2) == "operator"


def test_the_sender_in_the_header_is_the_actor_of_the_message(run, engine):
    project = run()
    wait_activity(project, "idle")
    control = container(engine, project, "control").name
    body = json.dumps({"to": "coder", "from": "user:cedric", "text": "from the hub"})
    import subprocess

    done = subprocess.run(
        [engine.cli, "exec", "-i", control, "/egzo", "control", "request", "POST", "/queue"],
        input=body, text=True, capture_output=True, env={**os.environ, **engine.env},
    )
    assert done.returncode == 0, done.stderr
    wait_for(lambda: message_events(project, "delivered"), "delivery")
    assert "from user:cedric]" in prompts(project)[0]


def test_text_with_unicode_and_quotes_arrives_whole(run):
    project = run()
    wait_activity(project, "idle")
    project.run("send", "coder", "résumé: use 'quotes' and café ☕")
    wait_for(lambda: message_events(project, "delivered"), "delivery")
    assert "use 'quotes' and caf" in prompts(project)[0]  # printable text; the fake keeps ASCII only


def test_nothing_is_typed_while_the_agent_is_busy(run):
    project = run(FAKE_WORK="6")
    wait_activity(project, "idle")
    project.run("send", "coder", "first")
    wait_activity(project, "busy")
    project.run("send", "coder", "second")
    wait_for(lambda: len(message_events(project, "delivered")) == 2, "both deliveries", timeout=60)
    all_events = events(project)
    first_stop = next(e for e in all_events if e["type"] == "hook" and e["text"] == "Stop")
    second = [e for e in all_events if e["type"] == "delivering"][-1]
    assert second["seq"] > first_stop["seq"]
    assert when(second) - when(first_stop) < 15


def test_messages_queued_while_busy_are_combined_into_one_prompt_and_acked_one_by_one(run):
    project = run(FAKE_WORK="5")
    wait_activity(project, "idle")
    project.run("send", "coder", "first")
    wait_activity(project, "busy")
    for word in ("alpha", "beta", "gamma"):
        project.run("send", "coder", word)
    wait_for(lambda: len(message_events(project, "delivered")) == 4, "all deliveries", timeout=60)
    combined = prompts(project)[1]
    assert all(word in combined for word in ("alpha", "beta", "gamma"))
    assert len(HEADER.findall(combined)) == 3
    assert len(prompts(project)) == 2
    assert len({e["id"] for e in message_events(project, "delivered")}) == 4


def test_a_blocked_agent_gets_nothing_until_a_human_unblocks_it(run, engine):
    project = run()
    wait_activity(project, "idle")
    project.run("send", "coder", "please ask-permission now")
    wait_activity(project, "blocked")
    project.run("send", "coder", "waiting behind the question")
    time.sleep(4)
    assert len(message_events(project, "delivering")) == 1
    assert as_agent(engine, project, "POST", "/v1/hooks/Stop", {}) == "204"  # the human answered, the turn ended
    wait_for(lambda: len(message_events(project, "delivered")) == 2, "delivery after unblocking")


def test_a_message_is_never_typed_while_a_human_is_typing(live_project, session_image, egzo):
    live_project.write(spec(agents={"coder": harness(session_image, inject={"human_quiet": "6s"})}))
    assert live_project.run("up", timeout=300).returncode == 0
    wait_activity(live_project, "idle")
    environment = {**os.environ, **live_project.env}
    environment.pop("EGZO_PROJECT_NAME", None)
    client = pexpect.spawn(egzo.binary, ["attach", "coder"], cwd=live_project.root, env=environment, encoding=None, timeout=30)
    client.expect(b"READY")
    client.send(b"typing")
    typed = time.time()
    live_project.run("send", "coder", "wait for me")
    wait_for(lambda: message_events(live_project, "delivering"), "delivery", timeout=40)
    delivering = message_events(live_project, "delivering")[0]
    assert when(delivering) - typed >= 5.5
    client.send(b"\x1d")
    client.expect(pexpect.EOF)


def test_a_read_only_observer_never_holds_a_message_back(live_project, session_image, egzo):
    live_project.write(spec(agents={"coder": harness(session_image, inject={"human_quiet": "30s"})}))
    assert live_project.run("up", timeout=300).returncode == 0
    wait_activity(live_project, "idle")
    environment = {**os.environ, **live_project.env}
    environment.pop("EGZO_PROJECT_NAME", None)
    observer = pexpect.spawn(egzo.binary, ["attach", "--read-only", "coder"], cwd=live_project.root, env=environment, encoding=None, timeout=30)
    observer.expect(b"READY")
    observer.send(b"keys the observer typed")
    live_project.run("send", "coder", "hello observer")
    wait_for(lambda: message_events(live_project, "delivered"), "delivery despite the observer", timeout=15)
    observer.send(b"\x1d")
    observer.expect(pexpect.EOF)


def test_a_message_the_harness_never_acknowledges_is_unconfirmed_and_never_retried(run):
    project = run(inject={"ack_timeout": "4s"}, FAKE_ACK="0")
    wait_activity(project, "idle")
    project.run("send", "coder", "do you hear me")
    wait_for(lambda: message_events(project, "unconfirmed"), "the unconfirmed event", timeout=30)
    time.sleep(8)
    assert len(message_events(project, "delivering")) == 1
    assert not message_events(project, "delivered")
    queue = project.run("events").stdout  # still there to read, attributed
    assert "unconfirmed" in queue


def test_a_message_for_a_stopped_agent_waits_and_is_delivered_after_it_starts(run):
    project = run()
    wait_activity(project, "idle")
    project.run("stop", "coder")
    assert project.run("send", "coder", "while you were away").returncode == 0
    time.sleep(2)
    assert not message_events(project, "delivering")
    project.run("start", "coder")
    wait_for(lambda: message_events(project, "delivered"), "delivery after the restart", timeout=60)


def test_send_interrupt_stops_the_current_turn_and_then_delivers(run):
    project = run(FAKE_WORK="40")
    wait_activity(project, "idle")
    project.run("send", "coder", "long job")
    wait_activity(project, "busy")
    result = project.run("send", "--interrupt", "coder", "change of plan")
    assert result.returncode == 0, result.stderr
    wait_for(lambda: len(message_events(project, "delivered")) == 2, "delivery after the interrupt", timeout=30)
    assert [e for e in events(project) if e["type"] == "interrupt" and e["agent"] == "coder"]


def test_without_hooks_quiet_output_means_idle_and_the_echoed_header_is_the_ack(live_project, session_image):
    document = spec(agents={"coder": agent(
        harness="custom", image=session_image, env={"FAKE_TUI": "cat"},
        inject={"idle_signal": "quiescence", "quiescence": "2s", "human_quiet": "1s", "ack_timeout": "15s"},
    )})
    live_project.write(document)
    assert live_project.run("up", timeout=300).returncode == 0
    wait_activity(live_project, "idle")
    live_project.run("send", "coder", "no hooks here")
    wait_for(lambda: message_events(live_project, "delivered"), "delivery by quiescence", timeout=40)
    wait_activity(live_project, "idle")


def test_an_escape_sequence_in_a_message_cannot_end_the_paste_and_type_keystrokes(run):
    """A message can come from another agent (handoff): it must stay one paste, never become keys."""
    project = run()
    wait_activity(project, "idle")
    assert project.run("send", "coder", "before\x1b[201~after and then more").returncode == 0
    wait_for(lambda: message_events(project, "delivered"), "delivery")
    time.sleep(2)
    (prompt,) = prompts(project)  # exactly one prompt: nothing was typed outside the paste
    assert "before" in prompt and "after and then more" in prompt
