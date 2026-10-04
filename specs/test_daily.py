"""Day-to-day commands: logs, exec (with and without a terminal), start, stop, restart, proxy log."""

import json

import pexpect

from conftest import LABEL_PREFIX
from support import agent, spec


def custom(image, **fields):
    return agent(harness="custom", image=image, **fields)


def up(project, document):
    project.write(document)
    result = project.run("up", timeout=300)
    assert result.returncode == 0, result.stderr


def one_agent(live_project, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image)}))


def state(engine, project, service):
    found = [r for r in engine.containers(project.name) if r.labels.get(f"{LABEL_PREFIX}service") == service]
    return found[0].raw["State"]


def test_logs_show_what_the_container_wrote(live_project, engine, agent_image):
    one_agent(live_project, agent_image)
    wrote = live_project.run("exec", "coder", "--", "sh", "-c", "echo hello-from-the-agent > /proc/1/fd/1")
    assert wrote.returncode == 0, wrote.stderr
    assert "hello-from-the-agent" in live_project.run("logs", "coder").stdout


def test_logs_of_an_unknown_service_name_the_service(live_project, engine, agent_image):
    one_agent(live_project, agent_image)
    result = live_project.run("logs", "ghost")
    assert result.returncode != 0
    assert "ghost" in result.stderr


def test_exec_runs_a_command_and_returns_its_output(live_project, engine, agent_image):
    one_agent(live_project, agent_image)
    result = live_project.run("exec", "coder", "--", "echo", "hello")
    assert result.returncode == 0
    assert result.stdout.strip() == "hello"


def test_exec_exits_with_the_exit_code_of_the_command(live_project, engine, agent_image):
    one_agent(live_project, agent_image)
    assert live_project.run("exec", "coder", "--", "sh", "-c", "exit 3").returncode == 3


def test_exec_keeps_stdout_and_stderr_apart(live_project, engine, agent_image):
    one_agent(live_project, agent_image)
    result = live_project.run("exec", "coder", "--", "sh", "-c", "echo out; echo err >&2")
    assert result.stdout.strip() == "out"
    assert result.stderr.strip() == "err"


def test_exec_passes_stdin_through(live_project, engine, agent_image):
    one_agent(live_project, agent_image)
    result = live_project.run("exec", "coder", "--", "cat", input="piped in\n")
    assert result.stdout == "piped in\n"


def test_exec_in_an_unknown_service_is_an_error(live_project, engine, agent_image):
    one_agent(live_project, agent_image)
    assert live_project.run("exec", "ghost", "--", "true").returncode != 0


def spawn_exec(live_project, egzo, *command, dimensions=(24, 80)):
    environment = {**__import__("os").environ, **live_project.env, "TERM": "xterm-256color", "NO_COLOR": "1"}
    environment.pop("EGZO_PROJECT_NAME", None)
    return pexpect.spawn(
        egzo.binary, ["exec", "coder", "--", *command], cwd=live_project.root, env=environment,
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
    before = [r for r in engine.containers(live_project.name) if r.labels[f"{LABEL_PREFIX}service"] == "coder"][0].raw["Id"]

    assert live_project.run("stop", "coder").returncode == 0
    assert state(engine, live_project, "coder")["Running"] is False
    assert live_project.run("start", "coder").returncode == 0
    assert state(engine, live_project, "coder")["Running"] is True
    after = [r for r in engine.containers(live_project.name) if r.labels[f"{LABEL_PREFIX}service"] == "coder"][0].raw["Id"]
    assert after == before


def test_restart_restarts_the_container(live_project, engine, agent_image):
    one_agent(live_project, agent_image)
    started = state(engine, live_project, "coder")["StartedAt"]
    assert live_project.run("restart", "coder").returncode == 0
    assert state(engine, live_project, "coder")["StartedAt"] != started


def test_up_starts_a_stopped_container_instead_of_recreating_it(live_project, engine, agent_image):
    one_agent(live_project, agent_image)
    assert live_project.run("stop", "coder").returncode == 0
    result = live_project.run("up")
    assert result.returncode == 0, result.stderr
    assert "start container" in result.stdout
    assert state(engine, live_project, "coder")["Running"] is True


def test_proxy_log_shows_the_audit_trail(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image)}))
    live_project.run("exec", "coder", "--", "curl", "-sS", "-m", "10", "-o", "/dev/null", "https://example.org/")
    events = [json.loads(line) for line in live_project.run("proxy", "log").stdout.splitlines() if line.startswith("{")]
    denied = [e for e in events if e["host"] == "example.org"]
    assert denied and denied[0]["action"] == "deny" and denied[0]["agent"] == "coder"


def test_ps_lists_the_project_containers_by_service(live_project, engine, agent_image):
    one_agent(live_project, agent_image)
    listing = live_project.run("ps").stdout
    for service in ("control", "proxy", "coder"):
        assert service in listing
