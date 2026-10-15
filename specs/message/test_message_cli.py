# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""`egzo message send | ls | reply`, and what a person sees of it in `egzo ps` and `egzo events`.

One project is up for the whole module (the `team` fixture: a template `worker` and two instances, `coder` and `reviewer`).
A spec that counts something (the OPEN and WAITING columns of `ps`) takes fresh agents with `pair`, so that what the other
specs did cannot show in it. What the agents do from inside (the HTTP and MCP tools) is in test_agent_api.py."""

import json
import os
import subprocess
import time

import pytest

from agentapi import ID
from support import table


def test_a_request_is_sent_listed_fetched_by_its_recipient_only_and_resolved(team):
    coder, reviewer = team.coder, team.reviewer
    first = team.send("coder", "please review the diff")
    others = {first, team.send("coder", "second"), team.send("coder", "third")}
    assert len(others) == 3 and all(ID.match(i) for i in others), "message ids are unguessable and distinct"

    row = team.messages()[first]
    assert (row["FROM"], row["TO"], row["KIND"], row["STATE"]) == ("operator", "agent:coder", "request", "queued")
    assert "please review the diff" in row["TEXT"]

    # Another agent's attempt looks exactly like an unknown id, and fetches nothing.
    foreign = reviewer.call("GET", f"/v1/messages/{first}")
    unknown = reviewer.call("GET", "/v1/messages/m" + "0" * 32)
    assert foreign == unknown and foreign[0] == 404 and "no such message" in json.dumps(foreign[1])
    assert team.messages()[first]["STATE"] == "queued"

    message = coder.get(first)
    assert message["id"] == first and message["from"] == "operator" and message["kind"] == "request"
    assert message["text"] == "please review the diff" and message["time"]
    assert coder.get(first)["text"] == "please review the diff", "fetching again returns it and changes nothing"
    assert team.messages()[first]["STATE"] == "fetched"
    assert [e["type"] for e in team.events_of(first, "message", "fetched")] == ["message", "fetched"]

    # list_messages is what the agent owes: its own fetched items, not the queued ones, not another agent's
    listed = [m["id"] for m in coder.call("GET", "/v1/messages")[1]]
    assert first in listed and not (others - {first}) & set(listed)
    assert first not in json.dumps(reviewer.call("GET", "/v1/messages")[1])
    coder.resolve(first)
    assert first not in json.dumps(coder.call("GET", "/v1/messages")[1])


def test_resolving_closes_a_fetched_request_sends_the_result_back_and_refuses_what_is_not_allowed(team):
    coder, reviewer = team.coder, team.reviewer
    unfetched = team.send("coder", "do it")
    path = f"/v1/messages/{unfetched}/resolve"
    code, text = coder.call("POST", path, {"text": "ok", "outcome": "done"})
    assert code in (400, 409) and "fetch" in json.dumps(text), "an unfetched request cannot be resolved"
    coder.get(unfetched)
    assert coder.call("POST", path, {"text": "ok", "outcome": "perhaps"})[0] == 400
    assert coder.call("POST", path, {"text": "", "outcome": "done"})[0] == 400
    assert reviewer.call("POST", path, {"text": "ok", "outcome": "done"})[0] == 404, "not the reviewer's message"
    assert coder.resolve(unfetched, "all green")[0] in (200, 204)
    code, text = coder.resolve(unfetched)
    assert code in (400, 409) and "resolved" in json.dumps(text), "a resolution is final"

    assert unfetched not in team.messages(), "no longer open"
    assert team.messages("--all")[unfetched]["STATE"] == "resolved"
    reply = next(e for e in team.events_of(unfetched, "message") if e["data"]["kind"] == "resolution")
    assert reply["actor"] == "agent:coder" and reply["data"]["to"] == "operator" and reply["text"] == "all green"
    assert next(e for e in team.events_of(unfetched, "resolved"))["data"]["outcome"] == "done"

    for outcome in ("declined", "failed"):
        message_id = team.send("coder", f"{outcome} please")
        coder.get(message_id)
        assert coder.resolve(message_id, "no", outcome)[0] in (200, 204)
        assert next(e for e in team.events_of(message_id, "resolved"))["data"]["outcome"] == outcome


def test_an_agent_asks_a_question_the_operator_answers_and_the_answer_goes_back(pair, team):
    asker, other = pair
    p = team.project
    message_id = team.send(asker.name, "deploy it")
    asker.get(message_id)

    path = f"/v1/messages/{message_id}/update"
    assert other.call("POST", path, {"text": "not mine"})[0] == 404
    code, body = asker.call("POST", path, {"text": "halfway there"})
    assert code in (200, 201) and ID.match(body["id"])
    update = next(e for e in team.events_of(message_id, "message") if e["data"]["kind"] == "update")
    assert update["text"] == "halfway there" and update["data"]["to"] == "operator"
    assert team.messages()[message_id]["STATE"] == "fetched", "an update does not announce itself and the request stays open"

    code, body = asker.call("POST", f"/v1/messages/{message_id}/ask", {"text": "to prod?", "choices": ["yes", "no"]})
    assert code in (200, 201) and ID.match(body["id"])
    question_id = body["id"]
    asked = p.ok("message ls --kind question").stdout
    assert question_id in asked and "to prod?" in asked
    question = team.messages()[question_id]
    assert (question["FROM"], question["TO"], question["KIND"]) == (asker.address, "operator", "question")
    assert team.messages()[message_id]["STATE"] == "fetched", "the request is still open"
    row = next(r for r in table(p.ok("ps").stdout) if r["NAME"] == asker.name)
    assert row["OPEN"] == "1" and row["WAITING"] == "yes"

    p.ok("message reply", question_id, "yes after the tests")
    answer = next(e for e in team.events_of(question_id, "message") if e["data"]["kind"] == "resolution")
    assert answer["data"]["to"] == asker.address and answer["actor"] == "operator" and answer["text"] == "yes after the tests"
    assert next(r for r in table(p.ok("ps").stdout) if r["NAME"] == asker.name)["WAITING"] == ""
    assert p.sh("message reply", question_id, "again").returncode != 0, "an answer is final"

    # a reply only closes what was addressed to a person
    refused = p.sh("message reply", message_id, "I did it myself")
    assert refused.returncode != 0 and asker.address in refused.stderr
    assert p.sh("message reply", "m" + "0" * 32, "x").returncode != 0

    # a message to the operator waits until it is answered
    sent = other.send("operator", "I am done with the migration")[1]["id"]
    row = team.messages()[sent]
    assert (row["FROM"], row["TO"], row["STATE"]) == (other.address, "operator", "queued")
    p.ok("message reply", sent, "thanks")
    assert sent not in team.messages()


def test_messages_reach_instances_that_exist_and_only_those(team):
    p, coder = team.project, team.coder

    late = team.spawn()
    sent = team.send(late.name, "hello at once")
    assert team.messages()[sent]["TO"] == late.address, "a spawned instance can be sent a message at once"
    assert coder.send(late.address, "welcome")[0] == 201, "and so can an agent that was there before it"

    refused = p.sh("message send worker hello")
    assert refused.returncode != 0
    assert "worker is a template" in refused.stderr and "egzo agent spawn worker" in refused.stderr
    ghost = p.sh("message send ghost hello")
    assert ghost.returncode != 0 and "ghost" in ghost.stderr

    leaving = team.spawn()
    owed = team.send(leaving.name, "review this")
    p.ok("agent rm --force", leaving.name)
    refused = p.sh("message send", leaving.name, "anyone there?")
    assert refused.returncode != 0 and leaving.name in refused.stderr
    code, body = coder.send(leaving.address, "x")
    assert code in (400, 404, 409) and leaving.name in json.dumps(body), (code, body)
    assert owed in team.messages("--all"), "the record of a removed instance stays"
    assert team.messages("--all")[owed]["STATE"] == "resolved", "removing it fails what it still owed"

    again = team.spawn(leaving.name)
    assert again.call("GET", "/v1/messages")[1] == [], "a new instance of the same name does not inherit the old requests"


def waiting_send(egzo, project, *args):
    return subprocess.Popen(
        [egzo.binary, "message", "send", "--wait", *args], cwd=project.root, env={**os.environ, **project.env},
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
    )


def queued(team, text):
    """The id of the message with this text, once `send` has queued it."""
    deadline = time.time() + 30
    while time.time() < deadline:
        found = [e for e in team.events() if e["type"] == "message" and e["text"] == text]
        if found:
            return found[-1]["id"]
        time.sleep(0.3)
    raise AssertionError(f"{text!r} was never queued")


@pytest.mark.parametrize("outcome,code", [("done", 0), ("declined", 3), ("failed", 4)])
def test_send_wait_prints_the_resolution_and_exits_by_outcome(team, egzo, outcome, code):
    text = f"please do it, expecting {outcome}"
    waiter = waiting_send(egzo, team.project, "coder", text)
    message_id = queued(team, text)
    team.coder.get(message_id)
    team.coder.call("POST", f"/v1/messages/{message_id}/update", {"text": "on it"})
    team.coder.resolve(message_id, f"the {outcome} result", outcome)
    out, err = waiter.communicate(timeout=60)
    assert waiter.returncode == code, (out, err)
    assert out.strip() == f"the {outcome} result"
    assert "on it" in err


def test_send_wait_times_out_with_its_own_exit_code_and_leaves_the_message_open(team):
    waited = team.project.sh("message send --wait --timeout 3s coder", "never answered", timeout=60)
    assert waited.returncode == 5, waited.stderr
    assert any("never answered" in row["TEXT"] for row in team.messages().values())


def test_what_an_agent_writes_never_acts_on_the_operators_terminal(pair, team):
    a, b = pair
    p = team.project
    nasty = "working\n\x1b]52;c;ZXZpbA==\x07p-forged-1  forged  running\x1b[2J"
    assert a.call("POST", "/v1/status", {"text": nasty})[0] == 204
    a.send("operator", nasty)
    shown = p.ok("ps").stdout
    assert "\x1b" not in shown and "\x07" not in shown
    containers = json.loads(p.ok("ps --json").stdout)
    assert len(shown.strip().splitlines()) == len(containers) + 1, f"the status forged rows:\n{shown}"
    assert "p-forged-1" not in {row["NAME"] for row in table(shown)}
    listed = p.ok("message ls").stdout
    assert "\x1b" not in listed and "\x07" not in listed

    message_id = team.send(b.name, "do it")
    b.get(message_id)
    b.resolve(message_id, "done \x1b]0;pwned\x07 \x1b[31mred")
    waited = p.sh("message send --wait --timeout 3s", b.name, "again")
    assert "\x1b" not in waited.stdout + waited.stderr


def test_message_ls_filters_by_recipient_sender_kind_and_state(pair, team):
    a, b = pair
    p = team.project
    first, second = team.send(a.name, "for the first"), team.send(b.name, "for the second")
    assert set(team.messages("--to", a.name)) == {first}
    assert set(team.messages("--to", b.address)) == {second}
    assert {first, second} <= set(team.messages("--from", "operator"))
    assert team.messages("--from", a.name) == {}
    assert {first, second} <= set(team.messages("--kind", "request")) and first not in team.messages("--kind", "question")
    assert {first, second} <= set(team.messages("--state", "pending")) and first not in team.messages("--state", "resolved")

    p.ok("agent rm --force", a.name)  # fails what it still owed
    assert first in team.messages("--state", "resolved") and first not in team.messages("--state", "pending")
    for flag, value in (("--kind", "rumor"), ("--state", "maybe")):
        refused = p.sh("message ls", flag, value)
        assert refused.returncode != 0 and value in refused.stderr
    assert p.sh("msg send", b.name, "through the alias").returncode == 0, "msg is an alias of message"
    assert p.sh("message reply", "m" + "0" * 32, "x").returncode != 0
