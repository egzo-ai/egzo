# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""Git workspaces: `up` clones a base, `spawn` gives each instance its own worktree of it.

Everything lives in engine volumes, never on the host:

    <project>_ws.repo.base          the base clone, made by `up` (no files checked out), at /workspace/repo/.base
    <project>_ws.repo.wt.coder-1    the worktree of the instance coder-1 on the branch egzo/coder-1, at /workspace/repo/coder-1

Inside the container the two are siblings, so the worktree's relative links resolve. The specs look inside the volumes with a throwaway
container (`engine.in_volumes`), since the host cannot browse them. They clone a small public repository through the project's proxy
and fail when github.com cannot be reached. No credential is ever handed to the CLI or the agent.

The first specs share one project that is up for the whole module (the base is cloned once); the last changes what the proxy allows and
what exists, and has its own."""

import json
import os
import subprocess

import pytest

from conftest import LABEL_PREFIX
from support import custom, spec

REPO = "https://github.com/octocat/Hello-World.git"
OTHER_REPO = "https://github.com/octocat/Spoon-Knife.git"

pytestmark = pytest.mark.usefixtures("github")


def project_document(image, allow=("github.com",), url=REPO):
    return spec(
        egress={"default": {"allow": list(allow)}},
        workspaces={"repo": {"git": {"url": url}}, "unused": {"git": {"url": REPO}}, "other": {"git": {"url": REPO, "branch": "test"}}},
        agents={
            "coder": custom(image, workspaces=["repo"]),
            "reviewer": custom(image, workspaces=["repo"]),
            "branched": custom(image, workspaces=["other"]),
        },
    )


@pytest.fixture(scope="module")
def sandbox(shared_project, engine):
    shared_project.up(project_document(engine.agent_image()))
    return shared_project


@pytest.fixture(autouse=True)
def start_from_no_instance(sandbox, engine):
    sandbox.retire()
    sandbox.write(project_document(engine.agent_image()))
    yield


def remote_head(ref):
    return subprocess.run(["git", "ls-remote", REPO, ref], capture_output=True, text=True, check=True).stdout.split()[0]


def shell(engine, project, command, instance=None, workspace="repo"):
    """A shell command in a throwaway container that mounts the base and, with an instance, its worktree where an agent has them.
    The working directory is the worktree, or the base."""
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


def has_file(engine, project, path, instance, workspace="repo"):
    return shell(engine, project, f"test -e {path}", instance, workspace).returncode == 0


def in_agent(engine, project, name, command):
    return engine.exec(engine.instance(project.name, name).name, "sh", "-c", command)


def github_audit(project, agent):
    events = [json.loads(line) for line in project.ok("proxy log").stdout.splitlines() if line.startswith("{")]
    return [e for e in events if e.get("host") == "github.com" and e.get("agent") == agent]


def test_up_clones_the_base_for_the_workspaces_an_agent_lists_without_a_checkout_and_leaves_it_alone_afterwards(sandbox, engine):
    p = sandbox
    assert git(engine, p, "remote", "get-url", "origin") == REPO
    assert not has_file(engine, p, "/workspace/repo/.base/README", None), "the base holds no working files"
    assert engine.instances(p.name) == [], "up starts no agent"
    [volume] = [r for r in engine.resources(p.name) if r.name == p.base_volume("repo")]
    assert volume.labels[f"{LABEL_PREFIX}kind"] == "workspace" and volume.labels[f"{LABEL_PREFIX}service"] == "repo"
    assert f"{LABEL_PREFIX}instance" not in volume.labels, "the base belongs to the project"
    assert shell(engine, p, "stat -c %u /workspace/repo/.base/.git/HEAD").stdout.strip() == str(engine.uid), "and to the invoking user"
    assert engine.volume_exists(p.base_volume("other")) and not engine.volume_exists(p.base_volume("unused")), "only workspaces an agent lists are cloned"
    assert p.left_in_directory() == [], "nothing is made in the project directory"

    assert shell(engine, p, "echo mine > .git/marker").returncode == 0
    p.ok("project up", timeout=400)
    assert shell(engine, p, "cat .git/marker").stdout == "mine\n", "a second up leaves the base alone"


def test_spawn_gives_each_instance_its_own_worktree_volume_on_its_own_branch_of_one_base(sandbox, engine):
    p = sandbox
    coder, reviewer, chosen = p.spawn("coder", "w1"), p.spawn("reviewer", "w2"), p.spawn("coder", "issue-412")
    assert chosen == "issue-412"
    assert has_file(engine, p, "README", coder) and git(engine, p, "branch", "--show-current", instance=coder) == f"egzo/{coder}"
    assert git(engine, p, "rev-parse", "HEAD", instance=coder) == remote_head("HEAD")
    assert git(engine, p, "branch", "--show-current", instance="issue-412") == "egzo/issue-412", "a chosen name names the worktree and the branch"
    assert len(git(engine, p, "worktree", "list").splitlines()) == 4, "the base and three worktrees"

    [volume] = [r for r in engine.resources(p.name) if r.name == p.worktree_volume("repo", "w1")]
    labels = {k.removeprefix(LABEL_PREFIX): v for k, v in volume.labels.items()}
    assert (labels["kind"], labels["service"], labels["instance"], labels["actor"]) == ("workspace", "coder", "w1", "operator")
    assert labels["template-hash"] == engine.instance(p.name, "w1").labels[f"{LABEL_PREFIX}template-hash"]
    assert {r.labels.get(f"{LABEL_PREFIX}service") for r in engine.containers(p.name)} == {"control", "proxy", "coder", "reviewer"}, "no prep container is left"

    coder = engine.instance(p.name, "w1")
    volumes = {m["Destination"]: m["Name"] for m in coder.raw["Mounts"] if m["Type"] == "volume"}
    assert volumes["/workspace/repo/.base"] == p.base_volume("repo")
    assert volumes["/workspace/repo/w1"] == p.worktree_volume("repo", "w1")
    assert "/workspace/repo" not in volumes and not any("reviewer" in d for d in volumes), "never another instance's worktree"
    assert coder.raw["Config"]["WorkingDir"] == "/workspace/repo/w1"
    assert sorted(in_agent(engine, p, "w1", "ls -A /workspace/repo").stdout.split()) == [".base", "w1"]
    assert "README" in in_agent(engine, p, "w1", "ls /workspace/repo/w1").stdout

    # the links are relative, so git works wherever the two volumes sit side by side
    assert shell(engine, p, "cat .git", instance="w1").stdout.strip() == "gitdir: ../.base/.git/worktrees/w1"
    assert git(engine, p, "status", "--short", instance="w1") == ""
    inside = in_agent(engine, p, "w1", "cd /workspace/repo/w1 && git status --short && git worktree list")
    assert inside.returncode == 0, inside.stderr
    assert "/workspace/repo/w1" in inside.stdout and "/workspace/repo/.base" in inside.stdout

    # the worktree is the invoking user's, the agent can commit, and what it writes is its own
    assert shell(engine, p, "stat -c %u README", instance="w1").stdout.strip() == str(engine.uid)
    committed = in_agent(engine, p, "w1", "cd /workspace/repo/w1 && git config user.email a@b.c && git config user.name a && echo x > new && git add new && git commit -qm x && git log --oneline -1")
    assert committed.returncode == 0, committed.stderr
    assert git(engine, p, "log", "--oneline", "-1", instance="w1").endswith(" x")
    assert in_agent(engine, p, "w1", "echo mine > /workspace/repo/w1/private").returncode == 0
    assert in_agent(engine, p, "issue-412", "test -e /workspace/repo/issue-412/private").returncode != 0
    assert in_agent(engine, p, "w2", "test -e /workspace/repo/w2/private").returncode != 0
    assert shell(engine, p, "echo 'unpushed work' > wip.txt", instance="w1").returncode == 0
    p.spawn("coder", "second")
    assert shell(engine, p, "cat wip.txt", instance="w1").stdout == "unpushed work\n", "an existing worktree is never modified by another spawn"

    other = p.spawn("branched")  # the worktree starts at the head of the configured branch
    assert git(engine, p, "rev-parse", "HEAD", instance=other, workspace="other") == remote_head("refs/heads/test")

    refused = p.run("agent", "spawn", "coder", ".base", timeout=400)  # `.base` is the base: no instance may be called that
    assert refused.returncode != 0 and not has_file(engine, p, "/workspace/repo/.base/README", None)


def test_the_fetch_goes_through_the_proxy_as_the_instance_and_no_credential_or_hook_reaches_an_agent(sandbox, engine):
    p = sandbox
    p.spawn("coder", "issue-9")
    fetched = [e for e in github_audit(p, "issue-9") if e["event"] == "connect" and e["decision"] == "allow"]
    assert fetched, "the fetch has the instance's identity"
    assert {e["instance"] for e in fetched} == {engine.instance(p.name, "issue-9").labels[f"{LABEL_PREFIX}instance-id"]}
    p.ok("agent spawn coder quiet --no-fetch", timeout=400)
    assert has_file(engine, p, "README", "quiet") and not github_audit(p, "quiet"), "--no-fetch does not touch the network"

    # the fetch uses the url of the file, not the remote of the base (which may be ssh, which the proxy cannot serve)
    git(engine, p, "remote", "set-url", "origin", "ssh://git@github.com/octocat/Hello-World.git")
    p.spawn("coder", "issue-3")
    assert github_audit(p, "issue-3") and has_file(engine, p, "README", "issue-3")
    git(engine, p, "remote", "set-url", "origin", REPO)

    for text in (shell(engine, p, "cat .git/config").stdout, shell(engine, p, "cat .git", instance="issue-9").stdout):
        assert "Authorization" not in text and "token" not in text.lower()
    env = engine.instance(p.name, "issue-9").raw["Config"]["Env"]
    assert not [e for e in env if e.startswith(("GITHUB_TOKEN", "GIT_ASKPASS", "GH_TOKEN"))]

    # every agent can write the base .git; preparing the next instance runs git with that instance's network identity, so a hook there
    # would be code running as someone else
    planted = shell(engine, p, "printf '#!/bin/sh\\ntouch hook-ran\\n' > .git/hooks/post-checkout && chmod +x .git/hooks/post-checkout")
    assert planted.returncode == 0, planted.stderr
    prepared = p.spawn("reviewer")
    assert not has_file(engine, p, "hook-ran", prepared) and has_file(engine, p, "README", prepared)
    shell(engine, p, "rm .git/hooks/post-checkout")


def test_a_worktree_is_never_replaced_committed_work_outlives_the_instance_and_rm_deletes_without_a_check(sandbox, engine):
    p = sandbox
    volume = p.worktree_volume("repo", "issue-0")
    labels = ["--label", f"{LABEL_PREFIX}project={p.name}", "--label", f"{LABEL_PREFIX}kind=workspace"]
    assert engine.run("volume", "create", *labels, volume).returncode == 0
    refused = p.sh("agent spawn coder issue-0", timeout=400)
    assert refused.returncode != 0 and volume in refused.stderr and "--reuse" in refused.stderr
    assert engine.instances(p.name) == []
    p.ok("agent spawn coder issue-0 --reuse", timeout=400)
    assert has_file(engine, p, "README", "issue-0")

    p.ok("agent spawn coder issue-1", timeout=400)

    git(engine, p, "-c", "user.email=a@b.c", "-c", "user.name=a", "commit", "--allow-empty", "-qm", "keep me", instance="issue-1")
    assert shell(engine, p, "echo 'not committed, and nobody asks' > wip.txt", instance="issue-1").returncode == 0
    kept = p.spawn("coder")
    p.ok("agent rm --force issue-1")
    assert not engine.volume_exists(p.worktree_volume("repo", "issue-1"))
    assert engine.volume_exists(p.base_volume("repo")) and has_file(engine, p, "README", kept)
    assert "issue-1" not in git(engine, p, "worktree", "list") and kept in git(engine, p, "worktree", "list")
    assert git(engine, p, "log", "-1", "--format=%s", "egzo/issue-1") == "keep me", "committed work stays on its branch"
    again = p.sh("agent spawn coder issue-1", timeout=400)
    assert again.returncode != 0 and "egzo/issue-1" in again.stderr and "--reuse" in again.stderr
    p.ok("agent spawn coder issue-1 --reuse", timeout=400)
    assert git(engine, p, "log", "-1", "--format=%s", instance="issue-1") == "keep me"
    assert git(engine, p, "branch", "--show-current", instance="issue-1") == "egzo/issue-1"

    p.ok("agent rm --force", kept)
    number = int(kept.rsplit("-", 1)[1])
    assert p.spawn("coder") == f"coder-{number + 1}", "an automatic name skips the one a branch was left under"

    leftover = p.worktree_volume("repo", "gone")  # a worktree volume left by no instance
    assert engine.run("volume", "create", *labels, leftover).returncode == 0
    p.ok("workspace rm repo --worktree gone")
    assert not engine.volume_exists(leftover) and engine.volume_exists(p.base_volume("repo"))


def test_the_base_survives_failed_clones_failed_fetches_down_and_up_and_goes_with_workspace_rm(live_project, engine, agent_image):
    p = live_project

    def document(*allow):
        return spec(egress={"default": {"allow": list(allow)}}, workspaces={"repo": {"git": {"url": REPO}}}, agents={"coder": custom(agent_image, workspaces=["repo"])})

    p.write(document("example.com"))  # github.com is not allowed: the clone fails and up goes on
    result = p.ok("project up", timeout=400)
    assert "github.com" in result.stderr and "repo" in result.stderr and [r.name for r in engine.containers(p.name)], "the project itself is up"
    assert not engine.volume_exists(p.base_volume("repo")), "an empty base volume is not left behind"
    failed = p.sh("agent spawn coder", timeout=400)
    assert failed.returncode != 0 and "github.com" in failed.stderr and "repo" in failed.stderr
    assert engine.instances(p.name) == [] and not [r for r in engine.resources(p.name) if r.name.endswith("_coder-1")], "a failed spawn leaves nothing"
    assert not engine.volume_exists(p.worktree_volume("repo", "coder-1")) and p.sh("ps --json").returncode == 0

    p.write(document("github.com"))
    again = p.ok("project up", timeout=400)
    assert engine.volume_exists(p.base_volume("repo")), f"the second up, which allows github.com, makes the base:\n{again.stdout}{again.stderr}\n{p.ok('proxy log').stdout[-1500:]}"
    assert git(engine, p, "remote", "get-url", "origin") == REPO
    other = spec(egress={"default": {"allow": ["github.com"]}}, workspaces={"repo": {"git": {"url": OTHER_REPO}}}, agents={"coder": custom(agent_image, workspaces=["repo"])})
    p.write(other)
    mismatch = p.ok("project up", timeout=400)
    assert "repo" in mismatch.stderr and "url" in mismatch.stderr, "a base that does not match the file is reported"
    assert git(engine, p, "remote", "get-url", "origin") == REPO, "and left alone"
    p.write(document("github.com"))
    p.ok("project up", timeout=400)
    first = p.spawn("coder")
    p.ok("agent rm --force", first)
    assert engine.run("volume", "rm", p.base_volume("repo")).returncode == 0
    second = p.spawn("coder")
    assert engine.volume_exists(p.base_volume("repo")) and has_file(engine, p, "README", second), "spawn clones the base itself when it is missing"
    p.ok("agent rm --force", second)

    p.write(document("example.com"))
    p.ok("project up", timeout=400)
    warned = p.sh("agent spawn coder", timeout=400)
    assert warned.returncode == 0, warned.stderr
    assert "github.com" in warned.stderr and "fetch" in warned.stderr.lower(), "a fetch that fails warns"
    assert has_file(engine, p, "README", warned.stdout.strip()), "and the worktree is made from the base as it is"
    assert shell(engine, p, "echo mine > .git/marker").returncode == 0

    down = p.ok("project down --force")
    assert "repo" in down.stdout and [r.name for r in engine.resources(p.name)] == [p.base_volume("repo")], "down keeps the base and says so"
    p.write(document("example.com"))  # github is unreachable: a clone would fail
    p.ok("project up", timeout=400)
    assert shell(engine, p, "cat .git/marker").stdout == "mine\n", "a new up reuses the base without cloning again"
    assert p.spawn("coder", "again", "--no-fetch") == "again" and has_file(engine, p, "README", "again")

    refused = p.sh("workspace rm repo")
    assert refused.returncode != 0 and "again" in refused.stderr and engine.volume_exists(p.base_volume("repo"))
    p.ok("agent rm --force again")
    p.ok("workspace rm repo")
    assert not engine.volume_exists(p.base_volume("repo"))
