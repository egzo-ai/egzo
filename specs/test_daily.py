# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""Day-to-day commands: logs, exec (with and without a terminal), start, stop, restart, proxy log."""

import json

import pexpect

from conftest import LABEL_PREFIX
from support import agent, spec


def custom(image, **fields):
    return agent(harness="custom", image=image, **fields)


def one_agent(live_project, agent_image):
    """One running instance of the template `coder`: `coder-1`."""
    live_project.up(spec(agents={"coder": custom(agent_image)}))
    return live_project.spawn("coder")


def state(engine, project, instance):
    found = [r for r in engine.containers(project.name) if r.labels.get(f"{LABEL_PREFIX}instance") == instance]
    return found[0].raw["State"]


def test_logs_show_what_the_container_wrote(live_project, engine, agent_image):
    one_agent(live_project, agent_image)
    wrote = live_project.run("exec", "coder-1", "--", "sh", "-c", "echo hello-from-the-agent > /proc/1/fd/1")
    assert wrote.returncode == 0, wrote.stderr
    assert "hello-from-the-agent" in live_project.run("logs", "coder-1").stdout


def test_logs_of_an_unknown_service_name_the_service(live_project, engine, agent_image):
    one_agent(live_project, agent_image)
    result = live_project.run("logs", "ghost")
    assert result.returncode != 0
    assert "ghost" in result.stderr


def test_exec_runs_a_command_and_returns_its_output(live_project, engine, agent_image):
    one_agent(live_project, agent_image)
    result = live_project.run("exec", "coder-1", "--", "echo", "hello")
    assert result.returncode == 0
    assert result.stdout.strip() == "hello"


def test_exec_exits_with_the_exit_code_of_the_command(live_project, engine, agent_image):
    one_agent(live_project, agent_image)
    assert live_project.run("exec", "coder-1", "--", "sh", "-c", "exit 3").returncode == 3


def test_exec_keeps_stdout_and_stderr_apart(live_project, engine, agent_image):
    one_agent(live_project, agent_image)
    result = live_project.run("exec", "coder-1", "--", "sh", "-c", "echo out; echo err >&2")
    assert result.stdout.strip() == "out"
    assert result.stderr.strip() == "err"


def test_exec_passes_stdin_through(live_project, engine, agent_image):
    one_agent(live_project, agent_image)
    result = live_project.run("exec", "coder-1", "--", "cat", input="piped in\n")
    assert result.stdout == "piped in\n"


def test_exec_in_an_unknown_service_is_an_error(live_project, engine, agent_image):
    one_agent(live_project, agent_image)
    assert live_project.run("exec", "ghost", "--", "true").returncode != 0


def spawn_exec(live_project, egzo, *command, dimensions=(24, 80)):
    environment = {**__import__("os").environ, **live_project.env, "TERM": "xterm-256color", "NO_COLOR": "1"}
    environment.pop("EGZO_PROJECT_NAME", None)
    return pexpect.spawn(
        egzo.binary, ["exec", "coder-1", "--", *command], cwd=live_project.root, env=environment,
        dimensions=dimensions, encoding="utf-8", timeout=30,
    )


def test_exec_with_a_terminal_is_interactive(live_project, engine, agent_image, egzo):
    one_agent(live_project, agent_image)
    shell = spawn_exec(live_project, egzo, "sh")
    shell.sendline("echo $((6*7))")
    shell.expect("42")
    shell.sendline("tty")
    shell.expect("/dev/pts/")
    shell.sendline("exit 5")
    shell.expect(pexpect.EOF)
    shell.close()
    assert shell.exitstatus == 5


def test_exec_with_a_terminal_follows_window_resizes(live_project, engine, agent_image, egzo):
    one_agent(live_project, agent_image)
    shell = spawn_exec(live_project, egzo, "sh", dimensions=(24, 80))
    shell.sendline("stty size")
    shell.expect("24 80")
    shell.setwinsize(40, 132)
    shell.sendline("stty size")
    shell.expect("40 132")
    shell.sendline("exit")
    shell.expect(pexpect.EOF)


def test_exec_with_a_terminal_passes_control_characters_through(live_project, engine, agent_image, egzo):
    one_agent(live_project, agent_image)
    shell = spawn_exec(live_project, egzo, "sh")
    # Computed output cannot be confused with the typed (echoed) command line.
    shell.sendline("echo $((20+22))started; sleep 100")
    shell.expect("42started")
    shell.sendcontrol("c")  # interrupts the sleep inside the container, not egzo
    shell.sendline("echo $((1+1))alive")
    shell.expect("2alive")
    shell.sendline("exit")
    shell.expect(pexpect.EOF)


def test_stop_and_start_work_on_the_existing_container(live_project, engine, agent_image):
    one_agent(live_project, agent_image)
    before = engine.instance(live_project.name, "coder-1").raw["Id"]

    assert live_project.run("stop", "coder-1").returncode == 0
    assert state(engine, live_project, "coder-1")["Running"] is False
    assert live_project.run("start", "coder-1").returncode == 0
    assert state(engine, live_project, "coder-1")["Running"] is True
    after = engine.instance(live_project.name, "coder-1").raw["Id"]
    assert after == before


def test_restart_restarts_the_container(live_project, engine, agent_image):
    one_agent(live_project, agent_image)
    started = state(engine, live_project, "coder-1")["StartedAt"]
    assert live_project.run("restart", "coder-1").returncode == 0
    assert state(engine, live_project, "coder-1")["StartedAt"] != started


def test_up_leaves_a_stopped_instance_stopped(live_project, engine, agent_image):
    one_agent(live_project, agent_image)
    assert live_project.run("stop", "coder-1").returncode == 0
    result = live_project.run("up")
    assert result.returncode == 0, result.stderr
    assert state(engine, live_project, "coder-1")["Running"] is False


def test_naming_a_template_says_to_spawn_it(live_project, engine, agent_image):
    one_agent(live_project, agent_image)
    for command in (["logs", "coder"], ["exec", "coder", "--", "true"], ["stop", "coder"], ["start", "coder"], ["restart", "coder"]):
        result = live_project.run(*command)
        assert result.returncode != 0, command
        assert "coder is a template" in result.stderr, command
        assert "egzo spawn coder" in result.stderr, command


def test_the_sidecars_are_reached_by_their_service_name(live_project, engine, agent_image):
    one_agent(live_project, agent_image)
    assert live_project.run("logs", "control").returncode == 0
    assert live_project.run("logs", "proxy").returncode == 0
    assert live_project.run("exec", "control", "--", "true").returncode == 0


def test_proxy_log_shows_the_audit_trail(live_project, engine, agent_image):
    one_agent(live_project, agent_image)
    live_project.run("exec", "coder-1", "--", "curl", "-sS", "-m", "10", "-o", "/dev/null", "https://example.org/")
    events = [json.loads(line) for line in live_project.run("proxy", "log").stdout.splitlines() if line.startswith("{")]
    denied = [e for e in events if e["host"] == "example.org"]
    assert denied and denied[0]["action"] == "deny" and denied[0]["agent"] == "coder-1"


def test_ps_lists_the_project_containers_by_service(live_project, engine, agent_image):
    one_agent(live_project, agent_image)
    listing = live_project.run("ps").stdout
    for service in ("control", "proxy", "coder", "coder-1"):
        assert service in listing
