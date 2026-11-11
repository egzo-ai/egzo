# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""`hub:` in the project file and the link sidecar: a project dials out to its hub and proves its key. An operator makes a link code, the
project presents it with `egzo project register`, and from then on the link does what the hub asks with egzo's verbs and nothing else. The
hub here is fixtures/fakehub, which is core's real hub side of the protocol.

The story follows one project: not registered, registered, working, dropped, revoked, unlinked."""

import base64
import json
import time

import pytest

from agentapi import ID, Agent
from hubkit import FakeHub, eventually, hub_host
from support import SECRET_KEY_VARIABLE, agent, spec, table


def document(engine, egzo, hub):
    image = engine.session_image(egzo.binary)
    # typed reports each prompt it is typed (fixtures/fake-tui.sh, mode reporting), and fetches and resolves the messages a line names
    typed = agent(harness="custom", image=image, env={"FAKE_TUI": "reporting"}, inject={"human_quiet": "1s", "ack_timeout": "20s", "idle_signal": "state"})
    return spec(hub={"url": hub.url, "ca": str(hub.ca)}, agents={"web": agent(harness="custom", image=image, env={"FAKE_TUI": "cat"}), "typed": typed})


@pytest.fixture(scope="module")
def linked(shared_project, engine, egzo, fake_hub):
    shared_project.write(document(engine, egzo, fake_hub))
    up = shared_project.ok("project up")
    shared_project.up_output = up.stdout + up.stderr  # a link that has no answer yet is a warning
    return shared_project


def link_of(engine, project):
    found = [c for c in engine.containers(project.name) if c.name == f"{project.name}-link-1"]
    return found[0] if found else None


def row(project, name):
    return [r for r in json.loads(project.ok("ps --json").stdout) if r["service"] == name][0]


def check(project):
    return project.run("project", "check")


def register(project, hub, owner="alice"):
    return project.ok("project register", input=hub.code(owner) + "\n")


def test_the_hub_is_an_address_or_an_address_with_a_ca(project, tmp_path):
    project.write(spec(hub="https://hub.example.org/"))
    assert project.ok("project check --resolved").yaml()["hub"] == {"url": "https://hub.example.org"}
    project.write(spec(hub="http://hub.example.org"))
    assert "plain http" in project.ok("project check").stdout


@pytest.mark.parametrize("wrong", ["hub.example.org", "ftp://hub.example.org", "https://", "https://u:p@hub.example.org", "https://hub.example.org/path"])
def test_a_hub_that_is_not_an_address_is_refused(project, wrong):
    project.write(spec(hub=wrong))
    result = project.run("project", "check")
    assert result.returncode != 0 and "hub" in result.stdout


def test_a_dry_run_of_a_linked_project_runs_nothing(project, engine, egzo, fake_hub):
    project.write(document(engine, egzo, fake_hub))
    absent = "egzo-spec-absent:never-built"
    result = project.run("project", "up", "--dry-run", env={"EGZO_IMAGE": absent})
    assert result.returncode == 1 and "would create" in result.stdout, "exit 1: there are changes to make, and it could say which"
    assert "not up to date" in project.run("project", "check", env={"EGZO_IMAGE": absent}).stdout


def test_a_ca_that_is_not_certificates_is_refused(project):
    (project.root / "ca.pem").write_text("not a certificate")
    project.write(spec(hub={"url": "https://hub.example.org", "ca": "ca.pem"}))
    result = project.run("project", "check")
    assert result.returncode != 0 and "hub.ca" in result.stdout


def test_up_starts_the_link_says_it_is_not_registered_and_does_not_wait_for_anyone(linked, fake_hub, engine):
    # `up` waits five seconds for the hub's first answer: a slow engine may not have it by then, and says "connecting" instead
    assert "link: not registered: run `egzo project register`" in linked.up_output or "link: connecting" in linked.up_output
    assert eventually(lambda: "not registered" in row(linked, "link")["status"], seconds=60), "the hub said so, in the end"
    assert fake_hub.registration(linked.name) is None, "a key the hub does not know leaves nothing on it"

    linked.ok("agent spawn web early")  # the project runs without the hub
    linked.ok("agent rm --force early")


def test_the_link_has_a_network_of_its_own_the_socket_and_a_sidecars_limits_and_nothing_listens(linked, engine):
    link = link_of(engine, linked)
    assert link is not None
    host = link.raw["HostConfig"]
    assert list(link.raw["NetworkSettings"]["Networks"]) == [f"{linked.name}_link"]
    assert not link.raw["NetworkSettings"]["Ports"]
    dropped = set(host["CapDrop"])  # Podman lists the default capabilities it drops instead of ALL
    assert host["ReadonlyRootfs"] and ("ALL" in dropped or {"CAP_CHOWN", "CAP_KILL", "CAP_NET_BIND_SERVICE"} <= dropped)
    assert "no-new-privileges" in host["SecurityOpt"]
    if engine.rootless:  # root in the container is the user who owns the engine, which is who the socket belongs to
        assert link.raw["Config"]["User"].split(":")[0] in ("0", "root")
    else:
        assert link.raw["Config"]["User"].split(":")[0] == "65532", "not root: only the socket's group"
    assert any(m["Destination"] == "/run/engine.sock" for m in link.raw["Mounts"])
    assert any(m.get("Name") == f"{linked.name}_link" for m in link.raw["Mounts"])


def test_the_secret_key_is_never_in_the_links_settings_and_the_key_on_the_volume_is_sealed(linked, engine):
    link = link_of(engine, linked)
    assert not [e for e in link.raw["Config"]["Env"] if "SECRET_KEY" in e]
    sealed = engine.in_volumes({f"{linked.name}_link": "/l"}, "base64 -w0 /l/link.key")
    assert sealed.returncode == 0
    assert len(base64.b64decode(sealed.stdout)) > 32, "more than a bare Ed25519 seed: a sealed record"
    assert sealed.stdout.strip() != "", "the key is on the volume"


def test_a_project_that_is_not_registered_is_a_warning_and_the_link_stops_dialing(linked, fake_hub):
    assert eventually(lambda: "not registered" in row(linked, "link")["status"], seconds=60)
    result = check(linked)
    assert result.returncode == 0, "a project runs without its hub"
    assert "warn" in result.stdout and "not registered" in result.stdout
    dials = fake_hub.dials()
    time.sleep(3)
    assert fake_hub.dials() == dials, "the hub said no, and asking again changes nothing until a person does something"


def test_a_bad_code_is_refused_and_says_so_and_can_be_followed_by_a_good_one(linked, fake_hub):
    result = linked.run("project", "register", input="egzo-link-wrong\n")
    assert result.returncode != 0 and "did not take this code" in result.stderr
    assert fake_hub.registration(linked.name) is None
    assert "not registered" in row(linked, "link")["status"]


def test_register_with_no_code_at_all_is_refused_before_anything_is_sent(linked, fake_hub):
    dials = fake_hub.dials()
    result = linked.run("project", "register", input="\n")
    assert result.returncode != 0 and "not a link code" in result.stderr
    assert fake_hub.dials() == dials


def test_register_presents_the_code_once_and_the_project_is_approved(linked, fake_hub):
    code = fake_hub.code("alice")
    good = fake_hub.codes()
    said = linked.ok("project register", input=code + "\n").stdout
    assert "owned by alice" in said
    registration = fake_hub.registration(linked.name)
    assert registration["State"] == "approved"
    assert registration["Templates"] == ["typed", "web"], "the project says what it holds"
    assert registration["Host"]
    assert fake_hub.codes() == good - 1, "the code is spent"

    assert eventually(lambda: "owned by alice" in row(linked, "link")["status"])
    result = check(linked)
    assert result.returncode == 0 and "owned by alice" in result.stdout


def test_register_again_says_so_and_spends_nothing(linked, fake_hub):
    code = fake_hub.code("bob")
    good = fake_hub.codes()
    result = linked.run("project", "register", input=code + "\n")
    assert result.returncode != 0 and "registered already" in result.stderr
    assert fake_hub.codes() == good
    assert fake_hub.registration(linked.name)["Owner"] == "alice"


def test_the_code_is_in_no_setting_no_log_and_no_status(linked, fake_hub, engine):
    link = link_of(engine, linked)
    assert not [e for e in link.raw["Config"]["Env"] if "egzo-link-" in e]
    assert "egzo-link-" not in " ".join(link.raw["Config"]["Cmd"] or [])
    assert "egzo-link-" not in engine.run("logs", link.name).stdout + engine.run("logs", link.name).stderr
    assert "egzo-link-" not in linked.ok("ps --json").stdout


def test_the_hub_asks_and_the_link_does_every_verb(linked, fake_hub, engine):
    p, hub = linked.name, fake_hub
    assert hub.must(p, "published")["Templates"].keys() == {"web", "typed"}
    answer = hub.verb(p, "spawn", Template="web", Name="fromhub", Actor="user:7")
    assert answer["result"] == "fromhub" and answer["steps"], "progress comes as `egzo spawn` prints it"
    assert any(r["name"] == "fromhub" and r["actor"] == "user:7" for r in json.loads(linked.ok("ps --json").stdout))

    project = hub.must(p, "project")
    assert project["Name"] == p and [a["Name"] for a in project["Agents"]] == ["fromhub"]

    hub.must(p, "stop", Agent="fromhub")
    assert [a["State"] for a in hub.must(p, "project")["Agents"]] != ["running"]
    hub.must(p, "start", Agent="fromhub")
    assert [a["State"] for a in hub.must(p, "project")["Agents"]] == ["running"]

    assert "web" in hub.must(p, "spec")
    assert isinstance(hub.must(p, "logs", Container="control", Tail=5), str)
    assert isinstance(hub.must(p, "events", Limit=5), (list, type(None)))


def test_a_terminal_goes_through_the_link_both_ways(linked, fake_hub):
    output = fake_hub.must(linked.name, "attach", Agent="fromhub", Input="through the link\n", Seconds=3)
    assert "through the link" in output


def test_the_feeds_are_pushed(linked, fake_hub, engine):
    p, hub = linked.name, fake_hub
    assert eventually(lambda: hub.feed(p, "project")), "the project, at once"
    before = len(hub.feed(p, "project"))
    hub.must(p, "stop", Agent="fromhub")
    assert eventually(lambda: len(hub.feed(p, "project")) > before), "and each time it changes"
    hub.must(p, "start", Agent="fromhub")
    assert eventually(lambda: hub.feed(p, "status"))

    container = engine.instance(p, "fromhub").name
    engine.exec(container, "curl", "-sS", "-m", "5", "https://denied.example.com")
    assert eventually(lambda: [e for e in hub.feed(p, "audit") if e.get("Host", e.get("host")) == "denied.example.com"])


def message_events(hub, project):
    return [e for e in hub.feed(project, "events") if e["Type"] == "message"]


def test_the_hub_sends_a_message_for_one_of_its_users_and_sending_it_again_sends_nothing(linked, fake_hub):
    p, hub = linked.name, fake_hub
    first = hub.must(p, "send", Agent="fromhub", Actor="user:7", Text="hello from a chat", Key="chat-1")
    assert ID.match(first["ID"])
    again = hub.must(p, "send", Agent="fromhub", Actor="user:7", Text="hello from a chat", Key="chat-1")
    assert again["ID"] == first["ID"], "the key is the platform's message id: a retry is the same message"
    reused = hub.verb(p, "send", Agent="fromhub", Actor="user:7", Text="something else", Key="chat-1")["error"]
    assert "chat-1" in reused, "the same key with another text is a bridge's mistake: refused, never taken for the first message"
    listed = [r for r in table(linked.ok("message ls").stdout) if r["ID"] == first["ID"]]
    assert [(r["FROM"], r["TO"], r["KIND"]) for r in listed] == [("user:7", "agent:fromhub", "request")]

    assert "user:" in hub.verb(p, "send", Agent="fromhub", Actor="operator", Text="x", Key="chat-2")["error"], "the hub speaks for its users only"
    assert "user:" in hub.verb(p, "send", Agent="fromhub", Actor="agent:fromhub", Text="x", Key="chat-3")["error"]
    assert "nobody" in hub.verb(p, "send", Agent="nobody", Actor="user:7", Text="x", Key="chat-4")["error"]
    assert hub.verb(p, "send", Agent="fromhub", Actor="user:7", Text="x")["error"], "a send without a key could be sent twice"


def test_the_events_feed_carries_what_an_agent_says_to_a_user_and_the_user_answers_through_the_link(linked, fake_hub, engine):
    p, hub = linked.name, fake_hub
    agent = Agent(engine, linked, "fromhub")
    sent = hub.must(p, "send", Agent="fromhub", Actor="user:7", Text="deploy it?", Key="chat-10")["ID"]
    assert eventually(lambda: [e for e in message_events(hub, p) if e["ID"] == sent]), "what the user said is in the feed too"
    assert agent.get(sent)["text"] == "deploy it?"

    code, body = agent.call("POST", f"/v1/messages/{sent}/ask", {"text": "to staging or production?", "choices": ["staging", "production"]})
    assert code in (200, 201), body
    question = eventually(lambda: [e for e in message_events(hub, p) if e["Kind"] == "question" and e["Re"] == sent])
    assert question, "the question reaches the hub"
    q = question[0]
    assert (q["Agent"], q["Actor"], q["To"], q["Text"], q["Choices"]) == ("fromhub", "agent:fromhub", "user:7", "to staging or production?", ["staging", "production"])

    assert hub.verb(p, "resolve", Message=q["ID"], Actor="user:8", Text="production")["error"], "only the user it is addressed to answers it"
    assert hub.verb(p, "resolve", Message=q["ID"], Actor="operator", Text="production")["error"], "and never as the operator"
    first = hub.must(p, "resolve", Message=q["ID"], Actor="user:7", Text="staging", Key="click-1")
    assert ID.match(first["ID"]) and not first["Already"]
    answer = eventually(lambda: [e for e in message_events(hub, p) if e["Kind"] == "resolution" and e["Re"] == q["ID"]])
    assert answer and (answer[0]["ID"], answer[0]["Actor"], answer[0]["To"], answer[0]["Text"]) == (first["ID"], "user:7", "agent:fromhub", "staging")

    again = hub.must(p, "resolve", Message=q["ID"], Actor="user:7", Text="staging", Key="click-1")
    assert again == first, "the key is the platform's id for the click: a retry is the same answer, and it went through"
    second = hub.must(p, "resolve", Message=q["ID"], Actor="user:7", Text="production", Key="click-2")
    assert second == {"ID": first["ID"], "Already": True}, "a second answer changes nothing, and says the person answered already"
    assert "click-1" in hub.verb(p, "resolve", Message=q["ID"], Actor="user:7", Text="production", Key="click-1")["error"], \
        "the same key with another answer is a bridge's mistake"
    assert len([e for e in message_events(hub, p) if e["Kind"] == "resolution" and e["Re"] == q["ID"]]) == 1, "one answer, whatever came after"

    agent.resolve(sent, "deployed to staging")
    result = eventually(lambda: [e for e in message_events(hub, p) if e["Kind"] == "resolution" and e["Re"] == sent])
    assert result and (result[0]["To"], result[0]["Text"], result[0]["Outcome"]) == ("user:7", "deployed to staging", "done")


def from_egzo(project, agent):
    """The notices egzo sent an agent, oldest first: read from the project's events."""
    events = [json.loads(line) for line in project.run("events").stdout.splitlines() if line.startswith("{")]
    return [e for e in events if e["type"] == "message" and e.get("actor") == "egzo" and e["data"]["to"] == f"agent:{agent}"]


def closed_by_egzo(project):
    events = [json.loads(line) for line in project.run("events").stdout.splitlines() if line.startswith("{")]
    return {e["id"]: e["data"]["outcome"] for e in events if e["type"] == "resolved" and e.get("actor") == "egzo"}


def test_a_message_the_hub_could_not_deliver_is_told_to_the_agent_by_a_notice(linked, fake_hub, engine):
    p, hub = linked.name, fake_hub
    agent = Agent(engine, linked, "fromhub")
    request = hub.must(p, "send", Agent="fromhub", Actor="user:7", Text="deploy it?", Key="chat-40")["ID"]
    agent.get(request)
    before = len(from_egzo(linked, "fromhub"))

    # an update is a progress line: the question or the result that ends the request tells
    code, body = agent.call("POST", f"/v1/messages/{request}/update", {"text": "building"})
    assert code == 201, body
    hub.must(p, "undelivered", Message=body["id"], Text="user 7 cannot be reached on Discord")
    assert len(from_egzo(linked, "fromhub")) == before, "an update gets no notice"

    # a question nobody will see is closed, and the agent is told
    code, body = agent.call("POST", f"/v1/messages/{request}/ask", {"text": "to staging or production?", "choices": ["staging", "production"]})
    assert code == 201, body
    question = body["id"]
    hub.must(p, "undelivered", Message=question, Text="user 7 cannot be reached on Discord")
    (notice,) = from_egzo(linked, "fromhub")[before:]
    text = notice["text"]
    assert notice["data"]["kind"] == "request", "a notice is a request the agent resolves once read"
    for said in (question, "user:7", "user 7 cannot be reached on Discord", "to staging or production?", "message"):
        assert said in text, f"{said!r} not in {text!r}"
    assert closed_by_egzo(linked).get(question) == "failed", "the agent does not wait for an answer that cannot come"
    hub.must(p, "undelivered", Message=question, Text="user 7 cannot be reached on Discord")
    assert len(from_egzo(linked, "fromhub")) == before + 1, "told twice, it is one notice"

    # a result nobody saw stays the result: the agent is told, and sends it again if it matters
    assert agent.resolve(request, "deployed to staging")[0] == 204
    result = eventually(lambda: [e for e in message_events(hub, p) if e["Kind"] == "resolution" and e["Re"] == request])[0]["ID"]
    hub.must(p, "undelivered", Message=result, Text="user 7 cannot be reached on Discord")
    assert result in from_egzo(linked, "fromhub")[-1]["text"]
    assert result not in closed_by_egzo(linked) and request not in closed_by_egzo(linked), "the request stays resolved, as the agent closed it"

    # only an agent's message to a user, that exists, with a reason
    assert hub.verb(p, "undelivered", Message=request, Text="x")["error"], "what a user said is not the agent's to resend"
    assert hub.verb(p, "undelivered", Message="m" + "0" * 32, Text="x")["error"]
    assert hub.verb(p, "undelivered", Message=question, Text="")["error"], "a notice says why"
    code, body = agent.send("operator", "a word for the operator")
    assert code == 201, body
    assert hub.verb(p, "undelivered", Message=body["id"], Text="x")["error"], "the hub delivers to its users only"


def test_the_events_feed_never_carries_what_agents_say_to_each_other_or_the_operator(linked, fake_hub, engine):
    p, hub = linked.name, fake_hub
    hub.must(p, "spawn", Template="web", Name="peer", Actor="user:7")
    agent = Agent(engine, linked, "fromhub")
    assert agent.send("agent:peer", "a secret between agents")[0] in (200, 201)
    assert agent.send("operator", "a word for the operator")[0] in (200, 201)
    marker = hub.must(p, "send", Agent="fromhub", Actor="user:7", Text="after them", Key="chat-20")["ID"]
    assert eventually(lambda: [e for e in message_events(hub, p) if e["ID"] == marker])
    for event in hub.feed(p, "events"):
        assert "a secret between agents" not in json.dumps(event) and "a word for the operator" not in json.dumps(event)
    between = [e for e in message_events(hub, p) if e.get("To") == "agent:peer"]
    assert between and not between[0]["Text"], "that a message went is said, never what it says"
    hub.must(p, "remove", Agent="peer")


def test_the_events_feed_resumes_where_the_hub_is_after_the_connection_drops(linked, fake_hub):
    p, hub = linked.name, fake_hub
    before = fake_hub.registration(p)["Connections"]
    hub.drop(p)
    assert eventually(lambda: fake_hub.registration(p)["Connections"] > before)
    marker = eventually(lambda: hub.verb(p, "send", Agent="fromhub", Actor="user:7", Text="after the drop", Key="chat-30").get("result"))
    assert eventually(lambda: [e for e in message_events(hub, p) if e["ID"] == marker["ID"]])
    seqs = [e["Seq"] for e in hub.feed(p, "events")]
    assert seqs == sorted(set(seqs)), "nothing twice, nothing out of order"


def env_of(engine, project, name):
    return dict(e.split("=", 1) for e in engine.instance(project.name, name).raw["Config"]["Env"])


def you(agent):
    """What the agents tool says of the agent itself."""
    return agent.tool("agents")["structuredContent"]["you"]


def screen(project, name, seconds=5):
    """An agent's terminal as `agent attach --read-only` shows it, read for a few seconds. The reporting stand-in prints `got:` and
    each line typed into it. Control keeps no prompt in the event log but its own announcements: a task is free text."""
    import pexpect

    client = project.pty("agent attach --read-only", name)
    try:
        client.expect(pexpect.TIMEOUT, timeout=seconds)
        return client.before
    finally:
        client.close(force=True)


def starter_notices(project, agent):
    return [e for e in from_egzo(project, agent) if "reached" in e["text"]]


def test_an_agent_knows_who_started_it_and_is_told_when_they_can_and_cannot_be_reached(linked, fake_hub, engine):
    p, hub = linked.name, fake_hub
    hub.must(p, "spawn", Template="web", Name="started", Actor="user:7", StarterName="Joe")
    env = env_of(engine, linked, "started")
    assert (env["EGZO_STARTER"], env["EGZO_STARTER_NAME"]) == ("user:7", "Joe"), "its instructions say who started it"
    assert "EGZO_UNATTENDED" not in env and "EGZO_TASK" not in env
    agent = Agent(engine, linked, "started")
    assert you(agent)["started_by"] == {"id": "user:7", "name": "Joe"}, "whether Joe can be reached is not known until the hub says"

    hub.must(p, "people", People={"user:7": {"Name": "Joe", "Reachable": True}})
    assert eventually(lambda: you(agent)["started_by"].get("reachable") is True)
    hub.must(p, "people", People={"user:7": {"Name": "Joe", "Reachable": True}})
    assert not starter_notices(linked, "started"), "the first word on a person, and the same again, are no news"

    hub.must(p, "people", People={"user:7": {"Name": "Joe", "Reachable": False}})
    gone = eventually(lambda: starter_notices(linked, "started"))
    assert len(gone) == 1 and "Joe" in gone[0]["text"] and "can't be reached" in gone[0]["text"]
    assert you(agent)["started_by"] == {"id": "user:7", "name": "Joe", "reachable": False}
    hub.must(p, "people", People={"user:7": {"Name": "Joe", "Reachable": False}})
    hub.must(p, "people", People={"user:7": {"Name": "Joe", "Reachable": True}})
    back = eventually(lambda: len(starter_notices(linked, "started")) == 2 and starter_notices(linked, "started"))
    assert "can be reached again" in back[1]["text"]
    assert len([e for e in starter_notices(linked, "started")]) == 2, "told once each way"

    hub.must(p, "people", People={"user:7": {"Name": "Joe Smith", "Reachable": True}})
    assert eventually(lambda: you(agent)["started_by"]["name"] == "Joe Smith"), "the name the hub says now"
    assert hub.verb(p, "people", People={"operator": {"Name": "x", "Reachable": True}})["error"], "the hub speaks of its users only"
    hub.must(p, "remove", Agent="started")


def test_an_agent_started_at_the_cli_was_started_by_the_operator(linked, engine):
    linked.ok("agent spawn web by-hand")
    env = env_of(engine, linked, "by-hand")
    assert env["EGZO_STARTER"] == "operator" and "EGZO_STARTER_NAME" not in env
    assert you(Agent(engine, linked, "by-hand"))["started_by"] == {"id": "operator"}
    linked.ok("agent rm --force by-hand")


def test_a_task_is_typed_once_as_the_first_prompt_and_sends_no_message(linked, fake_hub, engine):
    p, hub = linked.name, fake_hub
    hub.must(p, "spawn", Template="typed", Name="tasked", Actor="user:7", StarterName="Joe", Task="list the open issues")
    env = env_of(engine, linked, "tasked")
    assert "EGZO_UNATTENDED" not in env and "list the open issues" not in json.dumps(env), "the task is typed, never in the settings"
    assert eventually(lambda: screen(linked, "tasked").count(b"got:list the open issues") == 1, seconds=60), screen(linked, "tasked")
    assert not [e for e in message_events(hub, p) if e["Agent"] == "tasked" and e["Kind"] == "request"], "nothing goes to chat"

    linked.ok("stop tasked")
    linked.ok("start tasked")
    assert b"got:list the open issues" not in screen(linked, "tasked", 10), "typed once, not again after a restart"
    hub.must(p, "remove", Agent="tasked")


def test_an_unattended_agent_gets_its_task_as_a_request_from_who_started_it_and_answers_them(linked, fake_hub, engine):
    p, hub = linked.name, fake_hub
    hub.must(p, "spawn", Template="typed", Name="alone", Actor="user:7", StarterName="Joe", Task="list the open issues", Unattended=True)
    assert env_of(engine, linked, "alone")["EGZO_UNATTENDED"] == "1", "its instructions say nobody reads its terminal"
    task = eventually(lambda: [e for e in message_events(hub, p) if e["Agent"] == "alone" and e["Kind"] == "request"])
    assert len(task) == 1 and (task[0]["Actor"], task[0]["To"], task[0]["Text"]) == ("user:7", "agent:alone", "list the open issues")
    answer = eventually(lambda: [e for e in message_events(hub, p) if e["Kind"] == "resolution" and e["Re"] == task[0]["ID"]], seconds=60)
    assert answer and answer[0]["To"] == "user:7", "the agent answers the person, who reads it on a chat platform"
    assert b"got:list the open issues" not in screen(linked, "alone"), "the task is never typed: the agent fetches it"

    assert hub.verb(p, "spawn", Template="typed", Name="no-task", Actor="user:7", Unattended=True)["error"], "unattended needs a task"
    assert hub.verb(p, "spawn", Template="typed", Name="no-user", Actor="operator", Task="x", Unattended=True)["error"], \
        "an unattended agent answers a hub user"
    assert not [n for n in linked.instance_names() if n in ("no-task", "no-user")], "a refused spawn spawns nothing"
    hub.must(p, "remove", Agent="alone")


def test_the_hub_can_ask_nothing_else(linked, fake_hub):
    assert "no verb" in fake_hub.verb(linked.name, "exec")["error"]
    assert "no agent" in fake_hub.verb(linked.name, "stop", Agent="nobody")["error"]


def test_an_agent_cannot_reach_the_link(linked, engine):
    link = link_of(engine, linked)
    address = list(link.raw["NetworkSettings"]["Networks"].values())[0]["IPAddress"]
    container = engine.instance(linked.name, "fromhub")
    assert f"{linked.name}_link" not in container.raw["NetworkSettings"]["Networks"]
    assert engine.exec(container.name, "ping", "-c", "1", "-W", "2", address).returncode != 0


def test_a_dropped_connection_comes_back_by_itself(linked, fake_hub):
    before = fake_hub.registration(linked.name)["Connections"]
    fake_hub.drop(linked.name)
    assert eventually(lambda: fake_hub.registration(linked.name)["Connections"] > before)
    assert eventually(lambda: fake_hub.verb(linked.name, "project").get("error") is None)


def test_up_again_changes_nothing_and_recreating_the_link_keeps_its_key(linked, fake_hub):
    key = fake_hub.registration(linked.name)["Key"]
    assert "nothing to do" in linked.ok("project up").stdout
    assert "nothing to do" in linked.ok("project up --dry-run").stdout, "a dry run sees the link as it is"
    linked.ok("project up --recreate")
    assert eventually(lambda: fake_hub.registration(linked.name)["Online"])
    assert fake_hub.registration(linked.name)["Key"] == key
    assert eventually(lambda: "owned by alice" in row(linked, "link")["status"])


def test_revoked_ends_what_the_hub_may_do_and_check_fails(linked, fake_hub):
    fake_hub.revoke(linked.name, "the laptop was stolen")
    assert eventually(lambda: "revoked" in row(linked, "link")["status"])
    assert "the laptop was stolen" in row(linked, "link")["status"]
    assert fake_hub.registration(linked.name)["Online"] is False, "nothing of a revoked project stays connected"
    result = check(linked)
    assert result.returncode != 0 and "revoked" in result.stdout


def test_a_revoked_link_stops_dialing_and_a_code_does_not_help_until_an_operator_forgets_it(linked, fake_hub):
    dials = fake_hub.dials()
    time.sleep(3)
    assert fake_hub.dials() == dials
    code = fake_hub.code("alice")
    good = fake_hub.codes()
    result = linked.run("project", "register", input=code + "\n")
    assert result.returncode != 0 and "forget this project" in result.stderr
    assert fake_hub.codes() == good, "a revoked key spends nothing"

    before = fake_hub.registration(linked.name)
    fake_hub.forget(linked.name)
    said = linked.ok("project register", input=code + "\n").stdout
    assert "owned by alice" in said, "the way back for a link that was revoked by mistake"
    after = fake_hub.registration(linked.name)
    assert after["Key"] == before["Key"]
    fake_hub.revoke(linked.name)
    assert eventually(lambda: "revoked" in row(linked, "link")["status"])


def test_removing_hub_from_the_file_unlinks_and_removes_the_link(linked, fake_hub, engine, egzo):
    document_without = spec(agents={"web": agent(harness="custom", image=engine.session_image(egzo.binary), env={"FAKE_TUI": "cat"})})
    linked.ok("agent rm --force fromhub")
    linked.write(document_without)
    said = linked.ok("project up").stdout
    assert "the hub revoked this project" in said, "and it says what happened, not that the hub forgot it"
    assert linked.name not in fake_hub.forgotten(), "a revoked project cannot erase its own record"
    assert fake_hub.registration(linked.name)["State"] == "revoked"
    assert link_of(engine, linked) is None
    assert not engine.volume_exists(f"{linked.name}_link")
    assert engine.run("network", "inspect", f"{linked.name}_link").returncode != 0
    assert [c for c in engine.containers(linked.name) if c.name.endswith("-control-1")]


def test_down_tells_the_hub(live_project, engine, egzo, fake_hub):
    live_project.write(document(engine, egzo, fake_hub))
    live_project.ok("project up")
    register(live_project, fake_hub)
    said = live_project.ok("project down --force").stdout
    assert "forgotten" in said
    assert live_project.name in fake_hub.forgotten()
    assert not engine.containers(live_project.name)


def test_down_of_a_project_that_was_never_registered_says_so_at_once(live_project, engine, egzo, fake_hub):
    live_project.write(document(engine, egzo, fake_hub))
    live_project.ok("project up")
    began = time.time()
    said = live_project.ok("project down --force").stdout
    assert "was not registered" in said and "not told" not in said
    assert time.time() - began < 60
    assert live_project.name not in fake_hub.forgotten()


def test_register_needs_a_hub_and_a_running_link(project, engine, egzo, fake_hub):
    project.write(spec(agents={"web": agent(harness="custom", image=engine.session_image(egzo.binary), env={"FAKE_TUI": "cat"})}))
    result = project.run("project", "register", input="egzo-link-x\n")
    assert result.returncode != 0 and "names no hub" in result.stderr
    project.write(document(engine, egzo, fake_hub))
    result = project.run("project", "register", input="egzo-link-x\n")
    assert result.returncode != 0 and "project up" in result.stderr


def test_a_hub_that_is_away_is_said_and_the_project_keeps_running(live_project, engine, egzo, tmp_path):
    away = FakeHub(tmp_path / "away", hub_host(engine))
    live_project.write(document(engine, egzo, away))
    live_project.ok("project up")
    register(live_project, away)
    away.stop()
    assert eventually(lambda: "unreachable" in row(live_project, "link")["status"])
    result = check(live_project)
    assert result.returncode == 0 and "unreachable" in result.stdout, "the project runs without the hub"
    live_project.ok("agent spawn web still")
    assert engine.instance(live_project.name, "still")


def test_a_key_sealed_with_another_secret_key_is_refused_and_says_so(live_project, engine, egzo, fake_hub):
    live_project.write(document(engine, egzo, fake_hub))
    live_project.ok("project up")
    register(live_project, fake_hub)
    other = "b" * 64
    result = live_project.run("project", "up", "--recreate", env={SECRET_KEY_VARIABLE: other})
    assert result.returncode != 0 and "another secret key" in result.stderr


def test_rotate_ca_moves_the_links_key_to_the_new_secret_key(live_project, engine, egzo, fake_hub):
    live_project.write(document(engine, egzo, fake_hub))
    live_project.ok("project up")
    register(live_project, fake_hub)
    key = fake_hub.registration(live_project.name)["Key"]
    other = "b" * 64
    live_project.ok("project rotate-ca", env={SECRET_KEY_VARIABLE: other}, timeout=300)
    live_project.ok("project up", env={SECRET_KEY_VARIABLE: other})
    link = f"{live_project.name}-link-1"
    assert engine.run("restart", link).returncode == 0
    assert eventually(lambda: "locked" in row(live_project, "link")["status"])
    live_project.ok("project up", env={SECRET_KEY_VARIABLE: other})
    assert eventually(lambda: fake_hub.registration(live_project.name)["Online"])
    assert fake_hub.registration(live_project.name)["Key"] == key


def test_after_a_reboot_up_brings_the_project_back_with_the_same_key_and_nobody_registers_again(live_project, engine, egzo, fake_hub):
    """`up` is required after a restart. Who runs it is outside egzo."""
    p = live_project
    p.write(document(engine, egzo, fake_hub))
    p.ok("project up")
    register(p, fake_hub)
    p.ok("agent spawn web worker")
    registered = fake_hub.registration(p.name)
    codes = fake_hub.codes()

    assert engine.run("stop", *[c.name for c in engine.containers(p.name)]).returncode == 0  # what a reboot leaves
    assert eventually(lambda: not fake_hub.registration(p.name)["Online"]), "the hub sees it go"

    p.ok("project up")
    assert eventually(lambda: "owned by alice" in row(p, "link")["status"])
    assert fake_hub.registration(p.name)["Key"] == registered["Key"]
    assert fake_hub.registration(p.name)["ID"] == registered["ID"]
    assert fake_hub.codes() == codes, "nobody needed one"

    assert eventually(lambda: fake_hub.verb(p.name, "project").get("error") is None), "the verbs are back"
    fake_hub.must(p.name, "start", Agent="worker")
    container = engine.instance(p.name, "worker").name
    denied = lambda: engine.exec(container, "curl", "-s", "-m", "5", "-o", "/dev/null", "-w", "%{http_connect}", "https://denied.example.com").stdout
    assert eventually(lambda: denied() == "403"), f"the proxy has its policy again and refuses by it, not with a 503: {denied()}"


def test_a_link_that_restarted_alone_is_locked_and_says_what_opens_it(live_project, engine, egzo, fake_hub):
    p = live_project
    p.write(document(engine, egzo, fake_hub))
    p.ok("project up")
    link = f"{p.name}-link-1"
    assert link_of(engine, p).raw["HostConfig"]["RestartPolicy"]["Name"] == "unless-stopped"
    started = engine.inspect(link)["State"]["StartedAt"]
    engine.exec(link, "kill", "-TERM", "1")  # the process ends: the engine starts it again, without the secret key
    assert eventually(lambda: engine.inspect(link)["State"]["StartedAt"] != started and engine.inspect(link)["State"]["Running"])
    assert eventually(lambda: "locked" in row(p, "link")["status"])
    assert "egzo project up" in row(p, "link")["status"]
    assert "locked" in check(p).stdout
    result = p.run("project", "register", input=fake_hub.code() + "\n")
    assert result.returncode != 0 and "locked" in result.stderr, "the link holds no key to present a code with"
    p.ok("project up")
    assert eventually(lambda: "not registered" in row(p, "link")["status"])
