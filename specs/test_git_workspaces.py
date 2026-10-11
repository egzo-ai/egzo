# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""Git workspaces: `up` clones a base, `spawn` gives each instance its own worktree of it.

Everything lives in engine volumes, never on the host:

    <project>_ws.repo.base          the base clone, made by `up` (no files checked out), at /workspace/repo/.base
    <project>_ws.repo.wt.coder-1    the worktree of the instance coder-1 on the branch egzo/coder-1, at /workspace/repo/coder-1

Inside the container the two are siblings, so the worktree's relative links resolve. The specs look inside the volumes
with a throwaway container (`engine.in_volumes`), since the host cannot browse them.

The specs clone a small public repository through the project's proxy. No credential is ever handed to the CLI or
the agent.
"""

import json
import os
import subprocess

import pytest

from conftest import LABEL_PREFIX
from support import agent, spec

REPO = "https://github.com/octocat/Hello-World.git"
OTHER_REPO = "https://github.com/octocat/Spoon-Knife.git"

pytestmark = pytest.mark.usefixtures("github")


def custom(image, **fields):
    return agent(harness="custom", image=image, **fields)


def project_spec(image, *, workspace=None, agents=None, allow=("github.com",), extra=None):
    workspaces = {"repo": {"git": {"url": REPO}, **(workspace or {})}, **(extra or {})}
    agents = agents or {"coder": custom(image, workspaces=["repo"])}
    return spec(egress={"default": {"allow": list(allow)}}, workspaces=workspaces, agents=agents)


def up_ok(project, document):
    project.write(document)
    result = project.run("project", "up", timeout=400)
    assert result.returncode == 0, result.stderr
    return result


def spawn(project, template="coder", name=None, *args):
    command = ["agent", "spawn", template, *([name] if name else []), *args]
    return project.run(*command, timeout=400)


def spawn_ok(project, template="coder", name=None, *args):
    result = spawn(project, template, name, *args)
    assert result.returncode == 0, result.stderr
    return result.stdout.strip()


def container(engine, project, instance):
    return engine.instance(project.name, instance)


def in_agent(engine, project, name, command):
    return engine.exec(container(engine, project, name).name, "sh", "-c", command)


def remote_head(ref):
    return subprocess.run(["git", "ls-remote", REPO, ref], capture_output=True, text=True, check=True).stdout.split()[0]


def shell(engine, project, command, instance=None, workspace="repo"):
    """Run a shell command in a throwaway container that mounts the base and, with an instance, its worktree where
    an agent has them. Returns the result; the working directory is the worktree, or the base."""
    mounts = {project.base_volume(workspace): f"/workspace/{workspace}/.base"}
    where = f"/workspace/{workspace}/.base"
    if instance:
        where = f"/workspace/{workspace}/{instance}"
        mounts[project.worktree_volume(workspace, instance)] = where
    return engine.in_volumes(mounts, f"git config --global --add safe.directory '*' && {command}", workdir=where)


def git(engine, project, *args, instance=None, workspace="repo"):
    quoted = " ".join("'" + a.replace("'", "'\\''") + "'" for a in args)
    result = shell(engine, project, f"git {quoted}", instance, workspace)
    assert result.returncode == 0, result.stderr
    return result.stdout.strip()


def base_exists(engine, project):
    return engine.volume_exists(project.base_volume("repo"))


def worktree_exists(engine, project, instance="coder-1"):
    return engine.volume_exists(project.worktree_volume("repo", instance))


def has_file(engine, project, path, instance="coder-1"):
    return shell(engine, project, f"test -e {path}", instance).returncode == 0


def two_templates(image):
    return {"coder": custom(image, workspaces=["repo"]), "reviewer": custom(image, workspaces=["repo"])}


def audit(project):
    return [json.loads(line) for line in project.run("proxy", "log").stdout.splitlines() if line.startswith("{")]


# --- up makes the base ---------------------------------------------------------------------------------------


def test_up_clones_the_base_without_checking_anything_out_and_starts_no_agent(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    assert git(engine, live_project, "remote", "get-url", "origin") == REPO
    assert not has_file(engine, live_project, "/workspace/repo/.base/README", instance=None), "the base holds no working files"
    assert not worktree_exists(engine, live_project)
    assert engine.instances(live_project.name) == []


def test_the_base_is_a_workspace_volume_that_belongs_to_the_project(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    [volume] = [r for r in engine.resources(live_project.name) if r.name == live_project.base_volume("repo")]
    assert volume.labels[f"{LABEL_PREFIX}kind"] == "workspace" and volume.labels[f"{LABEL_PREFIX}service"] == "repo"
    assert f"{LABEL_PREFIX}instance" not in volume.labels


def test_the_base_belongs_to_the_invoking_user(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    owner = shell(engine, live_project, "stat -c %u /workspace/repo/.base/.git/HEAD")
    assert owner.stdout.strip() == str(os.getuid())


def test_a_second_up_leaves_the_base_alone(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    assert shell(engine, live_project, "echo mine > .git/marker").returncode == 0
    up_ok(live_project, project_spec(agent_image))
    assert shell(engine, live_project, "cat .git/marker").stdout == "mine\n"


def test_up_only_clones_workspaces_that_an_agent_lists(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image, extra={"unused": {"git": {"url": REPO}}}))
    assert base_exists(engine, live_project)
    assert not engine.volume_exists(live_project.base_volume("unused"))


def test_a_failed_clone_at_up_is_reported_and_does_not_fail_up(live_project, engine, agent_image):
    result = up_ok(live_project, project_spec(agent_image, allow=("example.com",)))
    assert "github.com" in result.stderr and "repo" in result.stderr
    assert not base_exists(engine, live_project), "an empty base volume is not left behind"
    assert [r.name for r in engine.containers(live_project.name)], "the project itself is up"


def test_a_base_that_does_not_match_the_file_is_reported_and_left_alone(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    changed = project_spec(agent_image, workspace={"git": {"url": OTHER_REPO}})
    result = up_ok(live_project, changed)
    assert "repo" in result.stderr and "url" in result.stderr
    assert git(engine, live_project, "remote", "get-url", "origin") == REPO


def test_nothing_is_made_in_the_project_directory(live_project, engine, agent_image):
    live_project.write(project_spec(agent_image))
    before = live_project.left_in_directory()
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    assert live_project.left_in_directory() == before


# --- spawn makes a worktree ----------------------------------------------------------------------------------


def test_spawn_gives_the_instance_a_worktree_volume_on_its_own_branch(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    assert spawn_ok(live_project) == "coder-1"
    assert has_file(engine, live_project, "README")
    assert git(engine, live_project, "branch", "--show-current", instance="coder-1") == "egzo/coder-1"
    assert git(engine, live_project, "rev-parse", "HEAD", instance="coder-1") == remote_head("HEAD")
    assert "README" in in_agent(engine, live_project, "coder-1", "ls /workspace/repo/coder-1").stdout


def test_the_worktree_volume_belongs_to_the_instance(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    [volume] = [r for r in engine.resources(live_project.name) if r.name == live_project.worktree_volume("repo", "coder-1")]
    labels = {k.removeprefix(LABEL_PREFIX): v for k, v in volume.labels.items()}
    assert (labels["kind"], labels["service"], labels["instance"], labels["actor"]) == ("workspace", "coder", "coder-1", "operator")
    assert labels["template-hash"] == container(engine, live_project, "coder-1").labels[f"{LABEL_PREFIX}template-hash"]


def test_spawn_clones_the_base_itself_when_it_is_missing(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    assert engine.run("volume", "rm", live_project.base_volume("repo")).returncode == 0
    spawn_ok(live_project)
    assert base_exists(engine, live_project) and has_file(engine, live_project, "README")


def test_the_worktree_starts_at_the_head_of_the_configured_branch(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image, workspace={"git": {"url": REPO, "branch": "test"}}))
    spawn_ok(live_project)
    assert git(engine, live_project, "rev-parse", "HEAD", instance="coder-1") == remote_head("refs/heads/test")


def test_the_container_sees_the_base_and_its_own_worktree_under_the_same_names(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image, agents=two_templates(agent_image)))
    spawn_ok(live_project, "coder")
    spawn_ok(live_project, "reviewer")
    coder = container(engine, live_project, "coder-1")
    volumes = {m["Destination"]: m["Name"] for m in coder.raw["Mounts"] if m["Type"] == "volume"}
    assert volumes["/workspace/repo/.base"] == live_project.base_volume("repo")
    assert volumes["/workspace/repo/coder-1"] == live_project.worktree_volume("repo", "coder-1")
    assert "/workspace/repo" not in volumes and not any("reviewer" in d for d in volumes)
    assert coder.raw["Config"]["WorkingDir"] == "/workspace/repo/coder-1"
    listing = in_agent(engine, live_project, "coder-1", "ls -A /workspace/repo").stdout.split()
    assert sorted(listing) == [".base", "coder-1"]


def test_the_worktree_links_are_relative_so_git_works_wherever_the_two_volumes_sit_side_by_side(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    assert shell(engine, live_project, "cat .git", instance="coder-1").stdout.strip() == "gitdir: ../.base/.git/worktrees/coder-1"
    assert git(engine, live_project, "status", "--short", instance="coder-1") == ""
    inside = in_agent(engine, live_project, "coder-1", "cd /workspace/repo/coder-1 && git status --short && git worktree list")
    assert inside.returncode == 0, inside.stderr
    assert "/workspace/repo/coder-1" in inside.stdout and "/workspace/repo/.base" in inside.stdout


def test_the_worktree_belongs_to_the_invoking_user_and_the_agent_can_commit(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    assert shell(engine, live_project, "stat -c %u README", instance="coder-1").stdout.strip() == str(os.getuid())
    committed = in_agent(
        engine, live_project, "coder-1",
        "cd /workspace/repo/coder-1 && git config user.email a@b.c && git config user.name a && echo x > new && git add new && git commit -qm x && git log --oneline -1",
    )
    assert committed.returncode == 0, committed.stderr
    assert git(engine, live_project, "log", "--oneline", "-1", instance="coder-1").endswith(" x")


def test_every_instance_gets_its_own_worktree_of_one_base(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image, agents=two_templates(agent_image)))
    assert spawn_ok(live_project, "coder") == "coder-1"
    assert spawn_ok(live_project, "reviewer") == "reviewer-1"
    assert spawn_ok(live_project, "coder") == "coder-2"
    assert in_agent(engine, live_project, "coder-1", "echo mine > /workspace/repo/coder-1/private").returncode == 0
    assert in_agent(engine, live_project, "coder-2", "test -e /workspace/repo/coder-2/private").returncode != 0
    assert in_agent(engine, live_project, "reviewer-1", "test -e /workspace/repo/reviewer-1/private").returncode != 0
    assert len(git(engine, live_project, "worktree", "list").splitlines()) == 4  # the base and three worktrees


def test_an_instance_name_chosen_by_the_user_names_its_worktree_and_branch(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    assert spawn_ok(live_project, "coder", "issue-412") == "issue-412"
    assert worktree_exists(engine, live_project, "issue-412")
    assert has_file(engine, live_project, "README", "issue-412")
    assert git(engine, live_project, "branch", "--show-current", instance="issue-412") == "egzo/issue-412"


def test_the_instance_name_cannot_start_with_a_dot(live_project, engine, agent_image):
    """`.base` is the base: no instance may be called that, or anything that hides in its place."""
    up_ok(live_project, project_spec(agent_image))
    result = spawn(live_project, "coder", ".base")
    assert result.returncode != 0
    assert base_exists(engine, live_project) and not has_file(engine, live_project, "/workspace/repo/.base/README", instance=None)
    assert engine.instances(live_project.name) == []


def test_the_prep_container_is_removed_and_leaves_no_trace(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    names = {r.labels.get(f"{LABEL_PREFIX}service") for r in engine.containers(live_project.name)}
    assert names == {"control", "proxy", "coder"}


def test_a_missing_base_that_cannot_be_cloned_stops_spawn_with_a_clear_error(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image, allow=("example.com",)))
    result = spawn(live_project)
    assert result.returncode != 0
    assert "github.com" in result.stderr and "repo" in result.stderr
    assert engine.instances(live_project.name) == []
    assert not [r for r in engine.resources(live_project.name) if r.name.endswith("_coder-1")], "a failed spawn left resources behind"
    assert not worktree_exists(engine, live_project) and not base_exists(engine, live_project)
    assert live_project.run("ps", "--json").returncode == 0


# --- spawn fetches -------------------------------------------------------------------------------------------


def test_the_fetch_goes_through_the_proxy_with_the_instances_identity(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project, "coder", "issue-9")
    allowed = [e for e in audit(live_project) if e.get("host") == "github.com" and e.get("action") == "allow"]
    assert "issue-9" in {e.get("agent") for e in allowed}


def test_no_fetch_does_not_touch_the_network(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project, "coder", "quiet", "--no-fetch")
    assert has_file(engine, live_project, "README", "quiet")
    assert "quiet" not in {e.get("agent") for e in audit(live_project) if e.get("host") == "github.com"}


def test_a_fetch_that_fails_warns_and_the_worktree_is_made_from_the_base_as_it_is(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    up_ok(live_project, project_spec(agent_image, allow=("example.com",)))
    result = spawn(live_project)
    assert result.returncode == 0, result.stderr
    assert "github.com" in result.stderr and "fetch" in result.stderr.lower()
    assert has_file(engine, live_project, "README")


def test_the_fetch_uses_the_url_of_the_file_and_not_the_remote_of_the_base(live_project, engine, agent_image):
    """The base may be a clone of your own whose remote is ssh, which the proxy cannot serve."""
    up_ok(live_project, project_spec(agent_image))
    git(engine, live_project, "remote", "set-url", "origin", "ssh://git@github.com/octocat/Hello-World.git")
    spawn_ok(live_project, "coder", "issue-3")
    assert "issue-3" in {e.get("agent") for e in audit(live_project) if e.get("host") == "github.com" and e.get("action") == "allow"}
    assert has_file(engine, live_project, "README", "issue-3")


# --- a worktree is never replaced ----------------------------------------------------------------------------


def test_a_worktree_volume_that_exists_makes_spawn_fail_unless_reuse_is_given(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    volume = live_project.worktree_volume("repo", "issue-1")
    labels = ["--label", f"{LABEL_PREFIX}project={live_project.name}", "--label", f"{LABEL_PREFIX}kind=workspace"]
    assert engine.run("volume", "create", *labels, volume).returncode == 0
    refused = spawn(live_project, "coder", "issue-1")
    assert refused.returncode != 0
    assert volume in refused.stderr and "--reuse" in refused.stderr
    assert engine.instances(live_project.name) == []
    assert spawn(live_project, "coder", "issue-1", "--reuse").returncode == 0
    assert has_file(engine, live_project, "README", "issue-1")


def test_committed_work_survives_rm_on_the_branch_and_reuse_continues_it(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project, "coder", "issue-1")
    git(engine, live_project, "-c", "user.email=a@b.c", "-c", "user.name=a", "commit", "--allow-empty", "-qm", "keep me", instance="issue-1")
    assert live_project.run("agent", "rm", "--force", "issue-1").returncode == 0
    assert not worktree_exists(engine, live_project, "issue-1")
    assert git(engine, live_project, "log", "-1", "--format=%s", "egzo/issue-1") == "keep me"
    refused = spawn(live_project, "coder", "issue-1")
    assert refused.returncode != 0
    assert "egzo/issue-1" in refused.stderr and "--reuse" in refused.stderr
    assert spawn(live_project, "coder", "issue-1", "--reuse").returncode == 0
    assert git(engine, live_project, "log", "-1", "--format=%s", instance="issue-1") == "keep me"
    assert git(engine, live_project, "branch", "--show-current", instance="issue-1") == "egzo/issue-1"


def test_an_existing_worktree_is_never_modified_by_another_spawn(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    assert shell(engine, live_project, "echo 'unpushed work' > wip.txt", instance="coder-1").returncode == 0
    spawn_ok(live_project, "coder", "second")
    assert shell(engine, live_project, "cat wip.txt", instance="coder-1").stdout == "unpushed work\n"


def test_an_automatic_name_skips_the_one_a_branch_was_left_under(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    assert live_project.run("agent", "rm", "--force", "coder-1").returncode == 0
    assert spawn_ok(live_project) == "coder-2", "coder-1 left the branch egzo/coder-1 behind"


# --- removing ------------------------------------------------------------------------------------------------


def test_rm_deletes_the_worktree_without_a_check_and_keeps_the_base_and_the_others(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    spawn_ok(live_project)
    assert shell(engine, live_project, "echo 'not committed, and nobody asks' > wip.txt", instance="coder-1").returncode == 0
    assert live_project.run("agent", "rm", "--force", "coder-1").returncode == 0
    assert not worktree_exists(engine, live_project, "coder-1")
    assert has_file(engine, live_project, "README", "coder-2")
    assert base_exists(engine, live_project)
    listed = git(engine, live_project, "worktree", "list")
    assert "coder-1" not in listed and "coder-2" in listed
    assert git(engine, live_project, "branch", "--list", "egzo/coder-1") != ""


def test_down_removes_the_worktrees_of_the_instances_and_keeps_the_base(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    result = live_project.run("project", "down", "--force")
    assert result.returncode == 0, result.stderr
    assert "repo" in result.stdout, "down says which workspaces it keeps"
    assert [r.name for r in engine.resources(live_project.name)] == [live_project.base_volume("repo")]
    assert git(engine, live_project, "worktree", "list").count("coder-1") == 0


def test_a_new_up_after_down_reuses_the_base_without_cloning_again(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    assert shell(engine, live_project, "echo mine > .git/marker").returncode == 0
    assert live_project.run("project", "down").returncode == 0
    up_ok(live_project, project_spec(agent_image, allow=("example.com",)))  # github is unreachable: a clone would fail
    assert shell(engine, live_project, "cat .git/marker").stdout == "mine\n"
    assert spawn_ok(live_project, "coder", "again", "--no-fetch") == "again"
    assert has_file(engine, live_project, "README", "again")


def test_down_and_rm_no_longer_know_workspace_flags(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    for command in (["project", "down", "--workspaces"], ["agent", "rm", "--workspaces", "coder-1"], ["project", "down", "--volumes"]):
        result = live_project.run(*command)
        assert result.returncode != 0 and "unknown flag" in result.stderr, command
    assert worktree_exists(engine, live_project)


def test_workspace_rm_removes_the_base_and_refuses_while_an_instance_uses_it(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    refused = live_project.run("workspace", "rm", "repo")
    assert refused.returncode != 0 and "coder-1" in refused.stderr
    assert base_exists(engine, live_project)
    assert live_project.run("agent", "rm", "--force", "coder-1").returncode == 0
    removed = live_project.run("workspace", "rm", "repo")
    assert removed.returncode == 0, removed.stderr
    assert not base_exists(engine, live_project)


def test_workspace_rm_removes_a_worktree_volume_left_by_no_instance(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    volume = live_project.worktree_volume("repo", "gone")
    labels = ["--label", f"{LABEL_PREFIX}project={live_project.name}", "--label", f"{LABEL_PREFIX}kind=workspace"]
    assert engine.run("volume", "create", *labels, volume).returncode == 0
    removed = live_project.run("workspace", "rm", "repo", "--worktree", "gone")
    assert removed.returncode == 0, removed.stderr
    assert not engine.volume_exists(volume) and base_exists(engine, live_project)


# --- safety --------------------------------------------------------------------------------------------------


def test_no_git_credential_reaches_the_base_the_worktree_or_the_agent(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    for text in (shell(engine, live_project, "cat .git/config").stdout, shell(engine, live_project, "cat .git", instance="coder-1").stdout):
        assert "Authorization" not in text and "token" not in text.lower()
    env = container(engine, live_project, "coder-1").raw["Config"]["Env"]
    assert not [e for e in env if e.startswith(("GITHUB_TOKEN", "GIT_ASKPASS", "GH_TOKEN"))]


def test_a_hook_planted_in_the_base_does_not_run_when_another_instance_is_prepared(live_project, engine, agent_image):
    """Every agent can write the base .git; preparing the next instance runs git with that instance's network
    identity, so a hook there would be code running as someone else."""
    up_ok(live_project, project_spec(agent_image, agents=two_templates(agent_image)))
    spawn_ok(live_project, "coder")
    planted = shell(engine, live_project, "printf '#!/bin/sh\\ntouch hook-ran\\n' > .git/hooks/post-checkout && chmod +x .git/hooks/post-checkout")
    assert planted.returncode == 0, planted.stderr
    spawn_ok(live_project, "reviewer")
    assert not has_file(engine, live_project, "hook-ran", "reviewer-1")
    assert has_file(engine, live_project, "README", "reviewer-1")
