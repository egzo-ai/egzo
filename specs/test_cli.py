# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""The shape of the command line: commands grouped by what they act on.

    egzo project   init | up | down | check | rotate-ca | unlock
    egzo agent     spawn | ls | rm | start | stop | attach
    egzo message   send | ls | reply            (alias: msg)
    egzo workspace ls | put | get | fetch | rm
    egzo system    check
    top level      ps, logs, start, stop, restart, exec, events, proxy, version, completion

There are no aliases for the commands that moved: egzo is pre-1.0.
"""

import json
import re

import pytest

from conftest import LABEL_PREFIX
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
    return [row["NAME"] for row in table(project.sh("agent ls").stdout)]


# --- the tree ----------------------------------------------------------------------------------------------


def test_the_help_lists_the_groups_and_the_commands_that_stay_at_the_top(project):
    out = project.sh("--help").stdout
    for name in GROUPS + TOP_LEVEL:
        assert re.search(rf"^\s+{name}\s", out, re.M), (name, out)


@pytest.mark.parametrize(
    "group, commands",
    [
        ("project", ["init", "up", "down", "check", "rotate-ca", "unlock"]),
        ("agent", ["spawn", "ls", "rm", "start", "stop", "attach"]),
        ("message", ["send", "ls", "reply"]),
        ("workspace", ["ls", "put", "get", "fetch", "rm"]),
        ("system", ["check"]),
    ],
)
def test_every_group_lists_its_commands(project, group, commands):
    out = project.run(group, "--help").stdout
    for command in commands:
        assert re.search(rf"^\s+{command}\s", out, re.M), (group, command, out)


def test_msg_is_an_alias_of_message(project):
    assert project.sh("msg --help").stdout == project.sh("message --help").stdout


# --- project check -----------------------------------------------------------------------------------------


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


def test_drift_is_shown_but_is_not_an_error(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.write(spec(agents={"coder": custom(agent_image), "second": custom(agent_image)}))
    secrets = {"ANTHROPIC_API_KEY": "a", "GITHUB_TOKEN": "b", "DEPLOY_TOKEN": "c"}
    result = live_project.sh("project check", env=secrets)
    assert result.returncode == 0, result.stdout + result.stderr
    assert any(line.startswith("warn") and "up --dry-run" in line for line in result.stdout.splitlines()), result.stdout


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


# --- agent ls, rm, start and stop --------------------------------------------------------------------------


def test_agent_ls_lists_the_instances_and_not_the_sidecars(live_project, engine, agent_image):
    started(live_project, agent_image)
    assert instance_names(live_project) == []
    live_project.spawn("coder")
    live_project.spawn("coder", "issue-7")
    assert sorted(instance_names(live_project)) == ["coder-1", "issue-7"]
    ps_names = [row["NAME"] for row in table(live_project.sh("ps").stdout)]
    assert any(name.endswith("control-1") for name in ps_names) and any(name.endswith("proxy-1") for name in ps_names)


def test_agent_ls_json_is_a_list_of_the_instances(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder")
    listed = json.loads(live_project.sh("agent ls --json").stdout)
    assert [row["name"] for row in listed] == ["coder-1"]


def test_agent_rm_takes_names_or_filters_and_not_both_or_neither(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder")
    neither = live_project.sh("agent rm")
    assert neither.returncode != 0 and "--stale" in neither.stderr
    both = live_project.sh("agent rm coder-1 --stopped")
    assert both.returncode != 0 and "not both" in both.stderr
    assert instance_names(live_project) == ["coder-1"]


def test_agent_stop_and_start_are_the_top_level_commands(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder")
    assert live_project.sh("agent stop coder-1").returncode == 0
    assert engine.instance(live_project.name, "coder-1").raw["State"]["Running"] is False
    assert live_project.sh("agent start coder-1").returncode == 0
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
    first = live_project.run("message", "send", "coder-1", "for the coder").stdout.split()[1]
    second = live_project.run("message", "send", "reviewer-1", "for the reviewer").stdout.split()[1]
    assert set(listed(live_project, "--to", "coder-1")) == {first}
    assert set(listed(live_project, "--to", "agent:reviewer-1")) == {second}
    assert set(listed(live_project, "--from", "operator")) == {first, second}
    assert listed(live_project, "--from", "coder-1") == {}


def test_message_ls_filters_by_kind_and_by_state(live_project, engine, agent_image):
    two_agents(live_project, agent_image)
    sent = live_project.run("message", "send", "coder-1", "do it").stdout.split()[1]
    assert set(listed(live_project, "--kind", "request")) == {sent}
    assert listed(live_project, "--kind", "question") == {}
    assert set(listed(live_project, "--state", "pending")) == {sent}
    assert listed(live_project, "--state", "resolved") == {}
    assert live_project.sh("agent rm --force coder-1").returncode == 0  # fails what it still owed
    assert sent in listed(live_project, "--state", "resolved")
    assert sent not in listed(live_project, "--state", "pending")


def test_message_ls_refuses_a_filter_value_it_does_not_know(project):
    for flag, value in (("--kind", "rumor"), ("--state", "maybe")):
        result = project.run("message", "ls", flag, value)
        assert result.returncode != 0 and value in result.stderr


def test_message_reply_answers_what_message_ls_lists(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder")
    sent = live_project.sh("msg send coder-1 hello").stdout.split()[1]
    assert sent in listed(live_project)
    unknown = live_project.run("message", "reply", "m" + "0" * 32, "x")
    assert unknown.returncode != 0


# --- workspace ls, put, get, rm and fetch ------------------------------------------------------------------


def workspaces_of(project, *args, **kwargs):
    result = project.run("workspace", "ls", *args, **kwargs)
    assert result.returncode == 0, result.stderr
    return {row["NAME"]: row for row in table(result.stdout)}


GIT = {"git": {"url": "https://github.com/octocat/Hello-World.git"}}


def test_workspace_ls_shows_every_workspace_with_its_volume(live_project, engine):
    live_project.write(spec(workspaces={"docs": {}, "scratch": {}, "repo": GIT}))
    rows = workspaces_of(live_project)
    assert set(rows) == {"docs", "scratch", "repo"}
    assert rows["docs"]["VOLUME"] == live_project.volume("docs")
    assert rows["repo"]["VOLUME"] == live_project.base_volume("repo")
    assert (rows["docs"]["KIND"], rows["repo"]["KIND"]) == ("directory", "git")
    assert "PATH" not in rows["docs"]


def test_workspace_ls_says_whether_a_directory_exists_and_whether_a_base_is_cloned(live_project, engine, agent_image):
    live_project.write(spec(egress={"default": {"allow": ["example.com"]}}, workspaces={"docs": {}, "repo": GIT},
                            agents={"coder": custom(agent_image, workspaces=["docs", "repo"])}))
    rows = workspaces_of(live_project)
    assert rows["docs"]["STATE"] == "missing" and rows["repo"]["STATE"] == "not cloned"
    live_project.up()  # github.com is not allowed: the clone fails and up goes on
    rows = workspaces_of(live_project)
    assert rows["docs"]["STATE"] == "exists" and rows["repo"]["STATE"] == "not cloned"


def test_workspace_ls_needs_the_engine_and_says_so(project):
    project.write(spec(workspaces={"docs": {}}))
    result = project.sh("workspace ls", env={"DOCKER_HOST": "unix:///nonexistent.sock"})
    assert result.returncode != 0 and "engine" in result.stderr.lower()


def test_workspace_ls_json(live_project, engine):
    live_project.write(spec(workspaces={"docs": {}}))
    [row] = json.loads(live_project.sh("workspace ls --json").stdout)
    assert (row["name"], row["kind"], row["volume"], row["state"]) == ("docs", "directory", live_project.volume("docs"), "missing")
    assert row["worktrees"] == []


@pytest.mark.usefixtures("github")
def test_workspace_ls_names_the_worktrees_of_a_git_workspace(live_project, engine, agent_image):
    live_project.up(spec(egress={"default": {"allow": ["github.com"]}}, workspaces={"repo": GIT}, agents={"coder": custom(agent_image, workspaces=["repo"])}))
    live_project.spawn("coder")
    live_project.spawn("coder")
    assert workspaces_of(live_project)["repo"]["WORKTREES"] == "coder-1,coder-2"
    assert workspaces_of(live_project)["repo"]["STATE"] == "cloned"


def written(project, name, text):
    path = project.root / "inbox" / name
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text)
    return path


def test_workspace_put_copies_a_file_and_a_directory_in_for_the_agent_user(live_project, engine, agent_image):
    import os

    live_project.up(spec(workspaces={"docs": {}}, agents={"coder": custom(agent_image, workspaces=["docs"])}))
    written(live_project, "a.txt", "alpha\n")
    written(live_project, "sub/b.txt", "beta\n")
    assert live_project.sh("workspace put docs inbox/a.txt a.txt").returncode == 0
    assert live_project.sh("workspace put docs inbox/sub sub").returncode == 0
    live_project.spawn("coder")
    name = engine.instance(live_project.name, "coder-1").name
    assert engine.exec(name, "sh", "-c", "cat /workspace/docs/a.txt /workspace/docs/sub/b.txt").stdout == "alpha\nbeta\n"
    owner = engine.in_volumes({live_project.volume("docs"): "/w"}, "stat -c %u:%g /w/a.txt /w/sub /w/sub/b.txt")
    assert set(owner.stdout.split()) == {f"{os.getuid()}:{os.getgid()}"}
    assert engine.exec(name, "sh", "-c", "echo more >> /workspace/docs/a.txt").returncode == 0, "the agent can write what was put"


def test_workspace_put_works_before_up_and_up_then_keeps_what_was_put(live_project, engine, agent_image):
    live_project.write(spec(workspaces={"docs": {}}, agents={"coder": custom(agent_image, workspaces=["docs"])}))
    written(live_project, "a.txt", "alpha\n")
    put = live_project.sh("workspace put docs inbox/a.txt a.txt")
    assert put.returncode == 0, put.stderr
    live_project.up()
    live_project.spawn("coder")
    assert engine.exec(engine.instance(live_project.name, "coder-1").name, "cat", "/workspace/docs/a.txt").stdout == "alpha\n"


def test_workspace_get_copies_a_path_out_and_what_the_agent_wrote(live_project, engine, agent_image, tmp_path):
    live_project.up(spec(workspaces={"docs": {}}, agents={"coder": custom(agent_image, workspaces=["docs"])}))
    live_project.spawn("coder")
    name = engine.instance(live_project.name, "coder-1").name
    assert engine.exec(name, "sh", "-c", "mkdir /workspace/docs/out && echo result > /workspace/docs/out/r.txt").returncode == 0
    got = live_project.run("workspace", "get", "docs", "out", str(tmp_path / "copy"))
    assert got.returncode == 0, got.stderr
    assert (tmp_path / "copy" / "r.txt").read_text() == "result\n"
    everything = live_project.run("workspace", "get", "docs", str(tmp_path / "all"))
    assert everything.returncode == 0, everything.stderr
    assert (tmp_path / "all" / "out" / "r.txt").read_text() == "result\n"


@pytest.mark.parametrize("destination", ["../escape.txt", "/etc/escape.txt", "a/../../escape.txt"])
def test_workspace_put_refuses_a_destination_outside_the_workspace(live_project, engine, destination):
    live_project.write(spec(workspaces={"docs": {}}))
    written(live_project, "a.txt", "alpha\n")
    result = live_project.run("workspace", "put", "docs", "inbox/a.txt", destination)
    assert result.returncode != 0 and "outside" in result.stderr


def test_workspace_put_and_get_refuse_names_that_are_not_directory_workspaces(live_project, engine, tmp_path):
    live_project.write(spec(workspaces={"docs": {}, "repo": GIT}))
    written(live_project, "a.txt", "alpha\n")
    for command in (["put", "repo", "inbox/a.txt", "a.txt"], ["put", "ghost", "inbox/a.txt", "a.txt"], ["get", "ghost", str(tmp_path / "x")]):
        result = live_project.run("workspace", *command)
        assert result.returncode != 0 and command[1] in result.stderr, command
    assert "git" in live_project.sh("workspace put repo inbox/a.txt a.txt").stderr


@pytest.mark.usefixtures("github")
def test_workspace_get_reads_the_worktree_of_an_instance(live_project, engine, agent_image, tmp_path):
    live_project.up(spec(egress={"default": {"allow": ["github.com"]}}, workspaces={"repo": GIT}, agents={"coder": custom(agent_image, workspaces=["repo"])}))
    live_project.spawn("coder")
    got = live_project.run("workspace", "get", "repo/coder-1", "README", str(tmp_path / "README"))
    assert got.returncode == 0, got.stderr
    assert (tmp_path / "README").read_text().startswith("Hello World")


def test_workspace_rm_removes_a_directory_volume_and_refuses_one_an_instance_mounts(live_project, engine, agent_image):
    live_project.up(spec(workspaces={"docs": {}, "other": {}}, agents={"coder": custom(agent_image, workspaces=["docs"])}))
    live_project.spawn("coder")
    refused = live_project.sh("workspace rm docs")
    assert refused.returncode != 0 and "coder-1" in refused.stderr
    assert engine.volume_exists(live_project.volume("docs"))
    assert live_project.sh("workspace rm other").returncode == 0
    assert not engine.volume_exists(live_project.volume("other"))
    assert live_project.sh("workspace rm ghost").returncode != 0


def test_workspace_rm_all_removes_every_workspace_volume(live_project, engine, agent_image):
    live_project.up(spec(workspaces={"docs": {}, "other": {}}, agents={"coder": custom(agent_image)}))
    result = live_project.sh("workspace rm --all")
    assert result.returncode == 0, result.stderr
    assert not [r for r in engine.resources(live_project.name) if r.name.startswith(f"{live_project.name}_ws.")]


def test_project_unlock_breaks_a_lease_left_by_a_killed_command(live_project, engine, agent_image):
    live_project.up(spec(agents={"coder": custom(agent_image)}))
    labels = ["--label", f"{LABEL_PREFIX}project={live_project.name}", "--label", f"{LABEL_PREFIX}kind=lock",
              "--label", f"{LABEL_PREFIX}lock-holder=killed-1234", "--label", f"{LABEL_PREFIX}lock-expires=9999999999"]
    assert engine.run("create", "--name", f"{live_project.name}.lock", *labels, engine.agent_image()).returncode == 0
    assert live_project.sh("project down").returncode != 0, "up and down refuse at once while the lease is held"
    unlocked = live_project.sh("project unlock")
    assert unlocked.returncode == 0 and "killed-1234" in unlocked.stdout
    assert live_project.spawn("coder", "free") == "free"


def test_workspace_fetch_needs_the_project_up_and_says_so(live_project, engine, agent_image):
    live_project.write(spec(egress={"default": {"allow": ["github.com"]}}, workspaces={"repo": GIT}, agents={"coder": custom(agent_image, workspaces=["repo"])}))
    result = live_project.sh("workspace fetch")
    assert result.returncode != 0 and "project up" in result.stderr


def test_workspace_fetch_refuses_a_name_that_is_not_a_git_workspace(live_project, engine, agent_image):
    live_project.up(spec(egress={"default": {"allow": ["github.com"]}}, workspaces={"repo": GIT, "docs": {}}, agents={"coder": custom(agent_image, workspaces=["repo"])}))
    result = live_project.sh("workspace fetch docs")
    assert result.returncode != 0 and "docs" in result.stderr
    ghost = live_project.sh("workspace fetch ghost")
    assert ghost.returncode != 0 and "ghost" in ghost.stderr


@pytest.mark.usefixtures("github")
def test_workspace_fetch_clones_a_base_that_up_could_not_and_refreshes_one_that_is_there(live_project, engine, agent_image):
    allow = {"default": {"allow": ["github.com"]}}
    live_project.up(spec(egress={"default": {"allow": ["example.com"]}}, workspaces={"repo": GIT}, agents={"coder": custom(agent_image, workspaces=["repo"])}))
    assert not engine.volume_exists(live_project.base_volume("repo"))  # up could not reach github.com
    live_project.up(spec(egress=allow, workspaces={"repo": GIT}, agents={"coder": custom(agent_image, workspaces=["repo"])}))
    assert engine.run("volume", "rm", live_project.base_volume("repo")).returncode == 0  # the clone made by this up goes: the next command has to make it again
    result = live_project.sh("workspace fetch", timeout=300)
    assert result.returncode == 0, result.stderr
    assert engine.in_volumes({live_project.base_volume("repo"): "/b"}, "test -d /b/.git").returncode == 0
    again = live_project.sh("workspace fetch repo", timeout=300)
    assert again.returncode == 0, again.stderr
