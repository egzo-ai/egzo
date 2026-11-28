# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""A message has a subject: one short line a person reads in a list, in a chat card or in the hub's activity, without opening it.

An agent that starts a request names it; a person or the operator may leave it out, and egzo takes the start of the text's first
line. What answers a request (a question, an update, the result) carries the request's subject as "Re: ...". The subject is words of
the sender, like the text: it is never typed into an agent's terminal (test_message_announcements.py)."""

from agentapi import ID

SUBJECT_MAX = 120


def subject_of(team, message_id):
    return next(e for e in team.events() if e["type"] == "message" and e["id"] == message_id)["data"]["subject"]


def test_an_agent_names_what_it_asks_and_the_recipient_reads_the_subject(team):
    coder, reviewer = team.coder, team.reviewer
    code, refused = coder.send(reviewer.address, "please review branch x", subject=None)
    assert code == 400 and "subject" in str(refused), "an agent's request needs a subject"
    for bad in ("", "   ", "two\nlines", "x" * (SUBJECT_MAX + 1), "a \x1b[31mred\x1b[0m one"):
        code, refused = coder.send(reviewer.address, "please review branch x", subject=bad)
        assert code == 400 and "subject" in str(refused), (bad, code, refused)

    code, body = coder.send(reviewer.address, "please review branch x, mostly the parser", subject="Review branch x")
    assert code in (200, 201) and ID.match(body["id"]), body
    sent = body["id"]
    assert subject_of(team, sent) == "Review branch x", "the event carries it"
    assert reviewer.get(sent)["subject"] == "Review branch x"
    listed = reviewer.call("GET", "/v1/messages")[1]
    assert [m["subject"] for m in listed if m["id"] == sent] == ["Review branch x"]
    assert team.messages()[sent]["SUBJECT"] == "Review branch x", "egzo message ls shows it"


def test_the_message_tool_asks_for_a_subject(team):
    coder, reviewer = team.coder, team.reviewer
    (tool,) = [t for t in coder.mcp("tools/list")["result"]["tools"] if t["name"] == "message"]
    assert "subject" in tool["inputSchema"]["properties"] and "subject" in tool["inputSchema"]["required"]
    assert coder.tool("message", to=reviewer.address, text="the parser is slow").get("isError"), "no subject, no message"
    assert not coder.tool("message", to=reviewer.address, subject="Slow parser", text="the parser is slow").get("isError")
    sent = next(e for e in reversed(team.events()) if e["type"] == "message" and e["text"] == "the parser is slow")
    assert sent["data"]["subject"] == "Slow parser"


def test_what_answers_a_request_carries_its_subject(pair, team):
    a, b = pair
    code, body = a.send(b.address, "deploy it", subject="Deploy to staging")
    assert code in (200, 201), body
    request = body["id"]
    b.get(request)
    code, update = b.call("POST", f"/v1/messages/{request}/update", {"text": "building"})
    assert code == 201, update
    code, question = b.call("POST", f"/v1/messages/{request}/ask", {"text": "which branch?"})
    assert code == 201, question
    assert b.resolve(request, "deployed")[0] == 204
    result = next(e for e in team.events_of(request, "message") if e["data"]["kind"] == "resolution")
    for answer in (update["id"], question["id"], result["id"]):
        assert subject_of(team, answer) == "Re: Deploy to staging", answer

    # the answer to the question is about the same request: one "Re:", never two
    a.get(question["id"])
    assert a.resolve(question["id"], "main")[0] == 204
    answer = next(e for e in team.events_of(question["id"], "message") if e["data"]["kind"] == "resolution")
    assert subject_of(team, answer["id"]) == "Re: Deploy to staging"


def test_a_person_may_leave_the_subject_out_and_egzo_takes_the_start_of_the_first_line(team):
    named = team.send("coder", "please summarise the repo", "--subject", "Summary")
    assert subject_of(team, named) == "Summary"
    first_line = "Look at the failing build on main and tell me which commit broke it, with the log lines that show it, " \
                 "and what you would change"
    unnamed = team.send("coder", first_line + "\nmore details\non other lines")
    subject = subject_of(team, unnamed)
    assert len(subject) <= SUBJECT_MAX and subject.endswith("…") and first_line.startswith(subject[:-1].rstrip())
    short = team.send("coder", "fix the typo\nin the README")
    assert subject_of(team, short) == "fix the typo"
    assert team.project.run("message send", "--subject", "two\nlines", "coder", "x").returncode != 0

