# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""AHP mode (issue #227): an agent spawned with `--ahp` serves the chat of its main session over the Agent Host Protocol
(https://github.com/microsoft/agent-host-protocol), inside its own container, and the project's link carries it to the hub through the verb
`ahp`, which runs `egzo agent ahp` in the container, the way attach runs `egzo agent attach`: no new port, no new path into the container.

Only a harness that has an AHP mode takes it: OpenCode, through egzo's fork of the opencode-ahp plugin, which runs inside OpenCode's own
server and so sees the TUI's sessions; the terminal stays. The link lets through only what concerns the chat of the agent's main session (the
first session its harness created): no file, no terminal, no changeset, no other session, and nothing that acts in the chat for a client that
only watches. The images are the real ones; no model answers (the turn a message starts fails at the proxy), which is enough for a session and
a turn to exist."""

import json
import re
import time

import pexpect
import pytest

from hubkit import eventually
from support import agent, spec

API_KEY = "sk-ant-egzo-spec-not-a-real-key-0000000000"
INIT = {"jsonrpc": "2.0", "id": 1, "method": "initialize",
        "params": {"channel": "ahp-root://", "clientId": "egzo-spec", "protocolVersions": ["1.0.0"], "initialSubscriptions": ["ahp-root://"]}}


@pytest.fixture(scope="module")
def ahp(shared_project, engine, egzo, fake_hub):
    p = shared_project
    p.env.update({"ANTHROPIC_API_KEY": API_KEY})
    oc, cc = engine.harness_image("opencode", egzo.binary), engine.harness_image("claude-code", egzo.binary)
    quick = {"inject": {"human_quiet": "1s"}}
    p.up(
        spec(
            hub={"url": fake_hub.url, "ca": str(fake_hub.ca)},
            egress={"default": {"allow": ["platform.claude.com"], "services": {"anthropic": "env/ANTHROPIC_API_KEY"}}},
            agents={
                "oc": agent(harness="opencode", image=oc, model="anthropic/claude-sonnet-5-5", **quick),
                "cc": agent(harness="claude-code", image=cc),
            },
        ),
        timeout=900,
    )
    p.ok("project register", input=fake_hub.code("alice") + "\n")
    return p


def in_instance(engine, project, name, command):
    return engine.exec(engine.instance(project.name, name).name, "sh", "-c", command)


def bridge(engine, project, name, *lines, seconds=8):
    """What `egzo agent ahp` answers, inside the instance, to lines of JSON sent a second apart."""
    script = "; ".join(f"printf '%s\\n' {json.dumps(json.dumps(line))}; sleep 1" for line in lines)
    return in_instance(engine, project, name, f"({script}; sleep {seconds}) | timeout {seconds + len(lines) + 5} egzo agent ahp")


def messages(output):
    return [json.loads(line) for line in output.splitlines() if line.startswith("{")]


def answer_to(found, id_):
    return next((m for m in found if m.get("id") == id_), None)


def test_only_a_harness_with_an_ahp_mode_takes_it_and_nothing_is_made_for_one_without(ahp, engine):
    refused = ahp.run("agent", "spawn", "cc", "cc-ahp", "--ahp", timeout=300)
    assert refused.returncode != 0
    assert "has no AHP mode" in refused.stderr and "opencode" in refused.stderr, refused.stderr
    assert "cc-ahp" not in ahp.instance_names(), "a refused spawn makes nothing"


def test_an_agent_spawned_in_ahp_mode_serves_ahp_inside_its_container(ahp, engine):
    name = "oc-ahp"
    ahp.ok("agent spawn", "oc", name, "--ahp", timeout=900)
    instance = engine.instance(ahp.name, name)
    assert instance.labels.get("ai.egzo.agent.ahp") == "1", "the instance keeps its mode, for the link and the hub"
    assert "EGZO_AHP=1" in instance.raw["Config"]["Env"]
    endpoint = json.loads(in_instance(engine, ahp, name, "cat $HOME/.egzo/ahp.json").stdout)
    assert endpoint["url"].startswith("ws://127.0.0.1:"), "the server listens on the container's loopback only"
    assert endpoint["token"] and in_instance(engine, ahp, name, "stat -c %a $HOME/.egzo/ahp.json").stdout.strip() == "600"

    def initialized():
        answer = answer_to(messages(bridge(engine, ahp, name, INIT, seconds=3).stdout), 1)
        return answer and answer.get("result")

    result = eventually(initialized, seconds=120)
    assert result, "egzo agent ahp reaches the plugin's server: " + bridge(engine, ahp, name, INIT, seconds=3).stderr
    assert result["protocolVersion"] == "1.0.0"
    assert result["serverInfo"]["name"] == "opencode-ahp"
    # the user's opencode.json is left alone: the plugin comes through OpenCode's environment
    config = json.loads(in_instance(engine, ahp, name, "cat $HOME/.config/opencode/opencode.json").stdout)
    assert "opencode-ahp" not in json.dumps(config)


def test_an_agent_spawned_without_it_has_no_ahp_server(ahp, engine):
    name = ahp.spawn("oc", "oc-plain", timeout=900)
    instance = engine.instance(ahp.name, name)
    assert "ai.egzo.agent.ahp" not in instance.labels
    assert not any(e.startswith("EGZO_AHP=") for e in instance.raw["Config"]["Env"])
    assert in_instance(engine, ahp, name, "test -e $HOME/.egzo/ahp.json").returncode != 0
    ran = in_instance(engine, ahp, name, "egzo agent ahp < /dev/null")
    assert ran.returncode != 0 and "not in AHP mode" in ran.stderr, ran.stderr


def test_the_link_says_which_templates_have_an_ahp_mode_and_which_agents_are_in_it(ahp, fake_hub):
    if "oc-ahp" not in ahp.instance_names():
        ahp.spawn("oc", "oc-ahp", "--ahp", timeout=900)
    published = fake_hub.must(ahp.name, "published")
    assert published["AHP"] == {"oc": {"Terminal": True}}, "OpenCode keeps its terminal in AHP mode; Claude Code has no AHP mode"
    agents = {a["Name"]: a for a in fake_hub.must(ahp.name, "project")["Agents"]}
    assert agents["oc-ahp"].get("AHP") is True
    assert not agents.get("oc-plain", {}).get("AHP")


def test_the_hub_reaches_the_chat_of_the_main_session_and_nothing_else(ahp, engine, fake_hub):
    if "oc-ahp" not in ahp.instance_names():
        ahp.spawn("oc", "oc-ahp", "--ahp", timeout=900)
    name = "oc-ahp"
    # a message is typed into the TUI, on the agent's main session, once the agent is idle and its terminal quiet: its turn is in the chat
    ahp.ok("message send", name, "say hello")
    main = eventually(lambda: in_instance(engine, ahp, name, "cat $HOME/.egzo/main-session 2>/dev/null").stdout.strip(), seconds=120)
    assert main.startswith("ses"), "egzo keeps the agent's main session"
    chat = f"ahp-chat:/{main}"

    def typed_turn_in_chat():
        found = messages(bridge(engine, ahp, name, INIT, {"jsonrpc": "2.0", "id": 2, "method": "subscribe", "params": {"channel": chat}}, seconds=3).stdout)
        state = ((answer_to(found, 2) or {}).get("result") or {}).get("snapshot", {}).get("state", {})
        return state.get("turns") or state.get("activeTurn")

    eventually(typed_turn_in_chat, seconds=120)

    def ask(*lines, read_only=False, seconds=6):
        answer = fake_hub.verb(ahp.name, "ahp", Agent=name, Input="\n".join(json.dumps(l) for l in lines), Seconds=seconds + len(lines), ReadOnly=read_only)
        assert not answer.get("error"), answer["error"]
        return messages(answer["result"])

    got = ask(INIT,
              {"jsonrpc": "2.0", "id": 2, "method": "subscribe", "params": {"channel": chat}},
              {"jsonrpc": "2.0", "id": 3, "method": "resourceRead", "params": {"uri": "file:///etc/passwd"}},
              {"jsonrpc": "2.0", "id": 4, "method": "createSession", "params": {"channel": "ahp-session:/other"}},
              {"jsonrpc": "2.0", "id": 5, "method": "createTerminal", "params": {"channel": "x-term:/1"}})
    note = next((m for m in got if m.get("method") == "x-egzo/mainSession"), None)
    assert note and note["params"] == {"session": f"ahp-session:/{main}", "chat": chat}, got
    snapshot = answer_to(got, 2)
    assert snapshot and snapshot.get("result"), f"the main session's chat can be subscribed to: {snapshot}"
    # the turn is still running while OpenCode retries the model the proxy refuses, or it has ended: either way it is in the chat
    state = snapshot["result"]["snapshot"]["state"]
    turns = state["turns"] + ([state["activeTurn"]] if state.get("activeTurn") else [])
    assert turns and turns[0]["message"]["text"].strip(), f"the turn the message started in the TUI is in the chat: {state}"
    for refused in (3, 4, 5):
        error = (answer_to(got, refused) or {}).get("error")
        assert error and error["message"].startswith("egzo:"), f"request {refused} is refused by the link: {answer_to(got, refused)}"
    assert "root:x:0:0" not in json.dumps(got)

    watching = ask(INIT, {"jsonrpc": "2.0", "id": 2, "method": "subscribe", "params": {"channel": chat}},
                   {"jsonrpc": "2.0", "id": 6, "method": "dispatchAction",
                    "params": {"channel": chat, "clientSeq": 1, "action": {"type": "chat/turnCancelled", "turnId": "t"}}},
                   read_only=True)
    assert answer_to(watching, 2).get("result"), "a client that only watches still sees the chat"
    assert (answer_to(watching, 6) or {}).get("error"), "and cannot act in it"


def test_the_verb_is_refused_for_an_agent_not_in_ahp_mode(ahp, fake_hub):
    if "oc-plain" not in ahp.instance_names():
        ahp.spawn("oc", "oc-plain", timeout=900)
    answer = fake_hub.verb(ahp.name, "ahp", Agent="oc-plain", Input=json.dumps(INIT), Seconds=3)
    assert "not spawned in AHP mode" in (answer.get("error") or ""), answer


def ask_hub(ahp, fake_hub, name, *lines, read_only=False, seconds=6):
    """What the hub gets back, through the link's verb ahp, for lines of JSON sent a second apart."""
    answer = fake_hub.verb(ahp.name, "ahp", Agent=name, Input="\n".join(json.dumps(l) for l in lines), Seconds=seconds + len(lines), ReadOnly=read_only)
    assert not answer.get("error"), answer["error"]
    return messages(answer["result"])


def sessions_in(engine, project, name):
    """Every session the agent's AHP server knows, unfiltered: what `egzo agent ahp` answers inside the container."""
    found = messages(bridge(engine, project, name, INIT, {"jsonrpc": "2.0", "id": 3, "method": "listSessions", "params": {}}, seconds=3).stdout)
    answer = answer_to(found, 3) or {}
    return [item["resource"] for item in (answer.get("result") or {}).get("items", [])]


def tui_text(project, name, seconds=8):
    """What the agent's TUI shows, read through `agent attach --read-only` for a few seconds, as plain text."""
    client = project.pty("agent attach --read-only", name, dimensions=(40, 140), timeout=60)
    shown = b""
    deadline = time.time() + seconds
    try:
        while time.time() < deadline:
            shown += client.read_nonblocking(65536, timeout=1)
    except (pexpect.TIMEOUT, pexpect.EOF):
        pass
    client.send(b"\x1d")
    client.expect(pexpect.EOF)
    text = re.sub(rb"\x1b\[[0-9;?<>=]*[A-Za-z~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b.", b" ", shown).decode("utf-8", "replace")
    return re.sub(r"\s+", " ", text)


def test_an_agent_spawned_in_ahp_mode_without_a_task_takes_its_first_prompt_from_the_hub(ahp, engine, fake_hub):
    # Nothing is typed into a new agent that has no task, and OpenCode makes a session only for a first prompt: the main session is made
    # when the agent starts, so the chat has one to show and to send the first prompt to. The TUI is on that same session.
    name = "oc-quiet"
    ahp.ok("agent spawn", "oc", name, "--ahp", timeout=900)
    main = eventually(lambda: in_instance(engine, ahp, name, "cat $HOME/.egzo/main-session 2>/dev/null").stdout.strip(), seconds=120)
    assert main.startswith("ses"), "an agent in AHP mode has its main session before anybody types"
    chat = f"ahp-chat:/{main}"
    eventually(lambda: f"ahp-session:/{main}" in sessions_in(engine, ahp, name), seconds=60)

    got = ask_hub(ahp, fake_hub, name, INIT,
                  {"jsonrpc": "2.0", "id": 2, "method": "subscribe", "params": {"channel": chat}},
                  {"jsonrpc": "2.0", "method": "dispatchAction", "params": {"channel": chat, "clientSeq": 1, "action": {
                      "type": "chat/turnStarted", "turnId": "hub-first", "message": {"text": "say hello", "origin": {"kind": "user"}}}}},
                  seconds=10)
    assert (answer_to(got, 2) or {}).get("result"), f"the main session's chat can be subscribed to before any prompt: {answer_to(got, 2)}"
    started = [m for m in got if m.get("method") == "action" and m["params"]["action"].get("type") == "chat/turnStarted"]
    assert any(m["params"]["action"].get("turnId") == "hub-first" for m in started), f"the hub's first prompt starts a turn: {got}"
    # and the terminal shows it: the TUI is on the main session, not on its welcome screen
    shown = eventually(lambda: (lambda t: t if "say hello" in t else None)(tui_text(ahp, name)), seconds=60)
    assert shown, "the TUI shows the turn the hub started: it is on the main session"

    # what is typed into the TUI is a turn of the same session: OpenCode made no other one
    ahp.ok("message send", name, "and one from the terminal")
    time.sleep(20)  # typed once the agent is idle and the terminal quiet (human_quiet 1s), and run
    assert sessions_in(engine, ahp, name) == [f"ahp-session:/{main}"], "the TUI continues the main session"

    # a restart keeps it: the TUI comes back on it
    ahp.ok("agent stop", name, timeout=300)
    ahp.ok("agent start", name, timeout=300)
    eventually(lambda: f"ahp-session:/{main}" in sessions_in(engine, ahp, name), seconds=120)
    assert in_instance(engine, ahp, name, "cat $HOME/.egzo/main-session").stdout.strip() == main
    assert sessions_in(engine, ahp, name) == [f"ahp-session:/{main}"]


def test_an_agent_spawned_in_ahp_mode_with_a_task_starts_on_it_in_its_main_session(ahp, engine, fake_hub):
    # The hub's spawn with a task, in AHP mode: the TUI is started on the main session, where OpenCode draws no "Ask anything" to say it
    # takes input. The task starts a turn of the main session through the AHP server, at once: the chat has it, even one the hub opened
    # while it started, and the TUI shows it, being on that session. It used to be typed blind after a minute, and was lost when the TUI
    # was not reading yet; a prompt that reached OpenCode while the chat was first opened was missing from the chat.
    name, task = "oc-tasked", "list the files of the workspace"
    started = time.time()
    fake_hub.must(ahp.name, "spawn", Template="oc", Name=name, Actor="user:7", StarterName="Joe", Task=task, AHP=True)
    main = eventually(lambda: in_instance(engine, ahp, name, "cat $HOME/.egzo/main-session 2>/dev/null").stdout.strip(), seconds=120)
    chat = f"ahp-chat:/{main}"

    def turns():
        # what the hub's chat page does when it opens: list the sessions, then subscribe to the main chat
        got = ask_hub(ahp, fake_hub, name, INIT, {"jsonrpc": "2.0", "id": 2, "method": "listSessions", "params": {}},
                      {"jsonrpc": "2.0", "id": 3, "method": "subscribe", "params": {"channel": chat}}, seconds=2)
        state = (((answer_to(got, 3) or {}).get("result") or {}).get("snapshot") or {}).get("state") or {}
        texts = [t["message"]["text"] for t in state.get("turns", [])]
        if state.get("activeTurn"):
            texts.append(state["activeTurn"]["message"]["text"])
        return texts if task in texts else None

    found = eventually(turns, seconds=40)
    assert found == [task], f"the task is the chat's first turn, within 40s of the spawn ({time.time() - started:.0f}s): {found}"
    assert task in tui_text(ahp, name, seconds=4), "the TUI shows the task: it is on the main session"
    assert sessions_in(engine, ahp, name) == [f"ahp-session:/{main}"], "the task went to the main session, not to one of its own"
