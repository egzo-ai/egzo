# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""What an agent can do to the control sidecar, from inside its container with its own credentials: the HTTP tools, the MCP
endpoint, the hooks, and the limits that stop an agent from burying a person or another agent.

One project is up for the whole module (the `team` fixture). A spec that counts or floods takes fresh agents with `pair`:
a rate limit left on `coder` would show in every later spec. The people's side (`egzo message ...`) is in test_message_cli.py."""

import json
import time

from agentapi import ID

ROUTES_OF_THE_OPERATOR = ("/events", "/messages", "/questions", "/specs", "/tokens/reviewer", "/queue")
RETIRED_ROUTES = (("POST", "/v1/say"), ("GET", "/v1/inbox"), ("POST", "/v1/ask"), ("GET", "/v1/questions/q1"), ("POST", "/v1/ack"))


def test_the_agent_api_refuses_missing_and_wrong_credentials_and_the_operators_verbs_and_the_retired_routes(team):
    coder, reviewer = team.coder, team.reviewer

    def status(auth):
        flags = f"-u {auth}" if auth else ""
        return coder.exec(f'curl -sS -m 10 -o /dev/null -w "%{{http_code}}" {flags} "$EGZO_CONTROL_URL/v1/messages"').stdout

    assert status(None) == "401"
    assert status("coder:not-the-token") == "401"
    assert status(f"coder:{reviewer.token()}") == "401", "another agent's token is not this agent's"

    for (code, _), path in zip(coder.calls(*[("GET", path) for path in ROUTES_OF_THE_OPERATOR]), ROUTES_OF_THE_OPERATOR):
        assert code in (404, 405), f"{path} answered {code} on the agent port"
    results = coder.calls(*[(verb, path, {"text": "x"} if verb == "POST" else None) for verb, path in RETIRED_ROUTES])
    for (code, _), (verb, path) in zip(results, RETIRED_ROUTES):
        assert code in (404, 405), f"{verb} {path} answered {code}"


def test_an_agent_sends_to_another_agent_and_the_resolution_comes_back_to_it(team):
    coder, reviewer = team.coder, team.reviewer
    code, body = coder.send(reviewer.address, "please review branch x")
    assert code in (200, 201) and ID.match(body["id"])
    sent = body["id"]
    row = team.messages()[sent]
    assert (row["FROM"], row["TO"], row["KIND"], row["STATE"]) == (coder.address, reviewer.address, "request", "queued")
    message = reviewer.get(sent)
    assert message["from"] == coder.address and message["text"] == "please review branch x"
    assert coder.call("GET", f"/v1/messages/{sent}")[0] == 404, "not the sender's to fetch"

    reviewer.resolve(sent, "looks good")
    reply = next(e for e in team.events_of(sent, "message") if e["data"]["kind"] == "resolution")
    assert reply["data"]["to"] == coder.address and reply["actor"] == reviewer.address
    got = coder.get(reply["id"])
    assert got["kind"] == "resolution" and got["text"] == "looks good" and got["outcome"] == "done"


def test_a_message_to_an_unknown_agent_to_oneself_or_with_a_bad_address_is_refused(pair, team):
    a, b = pair
    for to in ("agent:ghost", a.address, b.name, "nobody", ""):
        code, text = a.send(to, "x")
        assert code in (400, 404), (to, code, text)
    assert a.send(b.address, "  ")[0] == 400
    assert not [e for e in team.events() if e["type"] == "message" and e["actor"] == a.address]


def test_a_reply_chain_is_threaded_and_a_thread_has_a_depth_limit(pair, team):
    a, b = pair
    parent = team.send(a.name, "start")
    a.get(parent)
    child = a.send(b.address, "child", re=parent)[1]["id"]
    assert next(e for e in team.events() if e["id"] == child)["data"]["re"] == parent
    assert a.send(b.address, "x", re="m" + "0" * 32)[0] in (400, 404), "an unknown thread"
    assert b.send(a.address, "x", re=parent)[0] in (400, 404), "not its thread"

    current, sender, receiver = child, b, a
    for hop in range(2, 8):  # the thread is 8 messages deep at most, counting the first: hops 0 to 7
        sender.get(current)
        code, body = sender.send(receiver.address, f"hop {hop}", re=current)
        assert code in (200, 201), (hop, code, body)
        current, sender, receiver = body["id"], receiver, sender
    sender.get(current)
    code, text = sender.send(receiver.address, "one too many", re=current)
    assert code in (400, 429) and "deep" in json.dumps(text)


def test_nothing_can_bury_a_person_or_an_agent_or_flood_the_status_line(pair, team):
    a, b = pair

    # a person: at most 20 open messages (the limit is the person's, so the answers below keep the other specs clear of it)
    results = a.calls(*[("POST", "/v1/messages", {"to": "operator", "text": f"look {n}"}) for n in range(25)])
    codes = [code for code, _ in results]
    for code, body in results:
        if code == 201:
            team.project.ok("message reply", body["id"], "seen")
    assert codes.count(201) == 20 and set(codes) - {201} <= {409, 429}, codes

    # an agent: at most 20 open requests from one sender, and the refusal says why
    results = b.calls(*[("POST", "/v1/messages", {"to": a.address, "text": f"request {n}"}) for n in range(21)])
    assert [code for code, _ in results[:20]] == [201] * 20
    code, text = results[20]
    assert code in (409, 429) and "open" in json.dumps(text)

    # a sender: at most 30 messages a minute, to whoever (here ten different people), counted on the sender
    results = b.calls(*[("POST", "/v1/messages", {"to": f"user:u{n % 10}", "text": f"note {n}"}) for n in range(12)])
    code, text = results[-1]
    assert code == 429 and "minute" in json.dumps(text), [code for code, _ in results]

    # the status line: slowed down, and one agent's flood does not limit another
    codes = [code for code, _ in a.calls(*[("POST", "/v1/status", {"text": f"line {n}"}) for n in range(45)])]
    assert 429 in codes, codes
    assert b.call("POST", "/v1/status", {"text": "fine"})[0] == 204


def test_choices_offered_with_a_question_are_short_plain_text(team):
    coder = team.coder
    message_id = team.send("coder", "do something")
    coder.get(message_id)
    path = f"/v1/messages/{message_id}/ask"
    bad, too_many, good = [
        code for code, _ in coder.calls(
            ("POST", path, {"text": "which?", "choices": ["yes\u001b[2J", "no"]}),
            ("POST", path, {"text": "which?", "choices": [str(n) for n in range(11)]}),
            ("POST", path, {"text": "which?", "choices": ["yes", "no"]}),
        )
    ]
    assert (bad, too_many, good) == (400, 400, 201)


def test_agents_lists_the_others_with_their_activity_and_open_counts_and_status_shows_in_ps(pair, team):
    a, b = pair
    message_id = team.send(b.name, "look at this")
    b.get(message_id)
    code, listed = a.call("GET", "/v1/agents")
    assert code == 200
    others = {item["agent"]: item for item in listed}
    assert a.name not in others, "the others, not itself"
    assert others[b.name]["open"] == 1 and "activity" in others[b.name]

    assert a.call("POST", "/v1/status", {"text": "refactoring the parser"})[0] == 204
    lines = team.project.ok("ps").stdout.splitlines()
    assert "refactoring the parser" in next(line for line in lines if a.name in line.split())
    assert "refactoring the parser" not in next(line for line in lines if b.name in line.split())


def test_hooks_arrive_as_events_without_what_the_agent_read_or_said(team):
    coder, reviewer = team.coder, team.reviewer
    secret = "ghp_SPECSECRETSPECSECRETSPECSECRET"
    assert coder.call("POST", "/v1/hooks/Stop", {"last_assistant_message": "all done"})[0] == 204
    assert coder.call("POST", "/v1/hooks/PostToolUse", {"tool_name": "Read", "tool_response": {"content": f"TOKEN={secret}"}})[0] == 204
    coder.call("POST", "/v1/hooks/UserPromptSubmit", {"prompt": f"my key is {secret}"})
    hooks = [e for e in team.events("--agent", coder.name) if e["type"] == "hook"]
    assert "Stop" in {e["text"] for e in hooks}
    assert any(e.get("data", {}).get("tool_name") == "Read" for e in hooks), "the tool name is what the stream is for"
    stream = team.project.ok("events").stdout
    assert secret not in stream and "all done" not in stream, "tool output and prompts are not recorded, only that the hook happened"
    on_volume = team.engine.exec(team.container("control").name, "sh", "-c", "cat /state/events.jsonl 2>/dev/null || true")
    assert secret not in on_volume.stdout

    # a large payload still counts
    big = (
        "(printf '{\"prompt\":\"'; head -c 150000 /dev/zero | tr '\\0' x; printf '\"}') | "
        'curl -sS -m 20 -o /dev/null -w "%{http_code}" -u "$EGZO_AGENT:$EGZO_TOKEN" -X POST '
        '-H "content-type: application/json" --data-binary @- "$EGZO_CONTROL_URL/v1/hooks/UserPromptSubmit"'
    )
    assert coder.exec(big).stdout.strip() == "204"
    team.project.ok("ps")
    activity = [e for e in team.events("--agent", coder.name) if e["type"] == "activity"]
    assert activity and activity[-1]["text"] == "working"

    # events can be filtered by agent, and followed as they happen
    coder.call("POST", "/v1/status", {"text": "from coder"})
    reviewer.call("POST", "/v1/status", {"text": "from reviewer"})
    assert [e["text"] for e in team.events("--agent", reviewer.name) if e["type"] == "status"][-1] == "from reviewer"
    assert "from coder" not in [e.get("text") for e in team.events("--agent", reviewer.name)]
    last = max(e["seq"] for e in team.events())
    assert team.project.ok("events --after", last).stdout.strip() == "", "after the last event there is nothing yet"
    follower = team.project.pty("events --follow", timeout=30)
    time.sleep(1)
    coder.call("POST", "/v1/status", {"text": "live event"})
    follower.expect(b"live event")
    follower.close()
    newer = [json.loads(line) for line in team.project.ok("events --after", last, "--agent", coder.name).stdout.splitlines() if line.startswith("{")]
    assert [e["text"] for e in newer if e["type"] == "status"] == ["live event"]


def test_the_mcp_endpoint_offers_exactly_the_tools_and_acts_as_the_calling_agent(team):
    coder, reviewer = team.coder, team.reviewer
    reply = coder.mcp("tools/list")
    assert {t["name"] for t in reply["result"]["tools"]} == {"list_messages", "get_message", "resolve", "update", "ask", "message", "status", "agents"}
    descriptions = " ".join(t["description"] for t in reply["result"]["tools"])
    assert "sparingly" not in descriptions and "check_inbox" not in descriptions

    message_id = team.send("coder", "please summarise the repo")
    got = coder.tool("get_message", id=message_id)
    assert not got.get("isError") and "please summarise the repo" in json.dumps(got) and "operator" in json.dumps(got)
    assert "please summarise the repo" in json.dumps(coder.tool("list_messages"))
    assert not coder.tool("update", id=message_id, text="reading").get("isError")
    assert not coder.tool("status", text="summarising").get("isError")
    assert reviewer.name in json.dumps(coder.tool("agents"))
    assert reviewer.tool("get_message", id=message_id).get("isError"), "not the reviewer's message"
    assert reviewer.tool("resolve", id=message_id, text="x", outcome="done").get("isError")
    assert not coder.tool("resolve", id=message_id, text="it is a Go project", outcome="done").get("isError")
    assert team.messages("--all")[message_id]["STATE"] == "resolved"

    assert coder.tool("message", to="agent:ghost", text="x").get("isError")
    assert not coder.tool("message", to=reviewer.address, text="from the tool").get("isError")
    message = next(e for e in team.events() if e["type"] == "message" and e["text"] == "from the tool")
    assert message["actor"] == coder.address and message["data"]["to"] == reviewer.address


def test_the_event_log_survives_restarting_the_control_sidecar(team):
    message_id = team.send("coder", "remember me")
    seen = [e["seq"] for e in team.events_of(message_id)]
    team.project.ok("restart control")
    assert team.messages()[message_id]["STATE"] == "queued"
    assert team.coder.get(message_id)["text"] == "remember me"
    assert [e["seq"] for e in team.events_of(message_id, "message")] == seen
