# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""`egzo agent spawn`: an `agents:` entry is a template, and spawn makes an instance of the template `project up` last published,
with a network, a home volume and a container of its own. An instance is addressed by its name everywhere (`attach`, `message
send`, `logs`, `exec`, `start`, `stop`). The life of an instance after spawn (`ps`, `rm`, stale ones) is in
test_agent_lifecycle.py and test_agent_stale.py.

One project is up for the whole module (`sandbox`): `coder` (an env and a prompt), `reviewer`, `broken` (an image that does not exist)
and `tui` (the fake TUI of the session specs). Every spec starts from no instance."""

import json
import os
import re
import subprocess
import time

import pexpect
import pytest

from agentapi import Agent
from conftest import LABEL_PREFIX
from support import agent, custom, spec

CTRL_RIGHT_BRACKET = "\x1d"
BAD_NAMES = ["Upper", "has space", "-leading", "_leading", "a.b", "x" * 64, "a/b", "--flag"]
RESERVED_NAMES = ["coder", "reviewer", "control", "proxy", "prep", "control-1", "proxy-1"]


class Sandbox:
    def __init__(self, project, engine, image, session_image):
        self.project = project
        self.engine = engine
        (project.root / "prompt.md").write_text("be brief\n")
        self.document = spec(agents={
            "coder": custom(image, env={"WHICH": "published"}, prompt="./prompt.md"),
            "reviewer": custom(image),
            "broken": custom("egzo-spec-no-such-image:0"),
            "tui": agent(harness="custom", image=session_image, env={"FAKE_TUI": "cat"}),
        })
        project.up(self.document)

    def restore_file(self):
        self.project.write(self.document)

    def env(self, name):
        return dict(item.split("=", 1) for item in self.engine.instance(self.project.name, name).raw["Config"]["Env"])

    def messages_to(self, name, *args):
        from support import table

        return [row for row in table(self.project.ok("message ls --all", *args).stdout) if row["TO"] == f"agent:{name}"]


@pytest.fixture(scope="module")
def sandbox(shared_project, engine, egzo):
    return Sandbox(shared_project, engine, engine.agent_image(), engine.session_image(egzo.binary))


@pytest.fixture(autouse=True)
def start_from_no_instance(sandbox):
    sandbox.project.retire()
    sandbox.restore_file()
    yield
    sandbox.restore_file()


def popen(project, *args):
    environment = {**os.environ, **project.env, "NO_COLOR": "1"}
    environment.pop("EGZO_PROJECT_NAME", None)
    return subprocess.Popen([project.egzo.binary, *args], cwd=project.root, env=environment, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)


def wait_for(check, what, timeout=60):
    deadline = time.time() + timeout
    while time.time() < deadline:
        value = check()
        if value:
            return value
        time.sleep(0.3)
    raise AssertionError(f"timed out waiting for {what}")


def test_an_instance_is_a_running_container_with_its_own_network_and_its_own_identity(sandbox, engine):
    p = sandbox.project
    result = p.ok("agent spawn --json coder issue-1", timeout=300)
    assert json.loads(result.stdout) == {"name": "issue-1", "service": "coder", "container": f"{p.name}-issue-1", "actor": "operator"}
    assert result.stderr.strip(), "spawn says what it is doing, on stderr"
    plain = p.ok("agent spawn coder", timeout=300)
    assert plain.stdout == "coder-1\n", "stdout is the name and nothing else"

    container = engine.instance(p.name, "issue-1")
    assert container.name == f"{p.name}-issue-1" and container.raw["State"]["Running"] is True
    assert container.labels[f"{LABEL_PREFIX}service"] == "coder"
    assert ("network", f"{p.name}_issue-1") in {(r.kind, r.name) for r in engine.resources(p.name)}
    assert set(container.raw["NetworkSettings"]["Networks"]) == {f"{p.name}_issue-1"}
    for service in ("control", "proxy"):
        sidecar = next(r for r in engine.containers(p.name) if r.labels.get(f"{LABEL_PREFIX}service") == service)
        assert {f"{p.name}_issue-1", f"{p.name}_coder-1"} <= set(sidecar.raw["NetworkSettings"]["Networks"]), service

    env = sandbox.env("issue-1")
    assert env["EGZO_AGENT"] == "issue-1" and env["HTTPS_PROXY"].startswith("http://issue-1:")
    assert re.fullmatch(r"[0-9a-f]{64}", env["EGZO_TOKEN"]) and env["EGZO_TOKEN"] != sandbox.env("coder-1")["EGZO_TOKEN"]
    other = Agent(engine, p, "issue-1").exec('curl -sS -m 10 -o /dev/null -w "%{http_code}" -u "coder-1:$EGZO_TOKEN" "$EGZO_CONTROL_URL/v1/messages"')
    assert other.stdout == "401", "one instance's token must not work as another's"


def test_names_count_up_from_the_template_name_and_a_free_number_is_reused(sandbox):
    p = sandbox.project
    assert [p.spawn("coder"), p.spawn("coder"), p.spawn("reviewer")] == ["coder-1", "coder-2", "reviewer-1"]
    p.ok("agent rm --force coder-1")
    assert p.spawn("coder") == "coder-1", "an auto name takes the first free number"


def test_the_first_message_is_sent_to_the_new_instance_and_a_name_that_exists_exits_17(sandbox, engine):
    p = sandbox.project
    sandbox.project.spawn("reviewer", "quiet")
    assert sandbox.messages_to("quiet") == [], "without a message nothing is sent"

    result = p.ok("agent spawn coder issue-412 -m", "Fix #412, then open a PR", timeout=300)
    assert result.stdout == "issue-412\n"
    [row] = sandbox.messages_to("issue-412")
    assert (row["FROM"], row["KIND"]) == ("operator", "request") and "Fix #412" in row["TEXT"]

    before = {c.name: c.raw["Id"] for c in engine.containers(p.name)}
    again = p.sh("agent spawn coder issue-412 -m", "a second task", timeout=300)
    assert again.returncode == 17 and "issue-412" in again.stderr and "coder" in again.stderr and again.stdout == ""
    assert {c.name: c.raw["Id"] for c in engine.containers(p.name)} == before
    assert len(sandbox.messages_to("issue-412")) == 1, "no message is sent to an instance that already exists"

    other = p.sh("agent spawn reviewer issue-412")
    assert other.returncode == 17 and "coder" in other.stderr, "the refusal names the template that has the name"
    assert engine.instance(p.name, "issue-412").labels[f"{LABEL_PREFIX}service"] == "coder"


def test_spawn_uses_what_up_published_not_the_file_and_not_the_spawning_machine(sandbox, engine):
    p = sandbox.project
    p.write("# nothing here any more\n")
    assert p.spawn("coder") == "coder-1", "spawn does not need the file"
    env = sandbox.env("coder-1")
    assert env["WHICH"] == "published" and env["EGZO_PROMPT"] == "be brief\n", "the template carries the prompt"
    assert "EGZO_PROMPT_FILE" not in env

    p.ok("agent spawn coder", env={"SPAWNER_ONLY_VARIABLE": "leak"}, timeout=300)
    assert not [e for e in engine.instance(p.name, "coder-2").raw["Config"]["Env"] if e.startswith("SPAWNER_ONLY_VARIABLE")]

    # edited but not published: spawn still uses the published template, and warns that the new instance is stale at once
    document = json.loads(json.dumps(sandbox.document))
    document["agents"]["coder"]["env"] = {"WHICH": "edited"}
    document["agents"]["added"] = custom("x:1")
    p.write(document)
    warned = p.ok("agent spawn coder", timeout=300)
    assert warned.stdout.strip() == "coder-3" and sandbox.env("coder-3")["WHICH"] == "published"
    assert "warning" in warned.stderr and "egzo project up" in warned.stderr and "coder-3" in warned.stderr
    assert p.ps()["coder-3"]["STALE"] == "stale"

    added = p.sh("agent spawn added")
    assert added.returncode != 0 and "added" in added.stderr and "egzo project up" in added.stderr, "a template not yet published"
    unknown = p.sh("agent spawn nobody")
    assert unknown.returncode != 0 and all(name in unknown.stderr for name in ("nobody", "coder", "reviewer"))


def test_spawn_refuses_what_it_should_and_creates_nothing(sandbox, engine):
    p = sandbox.project
    for name in BAD_NAMES:  # some look like flags, so these go through `run`, which does not check the command line
        result = p.run("agent", "spawn", "coder", name)
        assert result.returncode not in (0, 17), name
    for name in RESERVED_NAMES:
        result = p.sh("agent spawn coder", name)
        assert result.returncode not in (0, 17) and name in result.stderr, name
    egress = p.sh("agent spawn coder egress")
    assert egress.returncode == 1 and "egress" in egress.stderr, "the name of the proxy's network"

    needs = p.sh("agent spawn coder --wait")
    assert needs.returncode != 0 and ("-m" in needs.stderr or "message" in needs.stderr)
    assert p.sh("agent spawn coder -m x --wait --attach").returncode != 0
    for flags in ("--json --wait -m x", "--json --attach"):
        combined = p.sh(f"agent spawn coder {flags}")
        assert combined.returncode == 1 and "--json" in combined.stderr
    assert engine.instances(p.name) == []

    for command in ("message send coder hi", "agent attach coder", "agent rm coder"):
        result = p.sh(command, input="")
        assert result.returncode != 0 and "coder is a template" in result.stderr and "egzo agent spawn coder" in result.stderr, command

    failed = p.sh("agent spawn broken attempt", timeout=300)
    assert failed.returncode != 0 and "egzo-spec-no-such-image" in failed.stderr
    assert engine.instances(p.name) == [] and not [r for r in engine.resources(p.name) if "attempt" in r.name]
    assert p.spawn("coder", "attempt") == "attempt", "a failed spawn leaves the name free"
    assert p.sh("agent spawn coder notes").returncode == 0, "an ordinary name is fine"


def test_spawn_never_starts_the_infrastructure(sandbox, engine):
    p = sandbox.project
    p.ok("stop proxy")
    try:
        result = p.sh("agent spawn coder")
        assert result.returncode != 0 and "proxy" in result.stderr and "egzo project up" in result.stderr
        assert engine.instances(p.name) == []
    finally:
        p.ok("start proxy", timeout=300)


def test_spawn_needs_the_project_to_be_up(live_project, engine, agent_image):
    live_project.write(spec(agents={"coder": custom(agent_image)}))
    before_up = live_project.sh("agent spawn coder")
    assert before_up.returncode != 0 and "egzo project up" in before_up.stderr
    assert engine.resources(live_project.name) == [], "a refused spawn creates nothing"
    live_project.up()
    live_project.ok("project down --force")
    after_down = live_project.sh("agent spawn coder")
    assert after_down.returncode != 0 and "egzo project up" in after_down.stderr and engine.instances(live_project.name) == []


def test_spawns_started_together_take_turns(sandbox, engine):
    p = sandbox.project
    race = [popen(p, "agent", "spawn", "coder", "race") for _ in range(2)]
    batch = [popen(p, "agent", "spawn", "coder", f"batch-{n}") for n in range(3)]
    for process in race + batch:
        process.communicate(timeout=300)
    assert sorted(process.returncode for process in race) == [0, 17], "one name at once makes exactly one instance"
    assert [process.returncode for process in batch] == [0, 0, 0]
    assert {i.labels[f"{LABEL_PREFIX}instance"] for i in engine.instances(p.name)} == {"race", "batch-0", "batch-1", "batch-2"}
    assert engine.instance(p.name, "race").raw["State"]["Running"] is True


@pytest.mark.parametrize("outcome,code", [("done", 0), ("declined", 3), ("failed", 4)])
def test_wait_returns_the_resolution_of_the_first_message_like_send_wait(sandbox, engine, outcome, code):
    p = sandbox.project
    name = f"job-{outcome}"
    waiting = popen(p, "agent", "spawn", "coder", name, "-m", "do the task", "--wait", "--timeout", "90s")
    wait_for(lambda: engine.instance(p.name, name) and sandbox.messages_to(name), "the first message")
    message_id = sandbox.messages_to(name)[0]["ID"]
    job = Agent(engine, p, name)
    assert job.call("GET", f"/v1/messages/{message_id}")[0] == 200
    assert job.resolve(message_id, "the answer is 42", outcome)[0] in (200, 204)
    out, err = waiting.communicate(timeout=60)
    assert waiting.returncode == code, (out, err)
    assert "the answer is 42" in out and name in err and name not in out


def test_wait_times_out_with_exit_5_and_leaves_the_instance_and_the_message_open(sandbox, engine):
    p = sandbox.project
    result = p.sh("agent spawn coder slow -m", "never answered", "--wait", "--timeout", "4s", timeout=300)
    assert result.returncode == 5, (result.stdout, result.stderr)
    assert engine.instance(p.name, "slow").raw["State"]["Running"] is True
    assert any("never answered" in row["TEXT"] for row in sandbox.messages_to("slow"))


def test_attach_opens_the_terminal_of_the_new_instance_and_delivers_the_first_message(sandbox, engine):
    p = sandbox.project
    client = p.pty("agent spawn tui pair -m", "please look at this", "--attach", timeout=120)
    client.expect(b"READY")
    client.send(b"typed in the new instance")
    client.expect(b"typed in the new instance")
    client.send(CTRL_RIGHT_BRACKET)
    client.expect(pexpect.EOF)
    client.close()
    assert client.exitstatus == 0
    assert engine.instance(p.name, "pair").raw["State"]["Running"] is True, "detaching leaves the instance running"
    assert [row for row in sandbox.messages_to("pair") if "please look at this" in row["TEXT"]]
