"""Templates and spawn: `agents:` entries are templates, `egzo spawn` makes an instance of one.

`egzo up` makes the infrastructure and publishes the templates; it starts no agent. `egzo spawn TEMPLATE [NAME]`
makes an instance from the template `up` last published, with a network, a home volume and a container of its
own. An instance is addressed by its name everywhere (`attach`, `send`, `logs`, `exec`, `start`, `stop`).
The life cycle after spawn (ps, rm, prune, stale instances) is in test_instances.py.
"""

import json
import os
import re
import subprocess
import time

import pexpect
import pytest

from conftest import LABEL_PREFIX
from support import agent, spec, table

pytestmark = pytest.mark.usefixtures("engine")

CTRL_RIGHT_BRACKET = "\x1d"


def custom(image, **fields):
    return agent(harness="custom", image=image, **fields)


def project_with(project, image, **templates):
    """`up` with the named templates (default: one called `coder`)."""
    templates = templates or {"coder": custom(image)}
    project.up(spec(agents=templates))
    return project


def messages(project, *args):
    result = project.run("messages", *args)
    assert result.returncode == 0, result.stderr
    return {row["ID"]: row for row in table(result.stdout)}


def in_instance(engine, project, name, command):
    return engine.exec(engine.instance(project.name, name).name, "sh", "-c", command)


def as_agent(engine, project, name, verb, path, body=None):
    """Call the control sidecar from inside an instance with its own credentials, the way a harness would."""
    command = f'curl -sS -m 10 -w "\\n%{{http_code}}" -u "$EGZO_AGENT:$EGZO_TOKEN" -X {verb} "$EGZO_CONTROL_URL{path}"'
    if body is not None:
        command += f" -H 'content-type: application/json' -d '{json.dumps(body)}'"
    out = in_instance(engine, project, name, command).stdout
    text, _, code = out.rpartition("\n")
    return int(code or 0), text


def wait_for(check, what, timeout=60):
    deadline = time.time() + timeout
    while time.time() < deadline:
        value = check()
        if value:
            return value
        time.sleep(0.3)
    raise AssertionError(f"timed out waiting for {what}")


# --- what spawn makes ---------------------------------------------------------------------------------------


def test_spawn_prints_the_instance_name_and_only_that_on_stdout(live_project, engine, agent_image):
    project_with(live_project, agent_image)
    result = live_project.run("spawn", "coder", timeout=300)
    assert result.returncode == 0, result.stderr
    assert result.stdout == "coder-1\n"


def test_an_instance_is_a_running_container_with_its_own_network(live_project, engine, agent_image):
    project_with(live_project, agent_image)
    name = live_project.spawn("coder")
    container = engine.instance(live_project.name, name)
    assert container.name == f"{live_project.name}-{name}"
    assert container.raw["State"]["Running"] is True
    names = {(r.kind, r.name) for r in engine.resources(live_project.name)}
    assert ("network", f"{live_project.name}_{name}") in names
    networks = set(container.raw["NetworkSettings"]["Networks"])
    assert networks == {f"{live_project.name}_{name}"}


def test_auto_names_count_up_from_the_template_name(live_project, engine, agent_image):
    project_with(live_project, agent_image, coder=custom(agent_image), reviewer=custom(agent_image))
    assert [live_project.spawn("coder"), live_project.spawn("coder"), live_project.spawn("reviewer")] == [
        "coder-1", "coder-2", "reviewer-1",
    ]


def test_an_auto_name_takes_the_first_free_number(live_project, engine, agent_image):
    project_with(live_project, agent_image)
    live_project.spawn("coder")
    live_project.spawn("coder")
    assert live_project.run("rm", "--force", "coder-1").returncode == 0
    assert live_project.spawn("coder") == "coder-1"


def test_a_chosen_name_is_the_instance_name(live_project, engine, agent_image):
    project_with(live_project, agent_image)
    assert live_project.spawn("coder", "issue-412") == "issue-412"
    container = engine.instance(live_project.name, "issue-412")
    assert container.name == f"{live_project.name}-issue-412"
    assert container.labels[f"{LABEL_PREFIX}service"] == "coder"


def test_every_instance_has_its_own_identity_toward_the_control_sidecar(live_project, engine, agent_image):
    project_with(live_project, agent_image)
    live_project.spawn("coder")
    live_project.spawn("coder")
    identities = {}
    for name in ("coder-1", "coder-2"):
        env = dict(item.split("=", 1) for item in engine.instance(live_project.name, name).raw["Config"]["Env"])
        assert env["EGZO_AGENT"] == name
        identities[name] = env["EGZO_TOKEN"]
    assert identities["coder-1"] != identities["coder-2"]
    other = in_instance(engine, live_project, "coder-1", 'curl -sS -m 10 -o /dev/null -w "%{http_code}" -u "coder-2:$EGZO_TOKEN" "$EGZO_CONTROL_URL/v1/messages"')
    assert other.stdout == "401", "one instance's token must not work as another's"


def test_spawn_json_describes_the_instance(live_project, engine, agent_image):
    project_with(live_project, agent_image)
    result = live_project.run("spawn", "coder", "issue-1", "--json", timeout=300)
    assert result.returncode == 0, result.stderr
    assert json.loads(result.stdout) == {
        "name": "issue-1", "service": "coder", "container": f"{live_project.name}-issue-1", "actor": "operator",
    }


def test_the_progress_goes_to_stderr(live_project, engine, agent_image):
    project_with(live_project, agent_image)
    result = live_project.run("spawn", "coder", timeout=300)
    assert result.stdout == "coder-1\n"
    assert result.stderr.strip(), "spawn says what it is doing, on stderr"


def test_the_first_message_is_sent_to_the_new_instance(live_project, engine, agent_image):
    project_with(live_project, agent_image)
    result = live_project.run("spawn", "coder", "issue-412", "-m", "Fix #412, then open a PR", timeout=300)
    assert result.returncode == 0, result.stderr
    assert result.stdout == "issue-412\n"
    [row] = [r for r in messages(live_project).values() if "Fix #412" in r["TEXT"]]
    assert (row["FROM"], row["TO"], row["KIND"]) == ("operator", "agent:issue-412", "request")


def test_without_a_message_nothing_is_sent(live_project, engine, agent_image):
    project_with(live_project, agent_image)
    live_project.spawn("coder")
    assert messages(live_project, "--all") == {}


def test_two_instances_of_one_template_are_isolated_from_each_other(live_project, engine, agent_image):
    project_with(live_project, agent_image)
    live_project.spawn("coder")
    live_project.spawn("coder")
    second_ip = engine.instance(live_project.name, "coder-2").raw["NetworkSettings"]["Networks"][f"{live_project.name}_coder-2"]["IPAddress"]
    listener = engine.instance(live_project.name, "coder-2").name
    assert engine.exec(listener, "nc", "-l", "-p", "8080", "-e", "/bin/echo", detach=True).returncode == 0
    assert engine.exec(listener, "nc", "-w", "3", second_ip, "8080").returncode == 0  # it listens: a failure below is isolation
    assert in_instance(engine, live_project, "coder-1", f"nc -w 3 {second_ip} 8080").returncode != 0


# --- spawn uses the published templates -----------------------------------------------------------------------


def test_spawn_does_not_need_the_file_only_what_up_published(live_project, engine, agent_image):
    project_with(live_project, agent_image)
    (live_project.root / "egzo.yaml").write_text("# nothing here any more\n")
    assert live_project.spawn("coder") == "coder-1"


def test_editing_the_file_changes_nothing_for_spawn_until_up_runs(live_project, engine, agent_image):
    project_with(live_project, agent_image, coder=custom(agent_image, env={"WHICH": "published"}))
    live_project.write(spec(agents={"coder": custom(agent_image, env={"WHICH": "edited"})}))
    live_project.spawn("coder")
    env = dict(item.split("=", 1) for item in engine.instance(live_project.name, "coder-1").raw["Config"]["Env"])
    assert env["WHICH"] == "published"


def test_a_template_added_to_the_file_cannot_be_spawned_before_up(live_project, engine, agent_image):
    project_with(live_project, agent_image)
    live_project.write(spec(agents={"coder": custom(agent_image), "reviewer": custom(agent_image)}))
    result = live_project.run("spawn", "reviewer")
    assert result.returncode != 0
    assert "reviewer" in result.stderr and "egzo up" in result.stderr
    assert engine.instances(live_project.name) == []


def test_an_unknown_template_names_the_published_ones(live_project, engine, agent_image):
    project_with(live_project, agent_image, coder=custom(agent_image), reviewer=custom(agent_image))
    result = live_project.run("spawn", "nobody")
    assert result.returncode != 0
    assert "nobody" in result.stderr and "coder" in result.stderr and "reviewer" in result.stderr


def test_the_environment_of_the_machine_that_spawns_does_not_leak_into_the_instance(live_project, engine, agent_image):
    project_with(live_project, agent_image)
    live_project.run("spawn", "coder", env={"SPAWNER_ONLY_VARIABLE": "leak"}, timeout=300)
    env = engine.instance(live_project.name, "coder-1").raw["Config"]["Env"]
    assert not [e for e in env if e.startswith("SPAWNER_ONLY_VARIABLE")]


# --- refusals ----------------------------------------------------------------------------------------------------


def test_spawn_before_up_says_to_run_up_and_creates_nothing(live_project, engine, agent_image):
    live_project.write(spec(agents={"coder": custom(agent_image)}))
    result = live_project.run("spawn", "coder")
    assert result.returncode != 0
    assert "egzo up" in result.stderr
    assert engine.resources(live_project.name) == []


def test_spawn_after_down_says_to_run_up(live_project, engine, agent_image):
    project_with(live_project, agent_image)
    assert live_project.run("down").returncode == 0
    result = live_project.run("spawn", "coder")
    assert result.returncode != 0 and "egzo up" in result.stderr
    assert engine.instances(live_project.name) == []


def test_spawn_never_starts_the_infrastructure(live_project, engine, agent_image):
    project_with(live_project, agent_image)
    assert live_project.run("stop", "proxy").returncode == 0
    result = live_project.run("spawn", "coder")
    assert result.returncode != 0
    assert "proxy" in result.stderr and "egzo up" in result.stderr
    assert engine.instances(live_project.name) == []


@pytest.mark.parametrize("name", ["Upper", "has space", "-leading", "_leading", "a.b", "x" * 64, "a/b", "--flag"])
def test_an_invalid_instance_name_is_refused(live_project, engine, agent_image, name):
    project_with(live_project, agent_image)
    result = live_project.run("spawn", "coder", name)
    assert result.returncode != 0
    assert result.returncode != 17
    assert engine.instances(live_project.name) == []


@pytest.mark.parametrize("name", ["coder", "reviewer", "control", "proxy", "prep", "shared", "control-1", "proxy-1"])
def test_a_reserved_name_or_a_template_name_is_refused(live_project, engine, agent_image, name):
    project_with(live_project, agent_image, coder=custom(agent_image), reviewer=custom(agent_image))
    result = live_project.run("spawn", "coder", name)
    assert result.returncode != 0
    assert result.returncode != 17
    assert name in result.stderr
    assert engine.instances(live_project.name) == []


def test_a_name_that_exists_exits_17_and_changes_nothing(live_project, engine, agent_image):
    project_with(live_project, agent_image, coder=custom(agent_image), reviewer=custom(agent_image))
    live_project.spawn("coder", "issue-412")
    before = {c.name: c.raw["Id"] for c in engine.containers(live_project.name)}
    sent_before = messages(live_project, "--all")
    result = live_project.run("spawn", "coder", "issue-412", "-m", "a second task", timeout=300)
    assert result.returncode == 17, result.stderr
    assert "issue-412" in result.stderr and "coder" in result.stderr
    assert result.stdout == ""
    assert {c.name: c.raw["Id"] for c in engine.containers(live_project.name)} == before
    assert messages(live_project, "--all") == sent_before, "no message is sent to an instance that already exists"


def test_a_name_that_exists_under_another_template_also_exits_17_and_names_that_template(live_project, engine, agent_image):
    project_with(live_project, agent_image, coder=custom(agent_image), reviewer=custom(agent_image))
    live_project.spawn("reviewer", "shared-name")
    result = live_project.run("spawn", "coder", "shared-name")
    assert result.returncode == 17
    assert "reviewer" in result.stderr
    assert engine.instance(live_project.name, "shared-name").labels[f"{LABEL_PREFIX}service"] == "reviewer"


def test_two_spawns_of_one_name_at_once_make_exactly_one_instance(live_project, engine, agent_image):
    project_with(live_project, agent_image)
    environment = {**os.environ, **live_project.env, "NO_COLOR": "1"}
    environment.pop("EGZO_PROJECT_NAME", None)

    def launch():
        return subprocess.Popen(
            [live_project.egzo.binary, "spawn", "coder", "race"], cwd=live_project.root, env=environment,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
        )

    first, second = launch(), launch()
    first.communicate(timeout=300)
    second.communicate(timeout=300)
    assert sorted([first.returncode, second.returncode]) == [0, 17], (first.returncode, second.returncode)
    assert len([r for r in engine.instances(live_project.name)]) == 1
    assert engine.instance(live_project.name, "race").raw["State"]["Running"] is True


def test_a_template_name_is_not_an_instance_for_the_commands_that_address_one(live_project, engine, agent_image):
    project_with(live_project, agent_image)
    for command in (["send", "coder", "hi"], ["attach", "coder"], ["rm", "coder"]):
        result = live_project.run(*command, input="")
        assert result.returncode != 0, command
        assert "coder is a template" in result.stderr, command
        assert "egzo spawn coder" in result.stderr, command


def test_a_spawn_that_fails_leaves_nothing_behind_and_the_name_stays_free(live_project, engine, agent_image):
    project_with(live_project, agent_image, broken=custom("egzo-spec-no-such-image:0"), coder=custom(agent_image))
    result = live_project.run("spawn", "broken", "attempt", timeout=300)
    assert result.returncode != 0
    assert "egzo-spec-no-such-image" in result.stderr
    assert engine.instances(live_project.name) == []
    assert not [r for r in engine.resources(live_project.name) if "attempt" in r.name], "a failed spawn left a network or volume"
    assert live_project.spawn("coder", "attempt") == "attempt"


# --- --wait and --attach ----------------------------------------------------------------------------------------------


def test_wait_needs_a_message(live_project, engine, agent_image):
    project_with(live_project, agent_image)
    result = live_project.run("spawn", "coder", "--wait")
    assert result.returncode != 0
    assert "-m" in result.stderr or "message" in result.stderr
    assert engine.instances(live_project.name) == []


def test_wait_and_attach_exclude_each_other(live_project, engine, agent_image):
    project_with(live_project, agent_image)
    result = live_project.run("spawn", "coder", "-m", "x", "--wait", "--attach")
    assert result.returncode != 0
    assert engine.instances(live_project.name) == []


def spawn_waiting(project, *args):
    environment = {**os.environ, **project.env, "NO_COLOR": "1"}
    environment.pop("EGZO_PROJECT_NAME", None)
    return subprocess.Popen(
        [project.egzo.binary, "spawn", *args, "--wait", "--timeout", "90s"], cwd=project.root, env=environment,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
    )


@pytest.mark.parametrize("outcome,code", [("done", 0), ("declined", 3), ("failed", 4)])
def test_wait_returns_the_resolution_of_the_first_message_like_send_wait(live_project, engine, agent_image, outcome, code):
    project_with(live_project, agent_image)
    waiting = spawn_waiting(live_project, "coder", "job-1", "-m", "do the task")
    message_id = wait_for(lambda: next(iter(messages(live_project)), None), "the first message")
    wait_for(lambda: engine.instance(live_project.name, "job-1"), "the instance")
    fetch, _ = as_agent(engine, live_project, "job-1", "GET", f"/v1/messages/{message_id}")
    assert fetch == 200
    resolved, _ = as_agent(engine, live_project, "job-1", "POST", f"/v1/messages/{message_id}/resolve", {"text": "the answer is 42", "outcome": outcome})
    assert resolved in (200, 204)
    out, err = waiting.communicate(timeout=60)
    assert waiting.returncode == code, (out, err)
    assert "the answer is 42" in out
    assert "job-1" in err
    assert "job-1" not in out


def test_wait_times_out_with_exit_5_and_leaves_the_instance_and_the_message_open(live_project, engine, agent_image):
    project_with(live_project, agent_image)
    result = live_project.run("spawn", "coder", "slow", "-m", "never answered", "--wait", "--timeout", "4s", timeout=300)
    assert result.returncode == 5, (result.stdout, result.stderr)
    assert engine.instance(live_project.name, "slow").raw["State"]["Running"] is True
    assert any("never answered" in r["TEXT"] for r in messages(live_project).values())


def test_attach_opens_the_terminal_of_the_new_instance(live_project, engine, session_image, egzo):
    live_project.up(spec(agents={"coder": agent(harness="custom", image=session_image, env={"FAKE_TUI": "cat"})}))
    environment = {**os.environ, **live_project.env}
    environment.pop("EGZO_PROJECT_NAME", None)
    client = pexpect.spawn(
        egzo.binary, ["spawn", "coder", "pair", "--attach"], cwd=live_project.root, env=environment, encoding=None, timeout=120,
    )
    client.expect(b"READY")
    client.send(b"typed in the new instance")
    client.expect(b"typed in the new instance")
    client.send(CTRL_RIGHT_BRACKET)
    client.expect(pexpect.EOF)
    client.close()
    assert client.exitstatus == 0
    assert engine.instance(live_project.name, "pair").raw["State"]["Running"] is True, "detaching leaves the instance running"


def test_attach_with_a_message_delivers_it_to_the_instance(live_project, engine, session_image, egzo):
    live_project.up(spec(agents={"coder": agent(harness="custom", image=session_image, env={"FAKE_TUI": "cat"})}))
    environment = {**os.environ, **live_project.env}
    environment.pop("EGZO_PROJECT_NAME", None)
    client = pexpect.spawn(
        egzo.binary, ["spawn", "coder", "pair", "-m", "please look at this", "--attach"], cwd=live_project.root, env=environment,
        encoding=None, timeout=120,
    )
    client.expect(b"READY")
    client.send(CTRL_RIGHT_BRACKET)
    client.expect(pexpect.EOF)
    client.close()
    assert [r for r in messages(live_project).values() if "please look at this" in r["TEXT"]]


# --- the sidecars an instance needs ------------------------------------------------------------------------------------


def test_the_control_sidecar_and_the_proxy_join_the_network_of_each_instance(live_project, engine, agent_image):
    project_with(live_project, agent_image)
    live_project.spawn("coder")
    live_project.spawn("coder")
    for service in ("control", "proxy"):
        sidecar = [r for r in engine.containers(live_project.name) if r.labels.get(f"{LABEL_PREFIX}service") == service][0]
        joined = set(sidecar.raw["NetworkSettings"]["Networks"])
        assert {f"{live_project.name}_coder-1", f"{live_project.name}_coder-2"} <= joined, service


def test_an_instance_reaches_control_by_name_and_has_no_other_way_out(live_project, engine, agent_image):
    project_with(live_project, agent_image)
    live_project.spawn("coder")
    assert in_instance(engine, live_project, "coder-1", "getent hosts control").returncode == 0
    assert in_instance(engine, live_project, "coder-1", "wget -T 3 -q -O /dev/null http://1.1.1.1/").returncode != 0
    raw = [r for r in engine.resources(live_project.name) if r.kind == "network" and r.name.endswith("_coder-1")][0].raw
    assert raw.get("Internal", raw.get("internal")) is True


def test_the_agent_environment_names_the_instance_not_the_template(live_project, engine, agent_image):
    project_with(live_project, agent_image)
    live_project.spawn("coder", "issue-9")
    env = dict(item.split("=", 1) for item in engine.instance(live_project.name, "issue-9").raw["Config"]["Env"])
    assert env["EGZO_AGENT"] == "issue-9"
    assert env["HTTPS_PROXY"].startswith("http://issue-9:")
    assert re.fullmatch(r"[0-9a-f]{64}", env["EGZO_TOKEN"])
