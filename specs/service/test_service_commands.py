# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""The commands that work on one container by its name, an instance or a sidecar: `logs`, `exec` (with and without a terminal),
`start`, `stop`, `restart`, and `ps`. One project is up for the whole module (`sandbox`) and every spec starts from no instance."""

import json
import os

import pexpect
import pytest

from conftest import LABEL_PREFIX
from support import custom, spec


@pytest.fixture(scope="module")
def sandbox(shared_project, engine):
    shared_project.up(spec(agents={"coder": custom(engine.agent_image())}))
    return shared_project


@pytest.fixture(autouse=True)
def start_from_no_instance(sandbox):
    sandbox.retire()
    yield


def state(engine, project, instance):
    return next(r for r in engine.containers(project.name) if r.labels.get(f"{LABEL_PREFIX}instance") == instance).raw["State"]


def terminal(project, egzo, *command, dimensions=(24, 80)):
    environment = {**os.environ, **project.env, "TERM": "xterm-256color", "NO_COLOR": "1"}
    environment.pop("EGZO_PROJECT_NAME", None)
    return pexpect.spawn(
        egzo.binary, ["exec", "coder-1", "--", *command], cwd=project.root, env=environment,
        dimensions=dimensions, encoding="utf-8", timeout=30,
    )


def test_logs_and_exec_work_on_instances_and_sidecars_by_name_and_say_when_it_is_a_template_or_unknown(sandbox, engine):
    p = sandbox
    p.spawn("coder")
    assert p.sh("exec coder-1 -- sh -c", "echo hello-from-the-agent > /proc/1/fd/1").returncode == 0
    assert "hello-from-the-agent" in p.ok("logs coder-1").stdout
    p.ok("logs control")
    p.ok("logs proxy")
    p.ok("exec control -- true")

    assert p.sh("exec coder-1 -- sh -c", "exit 3").returncode == 3, "exec exits with the exit code of the command"
    split = p.sh("exec coder-1 -- sh -c", "echo out; echo err >&2")
    assert split.stdout.strip() == "out" and split.stderr.strip() == "err"
    assert p.sh("exec coder-1 -- cat", input="piped in\n").stdout == "piped in\n"

    for command in ("logs ghost", "exec ghost -- true"):
        missing = p.sh(command)
        assert missing.returncode != 0 and (command != "logs ghost" or "ghost" in missing.stderr), command
    for command in ("logs coder", "exec coder -- true", "stop coder", "start coder", "restart coder"):
        template = p.sh(command)
        assert template.returncode != 0 and "coder is a template" in template.stderr and "egzo agent spawn coder" in template.stderr, command

    p.sh("exec coder-1 -- curl -sS -m 10 -o /dev/null https://example.org/")
    audit = [json.loads(line) for line in p.ok("proxy log").stdout.splitlines() if line.startswith("{")]
    denied = [e for e in audit if e["host"] == "example.org"]
    assert denied and denied[0]["action"] == "deny" and denied[0]["agent"] == "coder-1"
    listing = p.ok("ps").stdout
    assert all(service in listing for service in ("control", "proxy", "coder", "coder-1"))


def test_exec_with_a_terminal_is_interactive_follows_resizes_and_passes_control_characters(sandbox, egzo):
    sandbox.spawn("coder")
    shell = terminal(sandbox, egzo, "sh")
    shell.sendline("echo $((6*7))")
    shell.expect("42")
    shell.sendline("tty")
    shell.expect("/dev/pts/")
    shell.sendline("stty size")
    shell.expect("24 80")
    shell.setwinsize(40, 132)
    shell.sendline("stty size")
    shell.expect("40 132")
    shell.sendline("echo $((20+22))started; sleep 100")  # computed output cannot be confused with the echoed command line
    shell.expect("42started")
    shell.sendcontrol("c")  # interrupts the sleep inside the container, not egzo
    shell.sendline("echo $((1+1))alive")
    shell.expect("2alive")
    shell.sendline("exit 5")
    shell.expect(pexpect.EOF)
    shell.close()
    assert shell.exitstatus == 5


def test_stop_start_and_restart_work_on_the_existing_container_and_up_leaves_a_stopped_instance_stopped(sandbox, engine):
    p = sandbox
    p.spawn("coder")
    before = engine.instance(p.name, "coder-1").raw["Id"]
    p.ok("stop coder-1")
    assert state(engine, p, "coder-1")["Running"] is False
    p.ok("project up")
    assert state(engine, p, "coder-1")["Running"] is False, "up leaves a stopped instance stopped"
    p.ok("start coder-1")
    assert state(engine, p, "coder-1")["Running"] is True and engine.instance(p.name, "coder-1").raw["Id"] == before

    started = state(engine, p, "coder-1")["StartedAt"]
    p.ok("restart coder-1")
    assert state(engine, p, "coder-1")["StartedAt"] != started
