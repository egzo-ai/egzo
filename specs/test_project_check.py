# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""`egzo project check` (is the file valid, can every secret be read, and what `--resolved` prints) and `egzo system check` (what egzo
depends on, outside any project). The other command groups are in the specs of what they do: `agent/`, `message/`, `workspace/`."""

import re

import pytest

from support import agent, spec

def secret_vault(*names):
    return {"main": {"backend": "env", "secrets": list(names)}}


def test_check_of_a_valid_project_prints_ok_lines_and_exits_zero(project):
    project.write(spec(vaults=secret_vault("TOKEN")))
    result = project.sh("project check", env={"TOKEN": "a-secret"})
    assert result.returncode == 0, result.stdout + result.stderr
    lines = [line for line in result.stdout.splitlines() if line.strip()]
    assert all(re.match(r"(ok|warn|fail)\s", line) for line in lines), result.stdout
    assert any(line.startswith("ok") and "egzo.yaml" in line for line in lines)
    assert any(line.startswith("ok") and "main/TOKEN" in line for line in lines)


def test_check_exits_one_on_an_invalid_file_and_says_why(project):
    project.write(spec(agents={"coder": agent(workspaces=["nope"])}))
    result = project.sh("project check")
    assert result.returncode == 1
    assert "nope" in result.stdout + result.stderr


@pytest.mark.parametrize("value, word", [(None, "missing"), ("", "missing")])
def test_check_exits_one_when_an_environment_secret_is_missing_or_empty(project, value, word):
    project.write(spec(vaults=secret_vault("TOKEN")))
    result = project.sh("project check", env={} if value is None else {"TOKEN": value})
    assert result.returncode == 1, result.stdout
    line = next(line for line in result.stdout.splitlines() if "main/TOKEN" in line)
    assert line.startswith("fail") and word in line


def test_check_never_prints_a_secret_value(project):
    project.write(spec(vaults=secret_vault("TOKEN")))
    result = project.sh("project check", env={"TOKEN": "hunter2-the-value"})
    assert "hunter2-the-value" not in result.stdout + result.stderr


def test_check_with_a_pass_entry_that_does_not_exist_exits_one(project):
    project.write(spec(vaults={"main": {"backend": "pass", "secrets": ["no/such/entry"]}}))
    assert project.sh("project check").returncode == 1


def test_check_resolved_prints_the_configuration_and_does_not_read_secrets(project):
    project.write(spec(vaults=secret_vault("TOKEN"), workspaces={"foo": {}}))
    result = project.sh("project check --resolved", env={})
    assert result.returncode == 0, result.stderr
    assert "path" not in result.yaml()["workspaces"]["foo"]


# --- system check ------------------------------------------------------------------------------------------


def test_system_check_needs_no_project_file(tmp_path, engine, egzo):
    result = egzo.run("system", "check", cwd=tmp_path, env=engine.env)
    assert result.returncode == 0, result.stdout + result.stderr
    assert any(line.startswith("ok") and "engine" in line for line in result.stdout.splitlines())


def test_system_check_says_what_it_found_about_git_in_the_image_and_asks_nothing_of_the_host(tmp_path, engine, egzo):
    out = egzo.run("system", "check", cwd=tmp_path, env=engine.env).stdout
    assert not re.search(r"^(ok|warn|fail)\s+git.*host", out, re.M), out
    assert re.search(r"^(ok|warn|fail)\s+git.*(image|prep)", out, re.M), out


def test_system_check_does_not_check_the_project(project, engine):
    project.write(spec(agents={"coder": agent(workspaces=["nope"])}))
    result = project.sh("system check", env=engine.env)
    assert result.returncode == 0 and "egzo.yaml" not in result.stdout


