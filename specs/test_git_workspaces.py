# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""Git workspaces: `up` clones a base, `spawn` gives each instance its own worktree of it.

    workspaces/repo/.base          the base clone, made by `up` (no files checked out)
    workspaces/repo/coder-1        one worktree per instance, on the branch egzo/coder-1

Inside the container the names are kept (`/workspace/repo/.base`, `/workspace/repo/coder-1`), so the worktree's
relative links resolve on the host and in the container alike.

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


def git(path, *args):
    return subprocess.run(["git", "-C", str(path), *args], capture_output=True, text=True, check=True).stdout.strip()


def remote_head(ref):
    return subprocess.run(["git", "ls-remote", REPO, ref], capture_output=True, text=True, check=True).stdout.split()[0]


def repo_dir(project):
    return project.root / "workspaces" / "repo"


def base_of(project):
    return repo_dir(project) / ".base"


def worktree_of(project, instance="coder-1"):
    return repo_dir(project) / instance


def two_templates(image):
    return {"coder": custom(image, workspaces=["repo"]), "reviewer": custom(image, workspaces=["repo"])}


def audit(project):
    return [json.loads(line) for line in project.run("proxy", "log").stdout.splitlines() if line.startswith("{")]


# --- up makes the base ---------------------------------------------------------------------------------------


def test_up_clones_the_base_without_checking_anything_out_and_starts_no_agent(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    base = base_of(live_project)
    assert (base / ".git").exists()
    assert git(base, "remote", "get-url", "origin") == REPO
    assert not (base / "README").exists(), "the base holds no working files; every file belongs to a worktree"
    assert not worktree_of(live_project).exists()
    assert engine.instances(live_project.name) == []


def test_the_base_belongs_to_the_invoking_user(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    assert (base_of(live_project) / ".git" / "HEAD").stat().st_uid == os.getuid()


def test_a_second_up_leaves_the_base_alone(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    marker = base_of(live_project) / ".git" / "marker"
    marker.write_text("mine\n")
    up_ok(live_project, project_spec(agent_image))
    assert marker.read_text() == "mine\n"


def test_up_only_clones_workspaces_that_an_agent_lists(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image, extra={"unused": {"git": {"url": REPO}}}))
    assert base_of(live_project).exists()
    assert not (live_project.root / "workspaces" / "unused").exists()


def test_a_failed_clone_at_up_is_reported_and_does_not_fail_up(live_project, engine, agent_image):
    result = up_ok(live_project, project_spec(agent_image, allow=("example.com",)))
    assert "github.com" in result.stderr and "repo" in result.stderr
    assert not base_of(live_project).exists()
    assert [r.name for r in engine.containers(live_project.name)], "the project itself is up"


def test_a_base_that_does_not_match_the_file_is_reported_and_left_alone(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    changed = project_spec(agent_image, workspace={"git": {"url": OTHER_REPO}})
    result = up_ok(live_project, changed)
    assert "repo" in result.stderr and "url" in result.stderr
    assert git(base_of(live_project), "remote", "get-url", "origin") == REPO


def test_the_base_location_follows_the_path_key(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image, workspace={"path": "./work/repo"}))
    assert (live_project.root / "work" / "repo" / ".base" / ".git").exists()
    spawn_ok(live_project)
    assert (live_project.root / "work" / "repo" / "coder-1" / "README").exists()
    assert not (live_project.root / "workspaces").exists()


# --- spawn makes a worktree ----------------------------------------------------------------------------------


def test_spawn_gives_the_instance_a_worktree_on_its_own_branch(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    assert spawn_ok(live_project) == "coder-1"
    worktree = worktree_of(live_project)
    assert (worktree / "README").exists()
    assert git(worktree, "branch", "--show-current") == "egzo/coder-1"
    assert git(worktree, "rev-parse", "HEAD") == remote_head("HEAD")
    assert "README" in in_agent(engine, live_project, "coder-1", "ls /workspace/repo/coder-1").stdout


def test_spawn_clones_the_base_itself_when_it_is_missing(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    subprocess.run(["agent", "rm", "-rf", str(base_of(live_project))], check=True)
    spawn_ok(live_project)
    assert (base_of(live_project) / ".git").exists() and (worktree_of(live_project) / "README").exists()


def test_the_worktree_starts_at_the_head_of_the_configured_branch(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image, workspace={"git": {"url": REPO, "branch": "test"}}))
    spawn_ok(live_project)
    assert git(worktree_of(live_project), "rev-parse", "HEAD") == remote_head("refs/heads/test")


def test_the_container_sees_the_base_and_its_own_worktree_under_the_same_names(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image, agents=two_templates(agent_image)))
    spawn_ok(live_project, "coder")
    spawn_ok(live_project, "reviewer")
    coder = container(engine, live_project, "coder-1")
    destinations = {m["Destination"] for m in coder.raw["Mounts"]}
    assert {"/workspace/repo/.base", "/workspace/repo/coder-1"} <= destinations
    assert "/workspace/repo" not in destinations and not any("reviewer" in d for d in destinations)
    assert coder.raw["Config"]["WorkingDir"] == "/workspace/repo/coder-1"
    listing = in_agent(engine, live_project, "coder-1", "ls -A /workspace/repo").stdout.split()
    assert sorted(listing) == [".base", "coder-1"]


def test_the_worktree_links_are_relative_so_git_works_on_the_host_and_in_the_container(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    worktree = worktree_of(live_project)
    assert (worktree / ".git").read_text().strip() == "gitdir: ../.base/.git/worktrees/coder-1"
    assert git(worktree, "status", "--short") == ""  # on the host
    inside = in_agent(engine, live_project, "coder-1", "cd /workspace/repo/coder-1 && git status --short && git worktree list")
    assert inside.returncode == 0, inside.stderr
    assert "/workspace/repo/coder-1" in inside.stdout and "/workspace/repo/.base" in inside.stdout


def test_the_worktree_belongs_to_the_invoking_user_and_the_agent_can_commit(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    worktree = worktree_of(live_project)
    assert (worktree / "README").stat().st_uid == os.getuid()
    committed = in_agent(
        engine, live_project, "coder-1",
        "cd /workspace/repo/coder-1 && git config user.email a@b.c && git config user.name a && echo x > new && git add new && git commit -qm x && git log --oneline -1",
    )
    assert committed.returncode == 0, committed.stderr
    assert git(worktree, "log", "--oneline", "-1").endswith(" x")


def test_every_instance_gets_its_own_worktree_of_one_base(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image, agents=two_templates(agent_image)))
    assert spawn_ok(live_project, "coder") == "coder-1"
    assert spawn_ok(live_project, "reviewer") == "reviewer-1"
    assert spawn_ok(live_project, "coder") == "coder-2"
    assert in_agent(engine, live_project, "coder-1", "echo mine > /workspace/repo/coder-1/private").returncode == 0
    assert in_agent(engine, live_project, "coder-2", "test -e /workspace/repo/coder-2/private").returncode != 0
    assert in_agent(engine, live_project, "reviewer-1", "test -e /workspace/repo/reviewer-1/private").returncode != 0
    assert git(base_of(live_project), "worktree", "list").count("\n") == 3  # the base and three worktrees


def test_an_instance_name_chosen_by_the_user_names_its_worktree_and_branch(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    assert spawn_ok(live_project, "coder", "issue-412") == "issue-412"
    assert (worktree_of(live_project, "issue-412") / "README").exists()
    assert git(worktree_of(live_project, "issue-412"), "branch", "--show-current") == "egzo/issue-412"


def test_the_instance_name_cannot_start_with_a_dot(live_project, engine, agent_image):
    """`.base` is the base: no instance may be called that, or anything that hides in its place."""
    up_ok(live_project, project_spec(agent_image))
    result = spawn(live_project, "coder", ".base")
    assert result.returncode != 0
    assert base_of(live_project).exists() and not (base_of(live_project) / "README").exists()
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
    assert not worktree_of(live_project).exists()
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
    assert (worktree_of(live_project, "quiet") / "README").exists()
    assert "quiet" not in {e.get("agent") for e in audit(live_project) if e.get("host") == "github.com"}


def test_a_fetch_that_fails_warns_and_the_worktree_is_made_from_the_base_as_it_is(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    up_ok(live_project, project_spec(agent_image, allow=("example.com",)))
    result = spawn(live_project)
    assert result.returncode == 0, result.stderr
    assert "github.com" in result.stderr and "fetch" in result.stderr.lower()
    assert (worktree_of(live_project) / "README").exists()


def test_the_fetch_uses_the_url_of_the_file_and_not_the_remote_of_the_base(live_project, engine, agent_image):
    """The base may be a clone of your own whose remote is ssh, which the proxy cannot serve."""
    up_ok(live_project, project_spec(agent_image))
    git(base_of(live_project), "remote", "set-url", "origin", "ssh://git@github.com/octocat/Hello-World.git")
    spawn_ok(live_project, "coder", "issue-3")
    assert "issue-3" in {e.get("agent") for e in audit(live_project) if e.get("host") == "github.com" and e.get("action") == "allow"}
    assert (worktree_of(live_project, "issue-3") / "README").exists()


# --- a worktree is never replaced ----------------------------------------------------------------------------


def test_a_worktree_directory_that_exists_makes_spawn_fail_unless_reuse_is_given(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    worktree_of(live_project, "issue-1").mkdir(parents=True)
    refused = spawn(live_project, "coder", "issue-1")
    assert refused.returncode != 0
    assert str(worktree_of(live_project, "issue-1")) in refused.stderr and "--reuse" in refused.stderr
    assert engine.instances(live_project.name) == []
    assert spawn(live_project, "coder", "issue-1", "--reuse").returncode == 0
    assert (worktree_of(live_project, "issue-1") / "README").exists()


def test_committed_work_survives_rm_on_the_branch_and_reuse_continues_it(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project, "coder", "issue-1")
    worktree = worktree_of(live_project, "issue-1")
    git(worktree, "-c", "user.email=a@b.c", "-c", "user.name=a", "commit", "--allow-empty", "-qm", "keep me")
    assert live_project.run("agent", "rm", "--force", "issue-1").returncode == 0
    assert not worktree.exists()
    assert git(base_of(live_project), "log", "-1", "--format=%s", "egzo/issue-1") == "keep me"
    refused = spawn(live_project, "coder", "issue-1")
    assert refused.returncode != 0
    assert "egzo/issue-1" in refused.stderr and "--reuse" in refused.stderr
    assert spawn(live_project, "coder", "issue-1", "--reuse").returncode == 0
    assert git(worktree, "log", "-1", "--format=%s") == "keep me"
    assert git(worktree, "branch", "--show-current") == "egzo/issue-1"


def test_an_existing_worktree_is_never_modified_by_another_spawn(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    (worktree_of(live_project) / "wip.txt").write_text("unpushed work\n")
    spawn_ok(live_project, "coder", "second")
    assert (worktree_of(live_project) / "wip.txt").read_text() == "unpushed work\n"


# --- removing ------------------------------------------------------------------------------------------------


def test_rm_deletes_the_worktree_without_a_check_and_keeps_the_base_and_the_others(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    spawn_ok(live_project)
    (worktree_of(live_project, "coder-1") / "wip.txt").write_text("not committed, and nobody asks\n")
    assert live_project.run("agent", "rm", "--force", "coder-1").returncode == 0
    assert not worktree_of(live_project, "coder-1").exists()
    assert (worktree_of(live_project, "coder-2") / "README").exists()
    assert (base_of(live_project) / ".git").exists()
    listed = git(base_of(live_project), "worktree", "list")
    assert "coder-1" not in listed and "coder-2" in listed
    assert git(base_of(live_project), "branch", "--list", "egzo/coder-1") != ""


def test_down_removes_the_worktrees_of_the_instances_and_keeps_the_base(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    assert live_project.run("project", "down", "--force").returncode == 0
    assert not worktree_of(live_project).exists()
    assert (base_of(live_project) / ".git").exists()


def test_down_and_rm_no_longer_know_workspace_flags(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    for command in (["down", "--workspaces"], ["agent", "rm", "--workspaces", "coder-1"], ["agent", "rm", "--yes", "coder-1"]):
        result = live_project.run(*command)
        assert result.returncode != 0 and "unknown flag" in result.stderr, command
    assert worktree_of(live_project).exists()


# --- safety --------------------------------------------------------------------------------------------------


def test_no_git_credential_reaches_the_base_the_worktree_or_the_agent(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    for config in (base_of(live_project) / ".git" / "config", worktree_of(live_project) / ".git"):
        text = config.read_text()
        assert "Authorization" not in text and "token" not in text.lower()
    env = container(engine, live_project, "coder-1").raw["Config"]["Env"]
    assert not [e for e in env if e.startswith(("GITHUB_TOKEN", "GIT_ASKPASS", "GH_TOKEN"))]


def test_a_hook_planted_in_the_base_does_not_run_when_another_instance_is_prepared(live_project, engine, agent_image):
    """Every agent can write the base .git; preparing the next instance runs git with that instance's network
    identity, so a hook there would be code running as someone else."""
    up_ok(live_project, project_spec(agent_image, agents=two_templates(agent_image)))
    spawn_ok(live_project, "coder")
    hook = base_of(live_project) / ".git" / "hooks" / "post-checkout"
    hook.write_text("#!/bin/sh\ntouch hook-ran\n")
    hook.chmod(0o755)
    spawn_ok(live_project, "reviewer")
    assert not (worktree_of(live_project, "reviewer-1") / "hook-ran").exists()
    assert (worktree_of(live_project, "reviewer-1") / "README").exists()
