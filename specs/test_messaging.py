"""The control sidecar's messaging contract: status, say, questions, the queue and the event stream."""

import json
import subprocess
import time

from conftest import LABEL_PREFIX
from support import agent, spec


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


def events(project, *args):
    output = project.run("events", *args).stdout
    return [json.loads(line) for line in output.splitlines() if line.startswith("{")]


def test_an_agent_reports_its_status_and_ps_shows_it(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    code, _ = as_agent(engine, live_project, "coder", "POST", "/v1/status", {"text": "refactoring the parser"})
    assert code == 204
    listing = live_project.run("ps").stdout
    assert "refactoring the parser" in next(line for line in listing.splitlines() if "coder" in line)
    assert "refactoring the parser" not in next(line for line in listing.splitlines() if "reviewer" in line)


def test_the_control_api_refuses_missing_and_wrong_credentials(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    name = container(engine, live_project, "coder").name

    def status(auth):
        flags = auth if auth is None else f"-u {auth}"
        result = engine.exec(name, "sh", "-c", f'curl -sS -m 10 -o /dev/null -w "%{{http_code}}" {flags or ""} "$EGZO_CONTROL_URL/v1/status" -d "{{}}"')
        return result.stdout

    assert status(None) == "401"
    assert status("coder:not-the-token") == "401"
    reviewer_token = dict(i.split("=", 1) for i in container(engine, live_project, "reviewer").raw["Config"]["Env"])["EGZO_TOKEN"]
    assert status(f"coder:{reviewer_token}") == "401"


def test_agents_cannot_reach_operator_verbs(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    for path in ("/events", "/queue", "/questions", "/specs", "/tokens/reviewer"):
        code, _ = as_agent(engine, live_project, "coder", "GET", path)
        assert code in (404, 405), f"{path} answered {code} on the agent port"


def test_what_an_agent_says_is_an_attributed_event(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    as_agent(engine, live_project, "coder", "POST", "/v1/say", {"text": "tests are green"})
    said = [e for e in events(live_project) if e["type"] == "say"]
    assert len(said) == 1
    assert said[0]["agent"] == "coder" and said[0]["actor"] == "agent:coder" and said[0]["text"] == "tests are green"
    assert said[0]["seq"] >= 1 and said[0]["time"]


def test_a_queued_message_reaches_the_agent_once_with_its_sender(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    sent = live_project.run("send", "coder", "please", "review", "the", "diff")
    assert sent.returncode == 0, sent.stderr

    code, body = as_agent(engine, live_project, "coder", "GET", "/v1/inbox")
    messages = json.loads(body)
    assert code == 200 and len(messages) == 1
    assert messages[0]["text"] == "please review the diff" and messages[0]["from"] == "operator"

    again = json.loads(as_agent(engine, live_project, "coder", "GET", "/v1/inbox")[1])
    assert again == []
    assert json.loads(as_agent(engine, live_project, "reviewer", "GET", "/v1/inbox")[1]) == []

    kinds = [e["type"] for e in events(live_project) if e["type"] in ("message", "delivered")]
    assert kinds == ["message", "delivered"]


def test_sending_to_an_unknown_agent_is_an_error(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    result = live_project.run("send", "ghost", "hello")
    assert result.returncode != 0
    assert "ghost" in result.stderr


def test_a_question_is_asked_listed_answered_and_seen_by_the_agent(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    code, body = as_agent(engine, live_project, "coder", "POST", "/v1/ask", {"text": "deploy to prod?"})
    assert code == 200
    question = json.loads(body)["id"]

    listing = live_project.run("questions").stdout
    assert question in listing and "open" in listing and "deploy to prod?" in listing

    answered = live_project.run("answer", question, "yes", "after", "the", "tests")
    assert answered.returncode == 0, answered.stderr
    code, body = as_agent(engine, live_project, "coder", "GET", f"/v1/questions/{question}")
    assert json.loads(body)["answer"] == "yes after the tests"
    assert json.loads(body)["answered_by"] == "operator"
    assert "answered" in live_project.run("questions").stdout

    assert live_project.run("answer", question, "again").returncode != 0
    assert as_agent(engine, live_project, "reviewer", "GET", f"/v1/questions/{question}")[0] == 404


def test_hooks_arrive_as_events(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    code, _ = as_agent(engine, live_project, "coder", "POST", "/v1/hooks/Stop", {"last_assistant_message": "all done"})
    assert code == 204
    hooks = [e for e in events(live_project) if e["type"] == "hook"]
    assert hooks and hooks[0]["text"] == "Stop" and "all done" in json.dumps(hooks[0]["data"])


def test_events_can_be_filtered_by_agent(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    as_agent(engine, live_project, "coder", "POST", "/v1/say", {"text": "from coder"})
    as_agent(engine, live_project, "reviewer", "POST", "/v1/say", {"text": "from reviewer"})
    assert [e["text"] for e in events(live_project, "--agent", "reviewer")] == ["from reviewer"]


def test_following_the_stream_shows_new_events_as_they_happen(live_project, engine, agent_image, egzo):
    two_agents(live_project, agent_image)
    follower = subprocess.Popen(
        [egzo.binary, "events", "-f"], cwd=live_project.root, env={**__import__("os").environ, **live_project.env},
        stdout=subprocess.PIPE, text=True,
    )
    try:
        time.sleep(1)
        as_agent(engine, live_project, "coder", "POST", "/v1/say", {"text": "live event"})
        line = follower.stdout.readline()
        assert json.loads(line)["text"] == "live event"
    finally:
        follower.kill()
        follower.wait()


def test_the_event_log_survives_restarting_the_control_sidecar(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    as_agent(engine, live_project, "coder", "POST", "/v1/say", {"text": "remember me"})
    assert live_project.run("restart", "control").returncode == 0
    texts = [e["text"] for e in events(live_project) if e["type"] == "say"]
    assert texts == ["remember me"]
    as_agent(engine, live_project, "coder", "POST", "/v1/say", {"text": "and me"})
    assert [e["seq"] for e in events(live_project) if e["type"] == "say"] == [1, 2]


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


def test_the_mcp_endpoint_lists_the_agent_tools(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    reply = mcp_call(engine, live_project, "coder", "tools/list")
    names = {tool["name"] for tool in reply["result"]["tools"]}
    assert {"status", "say", "ask_user", "get_answer", "check_inbox"} <= names


def test_an_mcp_tool_call_is_an_event_attributed_to_the_calling_agent(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    reply = mcp_call(engine, live_project, "reviewer", "tools/call", {"name": "say", "arguments": {"text": "looks good"}})
    assert not reply["result"].get("isError")
    said = [e for e in events(live_project) if e["type"] == "say"]
    assert said[0]["agent"] == "reviewer" and said[0]["actor"] == "agent:reviewer" and said[0]["text"] == "looks good"


def test_mcp_and_the_http_api_share_one_queue(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    assert live_project.run("send", "coder", "via the queue").returncode == 0
    reply = mcp_call(engine, live_project, "coder", "tools/call", {"name": "check_inbox", "arguments": {}})
    assert "via the queue" in json.dumps(reply["result"])
    assert json.loads(as_agent(engine, live_project, "coder", "GET", "/v1/inbox")[1]) == []
