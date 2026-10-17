# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""`egzo workspace ls | put | get | rm | fetch`: how files go in and out of the volumes that workspaces are, and how a person sees
and removes them. What a workspace is to an agent (mounts, owners, read-only) is in test_agent_containers.py, and git workspaces
(base clone, worktrees) in test_git_workspaces.py.

The put and get specs share one project that is up for the whole module (`sandbox`); the others change what exists and have their own."""

import json
import os
import re

import pytest

from support import custom, spec, table

GIT = {"git": {"url": "https://github.com/octocat/Hello-World.git"}}


def written(project, name, text):
    path = project.root / "inbox" / name
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text)
    return path


def listing(project, *args):
    return {row["NAME"]: row for row in table(project.ok("workspace ls", *args).stdout)}


@pytest.fixture(scope="module")
def sandbox(shared_project, engine):
    shared_project.up(spec(workspaces={"docs": {}, "repo": GIT}, agents={"coder": custom(engine.agent_image(), workspaces=["docs"])}))
    return shared_project


@pytest.fixture(autouse=True)
def start_from_no_instance(sandbox):
    sandbox.retire()
    yield


def test_put_copies_files_in_for_the_agent_user_and_get_copies_paths_out(sandbox, engine, tmp_path):
    p = sandbox
    written(p, "a.txt", "alpha\n")
    written(p, "sub/b.txt", "beta\n")
    p.ok("workspace put docs inbox/a.txt a.txt")
    p.ok("workspace put docs inbox/sub sub")
    name = engine.instance(p.name, p.spawn("coder")).name
    assert engine.exec(name, "sh", "-c", "cat /workspace/docs/a.txt /workspace/docs/sub/b.txt").stdout == "alpha\nbeta\n"
    owner = engine.in_volumes({p.volume("docs"): "/w"}, "stat -c %u:%g /w/a.txt /w/sub /w/sub/b.txt")
    assert set(owner.stdout.split()) == {f"{engine.uid}:{engine.gid}"}
    assert engine.exec(name, "sh", "-c", "echo more >> /workspace/docs/a.txt").returncode == 0, "the agent can write what was put"

    assert engine.exec(name, "sh", "-c", "mkdir -p /workspace/docs/out && echo result > /workspace/docs/out/r.txt").returncode == 0
    p.ok("workspace get docs out", tmp_path / "copy")
    assert (tmp_path / "copy" / "r.txt").read_text() == "result\n"
    p.ok("workspace get docs", tmp_path / "all")
    assert (tmp_path / "all" / "out" / "r.txt").read_text() == "result\n" and (tmp_path / "all" / "a.txt").read_text() == "alpha\nmore\n"


def test_put_and_get_refuse_what_is_outside_the_workspace_and_names_that_are_not_directory_workspaces(sandbox, tmp_path):
    p = sandbox
    written(p, "a.txt", "alpha\n")
    for destination in ("../escape.txt", "/etc/escape.txt", "a/../../escape.txt"):
        result = p.sh("workspace put docs inbox/a.txt", destination)
        assert result.returncode != 0 and "outside" in result.stderr, destination
    onto_git = p.sh("workspace put repo inbox/a.txt a.txt")
    assert onto_git.returncode != 0 and "repo" in onto_git.stderr and "git" in onto_git.stderr
    unknown = p.sh("workspace put ghost inbox/a.txt a.txt")
    assert unknown.returncode != 0 and "ghost" in unknown.stderr
    result = p.sh("workspace get ghost", tmp_path / "x")
    assert result.returncode != 0 and "ghost" in result.stderr


def test_ls_rm_and_put_before_up_follow_the_volumes_the_engine_has(live_project, engine, agent_image):
    p = live_project
    p.write(spec(egress={"default": {"allow": ["example.com"]}}, workspaces={"docs": {}, "other": {}, "repo": GIT},
                 agents={"coder": custom(agent_image, workspaces=["docs"]), "gitter": custom(agent_image, workspaces=["repo"])}))
    rows = listing(p)
    assert set(rows) == {"docs", "other", "repo"} and "PATH" not in rows["docs"]
    assert rows["docs"]["VOLUME"] == p.volume("docs") and rows["repo"]["VOLUME"] == p.base_volume("repo")
    assert (rows["docs"]["KIND"], rows["repo"]["KIND"]) == ("directory", "git")
    assert rows["docs"]["STATE"] == "missing" and rows["repo"]["STATE"] == "not cloned"
    [first] = [row for row in json.loads(p.ok("workspace ls --json").stdout) if row["name"] == "docs"]
    assert (first["kind"], first["volume"], first["state"], first["worktrees"]) == ("directory", p.volume("docs"), "missing", [])
    unreachable = p.sh("workspace ls", env={"DOCKER_HOST": "unix:///nonexistent.sock"})
    assert unreachable.returncode != 0 and "engine" in unreachable.stderr.lower(), "ls needs the engine and says so"

    written(p, "a.txt", "alpha\n")
    p.ok("workspace put docs inbox/a.txt a.txt")  # before up
    p.up()  # github.com is not allowed: the clone fails and up goes on
    rows = listing(p)
    assert rows["docs"]["STATE"] == "exists" and rows["repo"]["STATE"] == "not cloned"
    assert engine.exec(engine.instance(p.name, p.spawn("coder")).name, "cat", "/workspace/docs/a.txt").stdout == "alpha\n", "up keeps what was put"

    refused = p.sh("workspace rm docs")
    assert refused.returncode != 0 and "coder-1" in refused.stderr and engine.volume_exists(p.volume("docs"))
    p.ok("workspace rm other")
    assert not engine.volume_exists(p.volume("other"))
    assert p.sh("workspace rm ghost").returncode != 0
    p.ok("agent rm --force coder-1")
    p.ok("workspace rm --all")
    assert not [r for r in engine.resources(p.name) if r.name.startswith(f"{p.name}_ws.")]


@pytest.mark.usefixtures("github")
def test_fetch_clones_a_base_that_up_could_not_and_refreshes_one_that_is_there_and_ls_names_the_worktrees(live_project, engine, agent_image, tmp_path):
    p = live_project
    allow = spec(egress={"default": {"allow": ["github.com"]}}, workspaces={"repo": GIT, "docs": {}}, agents={"coder": custom(agent_image, workspaces=["repo"])})
    p.write(allow)
    unreached = p.sh("workspace fetch")
    assert unreached.returncode != 0 and "project up" in unreached.stderr, "fetch needs the project up"
    p.up(spec(egress={"default": {"allow": ["example.com"]}}, workspaces={"repo": GIT, "docs": {}}, agents={"coder": custom(agent_image, workspaces=["repo"])}))
    assert not engine.volume_exists(p.base_volume("repo")), "up could not reach github.com"
    p.up(allow)
    engine.run("volume", "rm", p.base_volume("repo"))  # the clone made by this up goes: the next command has to make it again

    for name in ("docs", "ghost"):
        refused = p.sh("workspace fetch", name)
        assert refused.returncode != 0 and name in refused.stderr, "not a git workspace"
    p.ok("workspace fetch", timeout=300)
    assert engine.in_volumes({p.base_volume("repo"): "/b"}, "test -d /b/.git").returncode == 0
    p.ok("workspace fetch repo", timeout=300)

    p.spawn("coder")
    p.spawn("coder")
    assert listing(p)["repo"]["WORKTREES"] == "coder-1,coder-2" and listing(p)["repo"]["STATE"] == "cloned"
    p.ok("workspace get repo/coder-1 README", tmp_path / "README")
    assert (tmp_path / "README").read_text().startswith("Hello World")
