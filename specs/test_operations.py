"""The remaining operator commands: secrets, doctor, diff, ca rotate, up SERVICE, proxy rules."""

import json
import os
import re
import subprocess

import pytest

from conftest import LABEL_PREFIX
from support import agent, spec

pytestmark = pytest.mark.usefixtures("engine")


def custom(image, **fields):
    return agent(harness="custom", image=image, **fields)


def up(project, document, *args):
    project.write(document)
    result = project.run("up", *args, timeout=300)
    assert result.returncode == 0, result.stderr
    return result


def container(engine, project, service):
    found = [r for r in engine.containers(project.name) if r.labels.get(f"{LABEL_PREFIX}service") == service]
    return found[0] if found else None


def file_vault(path, **names):
    return {"main": {"backend": "file", "secrets": {name: {"from": f"file:{path}/{name}"} for name in names}}}


# --- secrets ----------------------------------------------------------------------------------------------


@pytest.mark.todo("secrets command")
def test_secrets_ls_shows_which_secrets_are_set_and_never_their_values(project, tmp_path):
    (tmp_path / "TOKEN_A").write_text("super-secret-value\n")
    vaults = {"main": {"backend": "env", "secrets": {
        "FROM_FILE": {"from": f"file:{tmp_path}/TOKEN_A"},
        "FROM_ENV": {"from": "env:SPEC_SET_VAR"},
        "MISSING": {"from": "env:SPEC_UNSET_VAR"},
    }}}
    project.write({"vaults": vaults})
    result = project.run("secrets", "ls", env={"SPEC_SET_VAR": "another-secret-value"})
    assert result.returncode == 0, result.stderr
    rows = {line.split()[0]: line for line in result.stdout.splitlines()[1:]}
    assert "set" in rows["main/FROM_FILE"] and "set" in rows["main/FROM_ENV"] and "missing" in rows["main/MISSING"]
    assert "super-secret-value" not in result.stdout + result.stderr
    assert "another-secret-value" not in result.stdout + result.stderr


@pytest.mark.todo("secrets command")
def test_secrets_set_writes_a_file_source_from_stdin_with_private_permissions(project, tmp_path):
    target = tmp_path / "vault" / "API_KEY"
    project.write({"vaults": file_vault(tmp_path / "vault", API_KEY=None)})
    result = project.run("secrets", "set", "main/API_KEY", input="s3cr3t-value\n")
    assert result.returncode == 0, result.stderr
    assert target.read_text().strip() == "s3cr3t-value"
    assert (target.stat().st_mode & 0o777) == 0o600
    assert "s3cr3t-value" not in result.stdout + result.stderr


@pytest.mark.todo("secrets command")
def test_secrets_set_refuses_an_env_source_and_says_why(project):
    project.write({"vaults": {"main": {"backend": "env", "secrets": {"TOKEN": {"from": "env:SOME_VAR"}}}}})
    result = project.run("secrets", "set", "main/TOKEN", input="value\n")
    assert result.returncode != 0
    assert "env" in result.stderr and "SOME_VAR" in result.stderr


@pytest.mark.todo("secrets command")
def test_secrets_set_of_an_unknown_secret_names_the_known_ones(project, tmp_path):
    project.write({"vaults": file_vault(tmp_path, API_KEY=None)})
    result = project.run("secrets", "set", "main/NOPE", input="x\n")
    assert result.returncode != 0
    assert "main/API_KEY" in result.stderr


@pytest.mark.todo("secrets command")
def test_secrets_rm_deletes_a_file_source(project, tmp_path):
    (tmp_path / "API_KEY").write_text("value\n")
    project.write({"vaults": file_vault(tmp_path, API_KEY=None)})
    assert project.run("secrets", "rm", "main/API_KEY").returncode == 0
    assert not (tmp_path / "API_KEY").exists()
    assert "missing" in project.run("secrets", "ls").stdout


@pytest.mark.todo("secrets command")
def test_a_secret_set_by_the_command_reaches_the_proxy_on_the_next_up(live_project, engine, agent_image, tmp_path):
    document = spec(
        vaults=file_vault(tmp_path, API_KEY=None),
        egress={"default": {"services": {"svc": {"hosts": ["api.test"], "inject": {"header": "x-key"}, "secret": "main/API_KEY"}}}},
        agents={"coder": custom(agent_image)},
    )
    live_project.write(document)
    assert live_project.run("secrets", "set", "main/API_KEY", input="one\n").returncode == 0
    assert live_project.run("up", timeout=300).returncode == 0
    assert live_project.run("secrets", "set", "main/API_KEY", input="two\n").returncode == 0
    second = live_project.run("up", timeout=300)
    assert "load egress policy" in second.stdout


# --- doctor -----------------------------------------------------------------------------------------------


@pytest.mark.todo("doctor")
def test_doctor_checks_the_engine_and_exits_zero_when_nothing_fails(project, engine):
    result = project.run("doctor", env=engine.env)
    assert result.returncode == 0, result.stdout + result.stderr
    lines = [l for l in result.stdout.splitlines() if l.strip()]
    assert all(re.match(r"(ok|warn|fail)\s", l) for l in lines), result.stdout
    assert any(l.startswith("ok") and "engine" in l for l in lines)


@pytest.mark.todo("doctor")
def test_doctor_fails_loudly_when_no_engine_answers(project):
    result = project.run("doctor", env={"DOCKER_HOST": "unix:///nonexistent.sock"})
    assert result.returncode != 0
    assert "fail" in result.stdout and "engine" in result.stdout


@pytest.mark.todo("doctor")
def test_doctor_reports_the_isolation_runtimes(project, engine):
    out = project.run("doctor", env=engine.env).stdout
    assert re.search(r"(ok|warn)\s+.*gVisor|runsc", out), out
    assert re.search(r"(ok|warn)\s+.*(rootless|rootful)", out), out


@pytest.mark.todo("doctor")
def test_doctor_warns_when_the_egzo_directory_is_not_git_ignored(project, engine):
    subprocess.run(["git", "init", "-q"], cwd=project.root, check=True)
    project.write(spec())
    result = project.run("doctor", env=engine.env)
    assert re.search(r"warn\s+.*\.egzo", result.stdout), result.stdout
    (project.root / ".gitignore").write_text(".egzo/\n")
    assert not re.search(r"warn\s+.*\.egzo", project.run("doctor", env=engine.env).stdout)


# --- diff -------------------------------------------------------------------------------------------------


@pytest.mark.todo("diff command")
def test_diff_lists_what_up_would_do_and_exits_one_when_there_is_a_difference(live_project, engine, agent_image):
    live_project.write(spec(agents={"coder": custom(agent_image)}))
    result = live_project.run("diff")
    assert result.returncode == 1
    assert "coder" in result.stdout and "create" in result.stdout


@pytest.mark.todo("diff command")
def test_diff_is_silent_and_exits_zero_when_converged(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image)}))
    result = live_project.run("diff")
    assert result.returncode == 0, result.stdout
    assert result.stdout.strip() == ""


@pytest.mark.todo("diff command")
def test_diff_names_the_service_whose_definition_changed(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image), "reviewer": custom(agent_image)}))
    live_project.write(spec(agents={"coder": custom(agent_image, env={"CHANGED": "yes"}), "reviewer": custom(agent_image)}))
    result = live_project.run("diff")
    assert result.returncode == 1
    assert "coder" in result.stdout and "reviewer" not in result.stdout
    assert live_project.run("up", "--dry-run").returncode == 0  # same algorithm, no exit code semantics


# --- ca rotate --------------------------------------------------------------------------------------------


def read_ca(engine, project, service="coder"):
    return engine.exec(container(engine, project, service).name, "cat", "/etc/egzo/ca/ca.crt").stdout


@pytest.mark.todo("ca rotate")
def test_ca_rotate_issues_a_new_ca_and_restarts_the_agents_that_trust_it(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image)}))
    old_ca = read_ca(engine, live_project)
    started = container(engine, live_project, "coder").raw["State"]["StartedAt"]
    result = live_project.run("ca", "rotate", timeout=300)
    assert result.returncode == 0, result.stderr
    new_ca = read_ca(engine, live_project)
    assert new_ca.startswith("-----BEGIN CERTIFICATE-----") and new_ca != old_ca
    assert container(engine, live_project, "coder").raw["State"]["StartedAt"] != started
    assert "nothing to do" in live_project.run("up").stdout


@pytest.mark.todo("ca rotate")
def test_after_rotating_the_proxy_still_serves_the_policy(live_project, engine, agent_image):
    up(live_project, spec(egress={"default": {"allow": ["example.com"]}}, agents={"coder": custom(agent_image)}))
    assert live_project.run("ca", "rotate", timeout=300).returncode == 0
    reached = engine.exec(container(engine, live_project, "coder").name, "sh", "-c", 'curl -sS -m 20 -o /dev/null -w "%{http_code}" https://example.com/')
    assert reached.stdout.strip() == "200", reached.stderr


# --- up SERVICE and depends_on ---------------------------------------------------------------------------------


@pytest.mark.todo("up service")
def test_up_with_a_service_only_brings_up_that_agent_and_what_it_needs(live_project, engine, agent_image):
    live_project.write(spec(agents={"coder": custom(agent_image), "reviewer": custom(agent_image)}))
    assert live_project.run("up", "coder", timeout=300).returncode == 0
    assert container(engine, live_project, "coder") and container(engine, live_project, "control") and container(engine, live_project, "proxy")
    assert container(engine, live_project, "reviewer") is None
    assert live_project.run("up", timeout=300).returncode == 0
    assert container(engine, live_project, "reviewer")


@pytest.mark.todo("up service")
def test_up_with_an_unknown_service_names_the_known_ones(live_project, agent_image):
    live_project.write(spec(agents={"coder": custom(agent_image)}))
    result = live_project.run("up", "nobody")
    assert result.returncode != 0
    assert "nobody" in result.stderr and "coder" in result.stderr


@pytest.mark.todo("up service")
def test_depends_on_brings_dependencies_up_first(live_project, engine, agent_image):
    live_project.write(spec(agents={"coder": custom(agent_image), "reviewer": custom(agent_image, depends_on=["coder"])}))
    assert live_project.run("up", "reviewer", timeout=300).returncode == 0
    coder = container(engine, live_project, "coder")
    reviewer = container(engine, live_project, "reviewer")
    assert coder and reviewer
    assert coder.raw["State"]["StartedAt"] <= reviewer.raw["State"]["StartedAt"]


# --- the proxy's audit trail and rules ---------------------------------------------------------------------------


@pytest.mark.todo("proxy rules")
def test_proxy_log_can_be_filtered_by_agent(live_project, engine, agent_image):
    up(live_project, spec(egress={"default": {"allow": []}}, agents={"coder": custom(agent_image), "reviewer": custom(agent_image)}))
    for name in ("coder", "reviewer"):
        live_project.run("exec", name, "--", "curl", "-sS", "-m", "10", "-o", "/dev/null", f"https://{name}.example.org/")
    both = [json.loads(l) for l in live_project.run("proxy", "log").stdout.splitlines() if l.startswith("{")]
    assert {e["agent"] for e in both} >= {"coder", "reviewer"}
    only = [json.loads(l) for l in live_project.run("proxy", "log", "--agent", "coder").stdout.splitlines() if l.startswith("{")]
    assert only and {e["agent"] for e in only} == {"coder"}


@pytest.mark.todo("proxy rules")
def test_proxy_rules_show_what_each_agent_may_reach_without_any_secret(live_project, engine, agent_image):
    live_project.env["API_SECRET"] = "very-secret-value-1234567890"
    document = spec(
        vaults={"main": {"backend": "env", "secrets": {"API_SECRET": {"from": "env:API_SECRET"}}}},
        egress={
            "default": {"allow": ["docs.example.org"]},
            "wide": {"extend": "default", "services": {"svc": {"hosts": ["api.test"], "inject": {"header": "x-key"}, "secret": "main/API_SECRET"}}},
        },
        agents={"coder": custom(agent_image), "reviewer": custom(agent_image, egress="wide")},
    )
    up(live_project, document)
    rules = live_project.run("proxy", "rules")
    assert rules.returncode == 0, rules.stderr
    assert "docs.example.org" in rules.stdout and "api.test" in rules.stdout
    assert "very-secret-value" not in rules.stdout + rules.stderr
    only = live_project.run("proxy", "rules", "--agent", "coder").stdout
    assert "docs.example.org" in only and "api.test" not in only


# --- the handoff tool -------------------------------------------------------------------------------------------


def mcp_call(engine, project, name, tool, arguments):
    body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": tool, "arguments": arguments}})
    command = (
        'curl -sS -m 10 -u "$EGZO_AGENT:$EGZO_TOKEN" -X POST "$EGZO_CONTROL_URL/mcp" '
        "-H 'content-type: application/json' -H 'accept: application/json, text/event-stream' "
        f"-d '{body}'"
    )
    reply = engine.exec(container(engine, project, name).name, "sh", "-c", command)
    return json.loads(reply.stdout)["result"]


@pytest.mark.todo("handoff tool")
def test_handoff_queues_a_message_for_another_agent_from_the_calling_agent(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image), "reviewer": custom(agent_image)}))
    result = mcp_call(engine, live_project, "coder", "handoff", {"to": "reviewer", "text": "please review branch x"})
    assert not result.get("isError"), result
    queued = [json.loads(l) for l in live_project.run("events").stdout.splitlines() if l.startswith("{")]
    message = [e for e in queued if e["type"] == "message"][0]
    assert message["agent"] == "reviewer" and message["actor"] == "agent:coder" and message["text"] == "please review branch x"


@pytest.mark.todo("handoff tool")
def test_handoff_to_an_unknown_agent_or_to_oneself_is_an_error(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image), "reviewer": custom(agent_image)}))
    assert mcp_call(engine, live_project, "coder", "handoff", {"to": "ghost", "text": "x"}).get("isError")
    assert mcp_call(engine, live_project, "coder", "handoff", {"to": "coder", "text": "x"}).get("isError")
