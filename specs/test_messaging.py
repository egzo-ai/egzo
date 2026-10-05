"""Messages between people and agents: the model, the agent's tools (HTTP and MCP) and the CLI.

Everything said to or by an agent is a message. A message goes queued -> announced -> fetched -> resolved
(with an outcome). The agent fetches, resolves, updates and asks through its tools; people use
`egzo send`, `messages`, `questions` and `answer`. Typing the announcement into the terminal is in
test_injection.py. Here the agents are plain containers that call the control sidecar's agent API with curl,
the way a harness's tool calls do.
"""

import json
import os
import re
import subprocess
import time

import pytest

from conftest import LABEL_PREFIX
from support import agent, spec, table

pytestmark = pytest.mark.usefixtures("engine")

ID = re.compile(r"^m[0-9a-f]{32}$")


def custom(image, **fields):
    return agent(harness="custom", image=image, **fields)


def up(project, document):
    project.write(document)
    result = project.run("up", timeout=300)
    assert result.returncode == 0, result.stderr


def two_agents(project, image):
    up(project, spec(agents={"coder": custom(image), "reviewer": custom(image)}))


def container(engine, project, service):
    return [r for r in engine.containers(project.name) if r.labels.get(f"{LABEL_PREFIX}service") == service][0]


def as_agent(engine, project, name, verb, path, body=None):
    """Call the control sidecar from inside an agent with its own credentials, the way a harness would."""
    command = f'curl -sS -m 10 -w "\\n%{{http_code}}" -u "$EGZO_AGENT:$EGZO_TOKEN" -X {verb} "$EGZO_CONTROL_URL{path}"'
    if body is not None:
        command += f" -H 'content-type: application/json' -d '{json.dumps(body)}'"
    result = engine.exec(container(engine, project, name).name, "sh", "-c", command)
    text, _, code = result.stdout.rpartition("\n")
    return int(code or 0), text


def api(engine, project, name, verb, path, body=None):
    code, text = as_agent(engine, project, name, verb, path, body)
    try:
        return code, json.loads(text)
    except ValueError:
        return code, text


def events(project, *args):
    output = project.run("events", *args).stdout
    return [json.loads(line) for line in output.splitlines() if line.startswith("{")]


def send(project, to, text, *flags):
    result = project.run("send", *flags, to, text)
    assert result.returncode == 0, result.stderr
    return result.stdout.split()[1]


def messages(project, *args):
    result = project.run("messages", *args)
    assert result.returncode == 0, result.stderr
    return {row["ID"]: row for row in table(result.stdout)}


def fetched(engine, project, name, message_id):
    code, body = api(engine, project, name, "GET", f"/v1/messages/{message_id}")
    assert code == 200, body
    return body


# --- a request from the operator --------------------------------------------------------------------


def test_send_queues_a_request_that_messages_lists(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    sent = live_project.run("send", "coder", "please", "review", "the", "diff")
    assert sent.returncode == 0, sent.stderr
    message_id = sent.stdout.split()[1]
    row = messages(live_project)[message_id]
    assert (row["FROM"], row["TO"], row["KIND"], row["STATE"]) == ("operator", "agent:coder", "request", "queued")
    assert "please review the diff" in row["TEXT"]


def test_message_ids_are_unguessable(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    ids = {send(live_project, "coder", f"message {n}") for n in range(3)}
    assert len(ids) == 3 and all(ID.match(i) for i in ids), ids


def test_sending_to_an_unknown_agent_is_an_error(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    result = live_project.run("send", "ghost", "hello")
    assert result.returncode != 0
    assert "ghost" in result.stderr


def test_get_message_gives_the_sender_kind_and_text_and_marks_it_fetched(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    message_id = send(live_project, "coder", "please review the diff")
    message = fetched(engine, live_project, "coder", message_id)
    assert message["id"] == message_id and message["from"] == "operator" and message["kind"] == "request"
    assert message["text"] == "please review the diff" and message["time"]
    assert messages(live_project)[message_id]["STATE"] == "fetched"
    kinds = [e["type"] for e in events(live_project) if e["type"] in ("message", "fetched")]
    assert kinds == ["message", "fetched"]


def test_fetching_again_returns_the_message_and_changes_nothing(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    message_id = send(live_project, "coder", "once")
    fetched(engine, live_project, "coder", message_id)
    again = fetched(engine, live_project, "coder", message_id)
    assert again["text"] == "once"
    assert len([e for e in events(live_project) if e["type"] == "fetched"]) == 1


def test_an_unknown_id_and_another_agents_message_look_exactly_alike(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    private = send(live_project, "coder", "for the coder only")
    foreign = api(engine, live_project, "reviewer", "GET", f"/v1/messages/{private}")
    unknown = api(engine, live_project, "reviewer", "GET", "/v1/messages/m" + "0" * 32)
    assert foreign == unknown and foreign[0] == 404
    assert "no such message" in json.dumps(foreign[1])
    assert messages(live_project)[private]["STATE"] == "queued"  # the attempt did not fetch it


def test_list_messages_shows_only_the_agents_own_open_items(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    first, second = send(live_project, "coder", "one"), send(live_project, "coder", "two")
    send(live_project, "reviewer", "not yours")
    assert api(engine, live_project, "coder", "GET", "/v1/messages")[1] == []  # nothing announced or fetched yet
    fetched(engine, live_project, "coder", first)
    listed = api(engine, live_project, "coder", "GET", "/v1/messages")[1]
    assert [m["id"] for m in listed] == [first]
    api(engine, live_project, "coder", "POST", f"/v1/messages/{first}/resolve", {"text": "done", "outcome": "done"})
    assert api(engine, live_project, "coder", "GET", "/v1/messages")[1] == []
    assert second not in json.dumps(listed)


# --- resolve, update, ask ------------------------------------------------------------------------------


def test_resolve_closes_a_fetched_request_and_sends_the_result_back(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    message_id = send(live_project, "coder", "run the tests")
    fetched(engine, live_project, "coder", message_id)
    code, _ = api(engine, live_project, "coder", "POST", f"/v1/messages/{message_id}/resolve", {"text": "all green", "outcome": "done"})
    assert code in (200, 204)
    assert message_id not in messages(live_project)  # no longer open
    everything = messages(live_project, "--all")
    assert everything[message_id]["STATE"] == "resolved"
    reply = next(e for e in events(live_project) if e["type"] == "message" and e["data"]["kind"] == "resolution")
    assert reply["actor"] == "agent:coder" and reply["data"]["to"] == "operator" and reply["data"]["re"] == message_id
    assert reply["text"] == "all green"
    resolved = next(e for e in events(live_project) if e["type"] == "resolved")
    assert resolved["id"] == message_id and resolved["data"]["outcome"] == "done"


def test_resolve_refuses_an_unfetched_message_a_second_resolution_and_a_bad_outcome(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    message_id = send(live_project, "coder", "do it")
    path = f"/v1/messages/{message_id}/resolve"
    body = {"text": "ok", "outcome": "done"}
    code, text = api(engine, live_project, "coder", "POST", path, body)
    assert code in (400, 409) and "fetch" in json.dumps(text)
    fetched(engine, live_project, "coder", message_id)
    assert api(engine, live_project, "coder", "POST", path, {"text": "ok", "outcome": "perhaps"})[0] == 400
    assert api(engine, live_project, "coder", "POST", path, {"text": "", "outcome": "done"})[0] == 400
    assert api(engine, live_project, "coder", "POST", path, body)[0] in (200, 204)
    code, text = api(engine, live_project, "coder", "POST", path, body)
    assert code in (400, 409) and "resolved" in json.dumps(text)  # terminal
    assert api(engine, live_project, "reviewer", "POST", path, body)[0] == 404  # not its message


def test_an_agent_can_decline_or_fail_a_request(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    for outcome in ("declined", "failed"):
        message_id = send(live_project, "coder", f"{outcome} please")
        fetched(engine, live_project, "coder", message_id)
        code, _ = api(engine, live_project, "coder", "POST", f"/v1/messages/{message_id}/resolve", {"text": "no", "outcome": outcome})
        assert code in (200, 204)
        assert next(e for e in events(live_project) if e["type"] == "resolved" and e["id"] == message_id)["data"]["outcome"] == outcome


def test_update_sends_progress_to_the_requester_without_announcing_it(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    message_id = send(live_project, "coder", "a long job")
    path = f"/v1/messages/{message_id}/update"
    assert api(engine, live_project, "coder", "POST", path, {"text": "early"})[0] in (400, 409)  # fetch it first
    fetched(engine, live_project, "coder", message_id)
    code, body = api(engine, live_project, "coder", "POST", path, {"text": "halfway there"})
    assert code in (200, 201) and ID.match(body["id"])
    update = next(e for e in events(live_project) if e["type"] == "message" and e["data"]["kind"] == "update")
    assert update["text"] == "halfway there" and update["data"]["re"] == message_id and update["data"]["to"] == "operator"
    assert messages(live_project)[message_id]["STATE"] == "fetched"  # the request is still open


def test_ask_questions_the_requester_keeps_the_request_open_and_the_answer_comes_back(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    message_id = send(live_project, "coder", "deploy it")
    fetched(engine, live_project, "coder", message_id)
    code, body = api(engine, live_project, "coder", "POST", f"/v1/messages/{message_id}/ask", {"text": "to prod?", "choices": ["yes", "no"]})
    assert code in (200, 201) and ID.match(body["id"])
    question_id = body["id"]
    listing = live_project.run("questions").stdout
    assert question_id in listing and "to prod?" in listing
    question = messages(live_project)[question_id]
    assert (question["FROM"], question["TO"], question["KIND"]) == ("agent:coder", "operator", "question")
    assert messages(live_project)[message_id]["STATE"] == "fetched"  # still open

    ps = table(live_project.run("ps").stdout)
    coder = next(row for row in ps if row["SERVICE"] == "coder")
    assert coder["OPEN"] == "1" and coder["WAITING"] == "yes"

    answered = live_project.run("answer", question_id, "yes", "after", "the", "tests")
    assert answered.returncode == 0, answered.stderr
    answer = next(e for e in events(live_project) if e["type"] == "message" and e["data"]["kind"] == "resolution")
    assert answer["data"]["to"] == "agent:coder" and answer["data"]["re"] == question_id and answer["actor"] == "operator"
    assert answer["text"] == "yes after the tests"
    assert next(row for row in table(live_project.run("ps").stdout) if row["SERVICE"] == "coder")["WAITING"] == ""
    assert live_project.run("answer", question_id, "again").returncode != 0  # terminal


def test_answer_only_resolves_what_is_addressed_to_a_person(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    message_id = send(live_project, "coder", "mine to do")
    result = live_project.run("answer", message_id, "I did it myself")
    assert result.returncode != 0
    assert "agent:coder" in result.stderr
    assert live_project.run("answer", "m" + "0" * 32, "x").returncode != 0


# --- agents talk to each other and to the operator ----------------------------------------------------


def test_an_agent_sends_a_message_to_another_agent(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    code, body = api(engine, live_project, "coder", "POST", "/v1/messages", {"to": "agent:reviewer", "text": "please review branch x"})
    assert code in (200, 201) and ID.match(body["id"])
    row = messages(live_project)[body["id"]]
    assert (row["FROM"], row["TO"], row["KIND"], row["STATE"]) == ("agent:coder", "agent:reviewer", "request", "queued")
    message = fetched(engine, live_project, "reviewer", body["id"])
    assert message["from"] == "agent:coder" and message["text"] == "please review branch x"
    assert api(engine, live_project, "coder", "GET", f"/v1/messages/{body['id']}")[0] == 404  # not the sender's to fetch


def test_the_resolution_of_a_request_goes_back_to_the_agent_that_sent_it(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    sent = api(engine, live_project, "coder", "POST", "/v1/messages", {"to": "agent:reviewer", "text": "review please"})[1]["id"]
    fetched(engine, live_project, "reviewer", sent)
    api(engine, live_project, "reviewer", "POST", f"/v1/messages/{sent}/resolve", {"text": "looks good", "outcome": "done"})
    reply = next(e for e in events(live_project) if e["type"] == "message" and e["data"]["kind"] == "resolution")
    assert reply["data"]["to"] == "agent:coder" and reply["actor"] == "agent:reviewer" and reply["data"]["re"] == sent
    got = fetched(engine, live_project, "coder", reply["id"])
    assert got["kind"] == "resolution" and got["text"] == "looks good" and got["outcome"] == "done"


def test_a_message_to_an_unknown_agent_to_oneself_or_with_a_bad_address_is_refused(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    for to in ("agent:ghost", "agent:coder", "reviewer", "nobody", ""):
        code, text = api(engine, live_project, "coder", "POST", "/v1/messages", {"to": to, "text": "x"})
        assert code in (400, 404), (to, code, text)
    assert api(engine, live_project, "coder", "POST", "/v1/messages", {"to": "agent:reviewer", "text": "  "})[0] == 400
    assert not [e for e in events(live_project) if e["type"] == "message"]


def test_a_message_to_the_operator_waits_until_it_is_answered(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    sent = api(engine, live_project, "coder", "POST", "/v1/messages", {"to": "operator", "text": "I am done with the migration"})[1]["id"]
    row = messages(live_project)[sent]
    assert (row["FROM"], row["TO"], row["STATE"]) == ("agent:coder", "operator", "queued")
    assert live_project.run("answer", sent, "thanks").returncode == 0
    assert sent not in messages(live_project)


def test_a_reply_chain_is_threaded_and_a_thread_has_a_depth_limit(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    parent = send(live_project, "coder", "start")
    fetched(engine, live_project, "coder", parent)
    child = api(engine, live_project, "coder", "POST", "/v1/messages", {"to": "agent:reviewer", "text": "child", "re": parent})[1]["id"]
    assert next(e for e in events(live_project) if e["id"] == child)["data"]["re"] == parent
    assert api(engine, live_project, "coder", "POST", "/v1/messages", {"to": "agent:reviewer", "text": "x", "re": "m" + "0" * 32})[0] in (400, 404)
    assert api(engine, live_project, "reviewer", "POST", "/v1/messages", {"to": "agent:coder", "text": "x", "re": parent})[0] in (400, 404)  # not its thread

    current, sender, receiver = child, "reviewer", "coder"
    for hop in range(2, 9):  # the thread is 8 messages deep at most, counting the first
        fetched(engine, live_project, sender, current)
        code, body = api(engine, live_project, sender, "POST", "/v1/messages", {"to": f"agent:{receiver}", "text": f"hop {hop}", "re": current})
        assert code in (200, 201), (hop, code, body)
        current, sender, receiver = body["id"], receiver, sender
    fetched(engine, live_project, sender, current)
    code, text = api(engine, live_project, sender, "POST", "/v1/messages", {"to": f"agent:{receiver}", "text": "one too many", "re": current})
    assert code in (400, 429) and "deep" in json.dumps(text)


def test_a_recipient_cannot_be_buried_in_open_requests(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    for n in range(20):
        code, body = api(engine, live_project, "coder", "POST", "/v1/messages", {"to": "agent:reviewer", "text": f"request {n}"})
        assert code in (200, 201), (n, code, body)
    code, text = api(engine, live_project, "coder", "POST", "/v1/messages", {"to": "agent:reviewer", "text": "one too many"})
    assert code in (409, 429) and "open" in json.dumps(text)


def test_a_sender_cannot_flood_the_minute(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    for n in range(30):
        code, body = api(engine, live_project, "coder", "POST", "/v1/messages", {"to": "operator", "text": f"note {n}"})
        assert code in (200, 201), (n, code, body)
    code, text = api(engine, live_project, "coder", "POST", "/v1/messages", {"to": "operator", "text": "note 31"})
    assert code == 429 and "minute" in json.dumps(text)


def test_agents_lists_the_others_with_their_activity_and_open_counts(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    message_id = send(live_project, "reviewer", "look at this")
    fetched(engine, live_project, "reviewer", message_id)
    code, listed = api(engine, live_project, "coder", "GET", "/v1/agents")
    assert code == 200
    assert [a["agent"] for a in listed] == ["reviewer"]  # the others, not itself
    assert listed[0]["open"] == 1 and "activity" in listed[0]


# --- the agent API, MCP and what was retired --------------------------------------------------------------------


def test_an_agent_reports_its_status_and_ps_shows_it(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    code, _ = as_agent(engine, live_project, "coder", "POST", "/v1/status", {"text": "refactoring the parser"})
    assert code == 204
    listing = live_project.run("ps").stdout
    assert "refactoring the parser" in next(line for line in listing.splitlines() if "coder" in line)
    assert "refactoring the parser" not in next(line for line in listing.splitlines() if "reviewer" in line)


def test_ps_shows_the_open_and_waiting_columns(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    header = live_project.run("ps").stdout.splitlines()[0].split()
    assert header[:4] == ["NAME", "SERVICE", "STATE", "HEALTH"] and {"ACTIVITY", "OPEN", "WAITING", "STATUS"} <= set(header)


def test_the_control_api_refuses_missing_and_wrong_credentials(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    name = container(engine, live_project, "coder").name

    def status(auth):
        flags = auth if auth is None else f"-u {auth}"
        result = engine.exec(name, "sh", "-c", f'curl -sS -m 10 -o /dev/null -w "%{{http_code}}" {flags or ""} "$EGZO_CONTROL_URL/v1/messages"')
        return result.stdout

    assert status(None) == "401"
    assert status("coder:not-the-token") == "401"
    reviewer_token = dict(i.split("=", 1) for i in container(engine, live_project, "reviewer").raw["Config"]["Env"])["EGZO_TOKEN"]
    assert status(f"coder:{reviewer_token}") == "401"


def test_agents_cannot_reach_operator_verbs(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    for path in ("/events", "/messages", "/questions", "/specs", "/tokens/reviewer", "/queue"):
        code, _ = as_agent(engine, live_project, "coder", "GET", path)
        assert code in (404, 405), f"{path} answered {code} on the agent port"


def test_the_retired_routes_are_gone(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    for verb, path in (("POST", "/v1/say"), ("GET", "/v1/inbox"), ("POST", "/v1/ask"), ("GET", "/v1/questions/q1"), ("POST", "/v1/ack")):
        code, _ = as_agent(engine, live_project, "coder", verb, path, {"text": "x"} if verb == "POST" else None)
        assert code in (404, 405), f"{verb} {path} answered {code}"


def test_hooks_arrive_as_events(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    code, _ = as_agent(engine, live_project, "coder", "POST", "/v1/hooks/Stop", {"last_assistant_message": "all done"})
    assert code == 204
    hooks = [e for e in events(live_project) if e["type"] == "hook"]
    assert hooks and hooks[0]["text"] == "Stop" and "all done" in json.dumps(hooks[0]["data"])


def test_events_can_be_filtered_by_agent(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    as_agent(engine, live_project, "coder", "POST", "/v1/status", {"text": "from coder"})
    as_agent(engine, live_project, "reviewer", "POST", "/v1/status", {"text": "from reviewer"})
    assert [e["text"] for e in events(live_project, "--agent", "reviewer") if e["type"] == "status"] == ["from reviewer"]


def test_following_the_stream_shows_new_events_as_they_happen(live_project, engine, agent_image, egzo):
    two_agents(live_project, agent_image)
    follower = subprocess.Popen(
        [egzo.binary, "events", "-f"], cwd=live_project.root, env={**os.environ, **live_project.env},
        stdout=subprocess.PIPE, text=True,
    )
    try:
        time.sleep(1)
        as_agent(engine, live_project, "coder", "POST", "/v1/status", {"text": "live event"})
        line = follower.stdout.readline()
        assert json.loads(line)["text"] == "live event"
    finally:
        follower.kill()
        follower.wait()


def test_the_event_log_survives_restarting_the_control_sidecar(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    message_id = send(live_project, "coder", "remember me")
    assert live_project.run("restart", "control").returncode == 0
    assert messages(live_project)[message_id]["STATE"] == "queued"
    assert fetched(engine, live_project, "coder", message_id)["text"] == "remember me"
    assert [e["seq"] for e in events(live_project) if e["type"] in ("message", "fetched")] == [1, 2]


def mcp_call(engine, project, name, method, params=None):
    """A JSON-RPC call to the control sidecar's MCP endpoint from inside an agent."""
    body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params or {}})
    command = (
        'curl -sS -m 10 -u "$EGZO_AGENT:$EGZO_TOKEN" -X POST "$EGZO_CONTROL_URL/mcp" '
        "-H 'content-type: application/json' -H 'accept: application/json, text/event-stream' "
        f"-d '{body}'"
    )
    reply = engine.exec(container(engine, project, name).name, "sh", "-c", command)
    assert reply.returncode == 0, reply.stderr
    return json.loads(reply.stdout)


def tool(engine, project, name, tool_name, **arguments):
    result = mcp_call(engine, project, name, "tools/call", {"name": tool_name, "arguments": arguments})["result"]
    return result


def test_the_mcp_endpoint_offers_exactly_the_new_tools(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    reply = mcp_call(engine, live_project, "coder", "tools/list")
    names = {t["name"] for t in reply["result"]["tools"]}
    assert names == {"list_messages", "get_message", "resolve", "update", "ask", "message", "status", "agents"}
    descriptions = " ".join(t["description"] for t in reply["result"]["tools"])
    assert "sparingly" not in descriptions and "check_inbox" not in descriptions


def test_the_whole_lifecycle_works_through_the_mcp_tools(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    message_id = send(live_project, "coder", "please summarise the repo")
    got = tool(engine, live_project, "coder", "get_message", id=message_id)
    assert not got.get("isError") and "please summarise the repo" in json.dumps(got) and "operator" in json.dumps(got)
    assert "please summarise the repo" in json.dumps(tool(engine, live_project, "coder", "list_messages"))
    assert not tool(engine, live_project, "coder", "update", id=message_id, text="reading").get("isError")
    assert not tool(engine, live_project, "coder", "status", text="summarising").get("isError")
    assert "reviewer" in json.dumps(tool(engine, live_project, "coder", "agents"))
    assert not tool(engine, live_project, "coder", "resolve", id=message_id, text="it is a Go project", outcome="done").get("isError")
    assert messages(live_project, "--all")[message_id]["STATE"] == "resolved"


def test_mcp_tools_act_as_the_calling_agent_and_refuse_what_is_not_theirs(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    message_id = send(live_project, "coder", "private")
    assert tool(engine, live_project, "reviewer", "get_message", id=message_id).get("isError")
    assert tool(engine, live_project, "reviewer", "resolve", id=message_id, text="x", outcome="done").get("isError")
    assert tool(engine, live_project, "coder", "message", to="agent:ghost", text="x").get("isError")
    sent = tool(engine, live_project, "coder", "message", to="agent:reviewer", text="from the tool")
    assert not sent.get("isError")
    message = next(e for e in events(live_project) if e["type"] == "message" and e["text"] == "from the tool")
    assert message["actor"] == "agent:coder" and message["data"]["to"] == "agent:reviewer"


# --- send --wait ----------------------------------------------------------------------------------------------------


def waiting_send(egzo, project, *args):
    return subprocess.Popen(
        [egzo.binary, "send", "--wait", *args], cwd=project.root, env={**os.environ, **project.env},
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
    )


def newest_message(project):
    deadline = time.time() + 30
    while time.time() < deadline:
        found = [e for e in events(project) if e["type"] == "message"]
        if found:
            return found[-1]["id"]
        time.sleep(0.3)
    raise AssertionError("no message was queued")


@pytest.mark.parametrize("outcome,code", [("done", 0), ("declined", 3), ("failed", 4)])
def test_send_wait_prints_the_resolution_and_exits_by_outcome(live_project, engine, agent_image, egzo, outcome, code):
    two_agents(live_project, agent_image)
    waiter = waiting_send(egzo, live_project, "coder", "please do it")
    message_id = newest_message(live_project)
    fetched(engine, live_project, "coder", message_id)
    api(engine, live_project, "coder", "POST", f"/v1/messages/{message_id}/update", {"text": "on it"})
    api(engine, live_project, "coder", "POST", f"/v1/messages/{message_id}/resolve", {"text": f"the {outcome} result", "outcome": outcome})
    out, err = waiter.communicate(timeout=60)
    assert waiter.returncode == code, (out, err)
    assert out.strip() == f"the {outcome} result"
    assert "on it" in err


def test_send_wait_times_out_with_its_own_exit_code_and_leaves_the_message_open(live_project, engine, agent_image, egzo):
    two_agents(live_project, agent_image)
    waiter = subprocess.run(
        [egzo.binary, "send", "--wait", "--timeout", "3s", "coder", "never answered"], cwd=live_project.root,
        env={**os.environ, **live_project.env}, capture_output=True, text=True, timeout=60,
    )
    assert waiter.returncode == 5, waiter.stderr
    assert any("never answered" in row["TEXT"] for row in messages(live_project).values())
