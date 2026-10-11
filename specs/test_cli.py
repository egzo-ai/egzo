# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""The shape of the command line: commands grouped by what they act on.

    egzo project   init | up | down | check | rotate-ca
    egzo agent     spawn | ls | rm | start | stop | attach
    egzo message   send | ls | reply            (alias: msg)
    egzo workspace ls | fetch
    egzo system    check
    top level      ps, logs, start, stop, restart, exec, events, proxy, version, completion

There are no aliases for the commands that moved: egzo is pre-1.0.
"""

import json
import re
import shutil

import pytest

from support import agent, spec, table

GROUPS = ["project", "agent", "message", "workspace", "system"]
TOP_LEVEL = ["ps", "logs", "start", "stop", "restart", "exec", "events", "proxy", "version", "completion"]
REMOVED = [
    "up", "down", "init", "spawn", "rm", "prune", "diff", "doctor", "config", "secrets", "ca",
    "send", "messages", "questions", "answer", "attach",
]


def custom(image, **fields):
    return agent(harness="custom", image=image, **fields)


def started(project, image, **agents):
    project.up(spec(agents=agents or {"coder": custom(image)}))


def instance_names(project):
    return [row["NAME"] for row in table(project.run("agent", "ls").stdout)]


# --- the tree ----------------------------------------------------------------------------------------------


def test_the_help_lists_the_groups_and_the_commands_that_stay_at_the_top(project):
    out = project.run("--help").stdout
    for name in GROUPS + TOP_LEVEL:
        assert re.search(rf"^\s+{name}\s", out, re.M), (name, out)


@pytest.mark.parametrize("name", REMOVED)
def test_a_command_that_moved_is_unknown_and_has_no_alias(project, name):
    result = project.run(name)
    assert result.returncode != 0
    assert f'unknown command "{name}"' in result.stderr


@pytest.mark.parametrize(
    "group, commands",
    [
        ("project", ["init", "up", "down", "check", "rotate-ca"]),
        ("agent", ["spawn", "ls", "rm", "start", "stop", "attach"]),
        ("message", ["send", "ls", "reply"]),
        ("workspace", ["ls", "fetch"]),
        ("system", ["check"]),
    ],
)
def test_every_group_lists_its_commands(project, group, commands):
    out = project.run(group, "--help").stdout
    for command in commands:
        assert re.search(rf"^\s+{command}\s", out, re.M), (group, command, out)


def test_msg_is_an_alias_of_message(project):
    assert project.run("msg", "--help").stdout == project.run("message", "--help").stdout


# --- project check -----------------------------------------------------------------------------------------


def secret_vault(*names):
    return {"main": {"backend": "env", "secrets": list(names)}}


def test_check_of_a_valid_project_prints_ok_lines_and_exits_zero(project):
    project.write(spec(vaults=secret_vault("TOKEN")))
    result = project.run("project", "check", env={"TOKEN": "a-secret"})
    assert result.returncode == 0, result.stdout + result.stderr
    lines = [line for line in result.stdout.splitlines() if line.strip()]
    assert all(re.match(r"(ok|warn|fail)\s", line) for line in lines), result.stdout
    assert any(line.startswith("ok") and "egzo.yaml" in line for line in lines)
    assert any(line.startswith("ok") and "main/TOKEN" in line for line in lines)


def test_check_exits_one_on_an_invalid_file_and_says_why(project):
    project.write(spec(agents={"coder": agent(workspaces=["nope"])}))
    result = project.run("project", "check")
    assert result.returncode == 1
    assert "nope" in result.stdout + result.stderr


@pytest.mark.parametrize("value, word", [(None, "missing"), ("", "empty")])
def test_check_exits_one_when_a_secret_is_missing_or_empty(project, value, word):
    project.write(spec(vaults=secret_vault("TOKEN")))
    result = project.run("project", "check", env={} if value is None else {"TOKEN": value})
    assert result.returncode == 1, result.stdout
    line = next(line for line in result.stdout.splitlines() if "main/TOKEN" in line)
    assert line.startswith("fail") and word in line


def test_check_never_prints_a_secret_value(project):
    project.write(spec(vaults=secret_vault("TOKEN")))
    result = project.run("project", "check", env={"TOKEN": "hunter2-the-value"})
    assert "hunter2-the-value" not in result.stdout + result.stderr


def test_check_with_a_pass_entry_that_does_not_exist_exits_one(project):
    project.write(spec(vaults={"main": {"backend": "pass", "secrets": ["no/such/entry"]}}))
    assert project.run("project", "check").returncode == 1


def test_check_resolved_prints_the_configuration_and_does_not_read_secrets(project):
    project.write(spec(vaults=secret_vault("TOKEN"), workspaces={"foo": {}}))
    result = project.run("project", "check", "--resolved", env={})
    assert result.returncode == 0, result.stderr
    assert result.yaml()["workspaces"]["foo"]["path"].endswith("workspaces/foo")


def test_drift_is_shown_but_is_not_an_error(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.write(spec(agents={"coder": custom(agent_image), "second": custom(agent_image)}))
    result = live_project.run("project", "check")
    assert result.returncode == 0, result.stdout + result.stderr


# --- system check ------------------------------------------------------------------------------------------


def test_system_check_needs_no_project_file(tmp_path, engine, egzo):
    result = egzo.run("system", "check", cwd=tmp_path, env=engine.env)
    assert result.returncode == 0, result.stdout + result.stderr
    assert any(line.startswith("ok") and "engine" in line for line in result.stdout.splitlines())


def test_system_check_says_what_it_found_about_git(tmp_path, engine, egzo):
    out = egzo.run("system", "check", cwd=tmp_path, env=engine.env).stdout
    assert re.search(r"^(ok|warn|fail)\s+git.*host", out, re.M), out
    assert re.search(r"^(ok|warn|fail)\s+git.*(image|prep)", out, re.M), out


def test_system_check_does_not_check_the_project(project, engine):
    project.write(spec(agents={"coder": agent(workspaces=["nope"])}))
    result = project.run("system", "check", env=engine.env)
    assert result.returncode == 0 and "egzo.yaml" not in result.stdout


# --- agent ls, rm, start and stop --------------------------------------------------------------------------


def test_agent_ls_lists_the_instances_and_not_the_sidecars(live_project, engine, agent_image):
    started(live_project, agent_image)
    assert instance_names(live_project) == []
    live_project.spawn("coder")
    live_project.spawn("coder", "issue-7")
    assert sorted(instance_names(live_project)) == ["coder-1", "issue-7"]
    assert {"control", "proxy"} <= {row["NAME"] for row in table(live_project.run("ps").stdout)}


def test_agent_ls_json_is_a_list_of_the_instances(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder")
    listed = json.loads(live_project.run("agent", "ls", "--json").stdout)
    assert [row["name"] for row in listed] == ["coder-1"]


def test_agent_rm_takes_names_or_filters_and_not_both_or_neither(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder")
    neither = live_project.run("agent", "rm")
    assert neither.returncode != 0 and "--stale" in neither.stderr
    both = live_project.run("agent", "rm", "coder-1", "--stopped")
    assert both.returncode != 0 and "not both" in both.stderr
    assert instance_names(live_project) == ["coder-1"]


def test_agent_stop_and_start_are_the_top_level_commands(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder")
    assert live_project.run("agent", "stop", "coder-1").returncode == 0
    assert engine.instance(live_project.name, "coder-1").raw["State"]["Running"] is False
    assert live_project.run("agent", "start", "coder-1").returncode == 0
    assert engine.instance(live_project.name, "coder-1").raw["State"]["Running"] is True


# --- message -----------------------------------------------------------------------------------------------


def two_agents(project, image):
    started(project, image, coder=custom(image), reviewer=custom(image))
    project.spawn("coder")
    project.spawn("reviewer")


def listed(project, *args):
    result = project.run("message", "ls", *args)
    assert result.returncode == 0, result.stderr
    return {row["ID"]: row for row in table(result.stdout)}


def test_message_ls_filters_by_recipient_and_by_sender(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    first = live_project.run("message", "send", "coder", "for the coder").stdout.split()[1]
    second = live_project.run("message", "send", "reviewer", "for the reviewer").stdout.split()[1]
    assert set(listed(live_project, "--to", "coder")) == {first}
    assert set(listed(live_project, "--to", "agent:reviewer")) == {second}
    assert set(listed(live_project, "--from", "operator")) == {first, second}
    assert listed(live_project, "--from", "coder") == {}


def test_message_ls_filters_by_kind_and_by_state(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    sent = live_project.run("message", "send", "coder", "do it").stdout.split()[1]
    assert set(listed(live_project, "--kind", "request")) == {sent}
    assert listed(live_project, "--kind", "question") == {}
    assert set(listed(live_project, "--state", "pending")) == {sent}
    assert listed(live_project, "--state", "resolved") == {}
    assert live_project.run("agent", "rm", "--force", "coder-1").returncode == 0  # fails what it still owed
    assert sent in listed(live_project, "--state", "resolved")
    assert sent not in listed(live_project, "--state", "pending")


def test_message_ls_refuses_a_filter_value_it_does_not_know(project):
    for flag, value in (("--kind", "rumor"), ("--state", "maybe")):
        result = project.run("message", "ls", flag, value)
        assert result.returncode != 0 and value in result.stderr


def test_message_reply_answers_what_message_ls_lists(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder")
    sent = live_project.run("msg", "send", "coder", "hello").stdout.split()[1]
    assert sent in listed(live_project)
    unknown = live_project.run("message", "reply", "m" + "0" * 32, "x")
    assert unknown.returncode != 0


# --- workspace ls and fetch --------------------------------------------------------------------------------


def workspaces_of(project, *args, **kwargs):
    result = project.run("workspace", "ls", *args, **kwargs)
    assert result.returncode == 0, result.stderr
    return {row["NAME"]: row for row in table(result.stdout)}


GIT = {"git": {"url": "https://github.com/octocat/Hello-World.git"}}


def test_workspace_ls_works_without_an_engine_and_shows_every_workspace_with_its_path(project):
    project.write(spec(workspaces={"docs": {"path": "./docs"}, "scratch": {}, "repo": GIT}))
    rows = workspaces_of(project, env={"DOCKER_HOST": "unix:///nonexistent.sock"})
    assert set(rows) == {"docs", "scratch", "repo"}
    assert rows["docs"]["PATH"].endswith("/docs") and rows["scratch"]["PATH"].endswith("/workspaces/scratch")
    assert rows["repo"]["PATH"].endswith("/workspaces/repo")
    assert (rows["docs"]["KIND"], rows["repo"]["KIND"]) == ("directory", "git")


def test_workspace_ls_says_whether_a_directory_exists_and_whether_a_base_is_cloned(project):
    project.write(spec(workspaces={"docs": {"path": "./docs"}, "repo": GIT}))
    rows = workspaces_of(project)
    assert rows["docs"]["STATE"] == "missing" and rows["repo"]["STATE"] == "not cloned"
    (project.root / "docs").mkdir()
    (project.root / "workspaces" / "repo" / ".base" / ".git").mkdir(parents=True)
    rows = workspaces_of(project)
    assert rows["docs"]["STATE"] == "exists" and rows["repo"]["STATE"] == "cloned"


def test_workspace_ls_names_the_worktrees_of_a_git_workspace(project):
    project.write(spec(workspaces={"repo": GIT}))
    meta = project.root / "workspaces" / "repo" / ".base" / ".git" / "worktrees"
    for name in ("coder-1", "coder-2"):
        (meta / name).mkdir(parents=True)
    rows = workspaces_of(project)
    assert rows["repo"]["WORKTREES"] == "coder-1,coder-2"


def test_workspace_ls_json(project):
    project.write(spec(workspaces={"docs": {"path": "./docs"}}))
    listed_json = json.loads(project.run("workspace", "ls", "--json").stdout)
    assert [(w["name"], w["kind"]) for w in listed_json] == [("docs", "directory")]


def test_workspace_fetch_needs_the_project_up_and_says_so(live_project, engine, agent_image):
    live_project.write(spec(egress={"default": {"allow": ["github.com"]}}, workspaces={"repo": GIT}, agents={"coder": custom(agent_image, workspaces=["repo"])}))
    result = live_project.run("workspace", "fetch")
    assert result.returncode != 0 and "project up" in result.stderr


def test_workspace_fetch_refuses_a_name_that_is_not_a_git_workspace(live_project, engine, agent_image):
    live_project.up(spec(egress={"default": {"allow": ["github.com"]}}, workspaces={"repo": GIT, "docs": {}}, agents={"coder": custom(agent_image, workspaces=["repo"])}))
    result = live_project.run("workspace", "fetch", "docs")
    assert result.returncode != 0 and "docs" in result.stderr
    ghost = live_project.run("workspace", "fetch", "ghost")
    assert ghost.returncode != 0 and "ghost" in ghost.stderr


@pytest.mark.usefixtures("github")
def test_workspace_fetch_clones_a_base_that_up_could_not_and_refreshes_one_that_is_there(live_project, engine, agent_image):
    allow = {"default": {"allow": ["github.com"]}}
    live_project.up(spec(egress={"default": {"allow": ["example.com"]}}, workspaces={"repo": GIT}, agents={"coder": custom(agent_image, workspaces=["repo"])}))
    base = live_project.root / "workspaces" / "repo" / ".base"
    assert not base.exists()  # up could not reach github.com
    live_project.up(spec(egress=allow, workspaces={"repo": GIT}, agents={"coder": custom(agent_image, workspaces=["repo"])}))
    shutil.rmtree(base)  # the clone made by this up goes: the next command has to make it again
    result = live_project.run("workspace", "fetch", timeout=300)
    assert result.returncode == 0, result.stderr
    assert (base / ".git").exists()
    again = live_project.run("workspace", "fetch", "repo", timeout=300)
    assert again.returncode == 0, again.stderr
