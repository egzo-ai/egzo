# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""Git workspaces: at spawn, the prep container clones into the host before the instance starts.

The specs clone a small public repository through the project's proxy. Nothing runs git on the host
and no credential is ever handed to the CLI or the agent.
"""

import os
import subprocess

import pytest

from conftest import LABEL_PREFIX
from support import agent, spec, table

REPO = "https://github.com/octocat/Hello-World.git"

pytestmark = pytest.mark.usefixtures("github")


def custom(image, **fields):
    return agent(harness="custom", image=image, **fields)


def project_spec(image, *, workspace=None, agents=None, allow=("github.com",)):
    workspaces = {"repo": {"git": {"url": REPO}, **(workspace or {})}}
    agents = agents or {"coder": custom(image, workspaces=["repo"])}
    return spec(egress={"default": {"allow": list(allow)}}, workspaces=workspaces, agents=agents)


def up_ok(project, document):
    """`up` makes the infrastructure and publishes the templates; it clones nothing."""
    project.write(document)
    result = project.run("up", timeout=400)
    assert result.returncode == 0, result.stderr
    return result


def spawn(project, template="coder", name=None, *args):
    command = ["spawn", template, *([name] if name else []), *args]
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


def workspaces_dir(project):
    return project.root / ".egzo" / "workspaces" / "repo"


def clone_of(project, instance="coder-1"):
    return workspaces_dir(project) / instance


def two_templates(image):
    return {"coder": custom(image, workspaces=["repo"]), "reviewer": custom(image, workspaces=["repo"])}


def test_up_prepares_no_checkout_and_starts_no_agent(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    assert not workspaces_dir(live_project).exists()
    assert engine.instances(live_project.name) == []


def test_spawn_clones_a_git_workspace_for_the_instance_before_it_starts(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    assert spawn_ok(live_project) == "coder-1"
    clone = clone_of(live_project)
    assert (clone / "README").exists()
    assert git(clone, "remote", "get-url", "origin") == REPO
    assert "README" in in_agent(engine, live_project, "coder-1", "ls /workspace/repo").stdout


def test_the_clone_belongs_to_the_invoking_user_and_the_agent_can_commit(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    clone = clone_of(live_project)
    assert (clone / "README").stat().st_uid == os.getuid()
    committed = in_agent(
        engine, live_project, "coder-1",
        "cd /workspace/repo && git config user.email a@b.c && git config user.name a && echo x > new && git add new && git commit -qm x && git log --oneline -1",
    )
    assert committed.returncode == 0, committed.stderr
    assert git(clone, "log", "--oneline", "-1").endswith(" x")


def test_the_branch_is_checked_out(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image, workspace={"git": {"url": REPO, "branch": "test"}}))
    spawn_ok(live_project)
    assert in_agent(engine, live_project, "coder-1", "git -C /workspace/repo branch --show-current").stdout.strip() == "test"


def test_every_instance_gets_its_own_independent_clone_even_of_one_template(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    assert spawn_ok(live_project) == "coder-1"
    assert spawn_ok(live_project) == "coder-2"
    assert clone_of(live_project, "coder-1").is_dir() and clone_of(live_project, "coder-2").is_dir()
    assert in_agent(engine, live_project, "coder-1", "echo mine > /workspace/repo/private").returncode == 0
    assert in_agent(engine, live_project, "coder-2", "ls /workspace/repo").stdout.split().count("private") == 0
    assert in_agent(engine, live_project, "coder-2", "test -d /workspace/repo/.git").returncode == 0


def test_an_instance_name_chosen_by_the_user_names_its_checkout(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    assert spawn_ok(live_project, "coder", "issue-412") == "issue-412"
    assert (clone_of(live_project, "issue-412") / "README").exists()


def test_the_clone_location_follows_the_path_key(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image, workspace={"path": "./work/repo"}))
    spawn_ok(live_project)
    assert (live_project.root / "work" / "repo" / "coder-1" / "README").exists()
    assert not (live_project.root / ".egzo" / "workspaces").exists()


def test_an_existing_checkout_is_never_modified_by_a_later_spawn(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    clone = clone_of(live_project)
    (clone / "wip.txt").write_text("unpushed work\n")
    spawn_ok(live_project, "coder", "second")
    assert (clone / "wip.txt").read_text() == "unpushed work\n"


def test_a_checkout_directory_that_already_exists_is_reused_not_replaced(live_project, engine, agent_image):
    """The instance `issue-1` was removed but its checkout was kept: spawning the name again gets that work back."""
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project, "coder", "issue-1")
    (clone_of(live_project, "issue-1") / "wip.txt").write_text("kept\n")
    assert live_project.run("rm", "--force", "issue-1").returncode == 0
    spawn_ok(live_project, "coder", "issue-1")
    assert (clone_of(live_project, "issue-1") / "wip.txt").read_text() == "kept\n"


def test_the_prep_container_is_removed_and_leaves_no_trace(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    names = {r.labels.get(f"{LABEL_PREFIX}service") for r in engine.containers(live_project.name)}
    assert names == {"control", "proxy", "coder"}


def test_the_clone_goes_through_the_proxy_with_the_instances_identity(live_project, engine, agent_image):
    import json

    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project, "coder", "issue-9")
    audit = [json.loads(line) for line in live_project.run("proxy", "log").stdout.splitlines() if line.startswith("{")]
    allowed = [e for e in audit if e["host"] == "github.com" and e["action"] == "allow"]
    assert allowed and {e["agent"] for e in allowed} == {"issue-9"}


def test_a_profile_that_cannot_reach_the_git_host_stops_spawn_with_a_clear_error(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image, allow=("example.com",)))
    result = spawn(live_project)
    assert result.returncode != 0
    assert "github.com" in result.stderr and "repo" in result.stderr
    assert engine.instances(live_project.name) == []
    assert not [r for r in engine.resources(live_project.name) if r.name.endswith("_coder-1")], "a failed spawn left resources behind"
    assert live_project.run("ps", "--json").returncode == 0


def test_shared_mode_gives_every_instance_the_same_checkout(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image, workspace={"mode": "shared"}, agents=two_templates(agent_image)))
    spawn_ok(live_project, "coder")
    spawn_ok(live_project, "reviewer")
    assert (workspaces_dir(live_project) / "shared" / "README").exists()
    assert in_agent(engine, live_project, "coder-1", "echo seen > /workspace/repo/note").returncode == 0
    assert in_agent(engine, live_project, "reviewer-1", "cat /workspace/repo/note").stdout.strip() == "seen"


def test_shared_mode_is_one_checkout_for_two_instances_of_one_template(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image, workspace={"mode": "shared"}))
    spawn_ok(live_project)
    spawn_ok(live_project)
    assert sorted(p.name for p in workspaces_dir(live_project).iterdir() if p.name != ".egzo-checkouts") == ["shared"]


def test_worktree_mode_gives_each_instance_a_worktree_of_one_base_clone(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image, workspace={"mode": "worktree"}, agents=two_templates(agent_image)))
    spawn_ok(live_project, "coder")
    spawn_ok(live_project, "reviewer")
    assert (workspaces_dir(live_project) / ".base" / ".git").exists()
    status = in_agent(engine, live_project, "coder-1", "cd /workspace/repo && git status --short --branch && git worktree list")
    assert status.returncode == 0, status.stderr
    assert in_agent(engine, live_project, "coder-1", "echo mine > /workspace/repo/private").returncode == 0
    assert in_agent(engine, live_project, "reviewer-1", "test -e /workspace/repo/private").returncode != 0


def test_worktree_mode_does_not_expose_other_instances_worktrees(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image, workspace={"mode": "worktree"}, agents=two_templates(agent_image)))
    spawn_ok(live_project, "coder")
    spawn_ok(live_project, "reviewer")
    mounts = {m["Destination"] for m in container(engine, live_project, "coder-1").raw["Mounts"]}
    assert not any("reviewer" in m for m in mounts)


def test_changing_the_mode_of_an_existing_clone_is_refused_at_spawn_not_converted(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    assert live_project.run("rm", "--force", "coder-1").returncode == 0  # the checkout stays
    up_ok(live_project, project_spec(agent_image, workspace={"mode": "shared"}))
    result = spawn(live_project)
    assert result.returncode != 0
    assert "mode" in result.stderr and str(workspaces_dir(live_project)) in result.stderr
    assert engine.instances(live_project.name) == []


def test_no_git_credential_reaches_the_clone_or_the_agent(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    config = (clone_of(live_project) / ".git" / "config").read_text()
    assert "Authorization" not in config and "token" not in config.lower()
    env = container(engine, live_project, "coder-1").raw["Config"]["Env"]
    assert not [e for e in env if e.startswith(("GITHUB_TOKEN", "GIT_ASKPASS", "GH_TOKEN"))]


def test_down_never_deletes_workspace_directories(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    assert live_project.run("down", "--volumes").returncode == 0
    assert (clone_of(live_project) / "README").exists()


def test_down_workspaces_removes_clean_clones_after_confirmation(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    spawn_ok(live_project)
    refused = live_project.run("down", "--workspaces", input="n\n")
    assert refused.returncode != 0
    assert clone_of(live_project, "coder-1").exists() and clone_of(live_project, "coder-2").exists()
    done = live_project.run("down", "--workspaces", input="y\n")
    assert done.returncode == 0, done.stderr
    assert not clone_of(live_project, "coder-1").exists() and not clone_of(live_project, "coder-2").exists()


def test_down_workspaces_finds_the_checkouts_of_instances_that_were_removed(live_project, engine, agent_image):
    """The checkout of an instance outlives it, so `down --workspaces` goes by what is on the disk."""
    up_ok(live_project, project_spec(agent_image, workspace={"mode": "worktree"}))
    spawn_ok(live_project, "coder", "gone")
    assert live_project.run("rm", "--force", "gone").returncode == 0
    assert clone_of(live_project, "gone").exists()
    assert live_project.run("down", "--workspaces", "--yes").returncode == 0
    assert not clone_of(live_project, "gone").exists()
    assert not (workspaces_dir(live_project) / ".base").exists()


def test_down_workspaces_leaves_a_checkout_alone_that_egzo_did_not_make(live_project, engine, agent_image):
    """A workspace path may hold the user's own clones: only what egzo recorded is offered for removal."""
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    foreign = workspaces_dir(live_project) / "my-own-clone"
    (foreign / ".git").mkdir(parents=True)
    (foreign / "work.txt").write_text("mine\n")
    result = live_project.run("down", "--workspaces", "--yes")
    assert result.returncode == 0, result.stderr
    assert not clone_of(live_project, "coder-1").exists()
    assert (foreign / "work.txt").read_text() == "mine\n"


def test_down_workspaces_refuses_a_clone_with_uncommitted_work(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    clone = clone_of(live_project)
    (clone / "wip.txt").write_text("not committed\n")
    result = live_project.run("down", "--workspaces", "--yes")
    assert result.returncode != 0
    assert "uncommitted" in result.stderr and str(clone) in result.stderr
    assert (clone / "wip.txt").exists()


def test_down_workspaces_refuses_a_clone_with_unpushed_commits(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    clone = clone_of(live_project)
    git(clone, "-c", "user.email=a@b.c", "-c", "user.name=a", "commit", "--allow-empty", "-qm", "local only")
    result = live_project.run("down", "--workspaces", "--yes")
    assert result.returncode != 0
    assert "unpushed" in result.stderr
    assert clone.exists()


def test_down_workspaces_force_removes_even_unsaved_work(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    clone = clone_of(live_project)
    (clone / "wip.txt").write_text("not committed\n")
    assert live_project.run("down", "--workspaces", "--yes", "--force").returncode == 0
    assert not clone.exists()


# --- down --workspaces never throws away work it did not look at ---------------------------------------------


def test_down_workspaces_refuses_a_clone_with_stashed_work(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    clone = clone_of(live_project)
    (clone / "README").write_text("changed\n")
    git(clone, "-c", "user.email=a@b.c", "-c", "user.name=a", "stash")
    result = live_project.run("down", "--workspaces", "--yes")
    assert result.returncode != 0
    assert "stash" in result.stderr
    assert clone.exists()


def test_down_workspaces_refuses_commits_that_are_on_no_branch(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    clone = clone_of(live_project)
    git(clone, "checkout", "-q", "--detach")
    git(clone, "-c", "user.email=a@b.c", "-c", "user.name=a", "commit", "--allow-empty", "-qm", "on no branch")
    result = live_project.run("down", "--workspaces", "--yes")
    assert result.returncode != 0
    assert "unpushed" in result.stderr or "detached" in result.stderr
    assert clone.exists()


def test_down_workspaces_says_what_ignored_files_it_is_about_to_delete(live_project, engine, agent_image):
    """Ignored files (a .env, local data) are not in git, so they exist nowhere else: the person is told."""
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    clone = clone_of(live_project)
    (clone / ".git" / "info" / "exclude").write_text("*.secret\n")
    (clone / "notes.secret").write_text("only here\n")
    result = live_project.run("down", "--workspaces", input="n\n")
    assert result.returncode != 0
    assert "ignored" in result.stderr
    assert (clone / "notes.secret").exists()


def test_a_refused_down_workspaces_leaves_the_instances_running(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    (clone_of(live_project) / "wip.txt").write_text("not committed\n")
    assert live_project.run("down", "--workspaces", "--yes").returncode != 0
    rows = table(live_project.run("ps").stdout)
    assert [r["STATE"] for r in rows if r["NAME"] == "coder-1"] == ["running"]


def test_down_workspaces_only_removes_directories_that_are_checkouts(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    clone = clone_of(live_project)
    subprocess.run(["rm", "-rf", str(clone / ".git")], check=True)  # no longer a checkout
    (clone / "precious.txt").write_text("not a git work tree any more\n")
    live_project.run("down", "--workspaces", "--yes")
    assert (clone / "precious.txt").exists()


def test_down_workspaces_never_removes_the_project_directory(live_project, engine, agent_image):
    """`path: .` puts checkouts next to egzo.yaml; a name clash must not make the project itself removable."""
    document = project_spec(agent_image, workspace={"path": "."})
    up_ok(live_project, document)
    spawn_ok(live_project)
    assert (live_project.root / "coder-1" / "README").exists()
    assert live_project.run("down", "--workspaces", "--yes").returncode == 0
    assert (live_project.root / "egzo.yaml").exists()


def test_a_hook_planted_in_a_shared_base_does_not_run_when_another_instance_is_prepared(live_project, engine, agent_image):
    """In worktree mode every agent can write the base .git; preparing the next instance runs git with that
    instance's network identity, so a hook there would be code running as someone else."""
    up_ok(live_project, project_spec(agent_image, workspace={"mode": "worktree"}, agents=two_templates(agent_image)))
    spawn_ok(live_project, "coder")
    base = workspaces_dir(live_project) / ".base"
    hook = base / ".git" / "hooks" / "post-checkout"
    hook.write_text("#!/bin/sh\ntouch hook-ran\n")
    hook.chmod(0o755)
    spawn_ok(live_project, "reviewer")
    assert not (clone_of(live_project, "reviewer-1") / "hook-ran").exists()
    assert (clone_of(live_project, "reviewer-1") / "README").exists()


# --- rm: an instance's checkout is its own ------------------------------------------------------------------


def test_rm_keeps_the_checkout_of_the_instance(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    assert live_project.run("rm", "--force", "coder-1").returncode == 0
    assert (clone_of(live_project) / "README").exists()


def test_rm_workspaces_removes_only_the_checkout_of_that_instance(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    spawn_ok(live_project)
    result = live_project.run("rm", "--force", "--workspaces", "--yes", "coder-1")
    assert result.returncode == 0, result.stderr
    assert not clone_of(live_project, "coder-1").exists()
    assert (clone_of(live_project, "coder-2") / "README").exists()
    assert live_project.run("ps", "--json").returncode == 0


def test_rm_workspaces_never_removes_the_shared_checkout_or_the_worktree_base(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image, workspace={"mode": "worktree"}))
    spawn_ok(live_project)
    assert live_project.run("rm", "--force", "--workspaces", "--yes", "coder-1").returncode == 0
    assert (workspaces_dir(live_project) / ".base" / ".git").exists()
    assert not clone_of(live_project).exists()


def test_rm_workspaces_refuses_a_checkout_with_unsaved_work(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    clone = clone_of(live_project)
    (clone / "wip.txt").write_text("not committed\n")
    assert live_project.run("stop", "coder-1").returncode == 0  # --force would also override the inspection
    result = live_project.run("rm", "--workspaces", "--yes", "coder-1")
    assert result.returncode != 0
    assert "uncommitted" in result.stderr and str(clone) in result.stderr
    assert (clone / "wip.txt").exists()
    assert engine.instance(live_project.name, "coder-1") is not None, "a refused removal must leave the instance alone"


def test_rm_workspaces_force_removes_even_unsaved_work(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    clone = clone_of(live_project)
    (clone / "wip.txt").write_text("not committed\n")
    assert live_project.run("rm", "--workspaces", "--yes", "--force", "coder-1").returncode == 0
    assert not clone.exists()


def test_rm_workspaces_asks_before_removing_a_checkout(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    spawn_ok(live_project)
    assert live_project.run("stop", "coder-1").returncode == 0
    refused = live_project.run("rm", "--workspaces", "coder-1", input="n\n")
    assert refused.returncode != 0
    assert clone_of(live_project).exists()
    done = live_project.run("rm", "--workspaces", "coder-1", input="y\n")
    assert done.returncode == 0, done.stderr
    assert not clone_of(live_project).exists()
