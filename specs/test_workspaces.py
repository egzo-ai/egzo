# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""Workspaces: declaration, references, mounts and working directory.

A workspace is declared once, at the top of the project file, and belongs to the project. It lives in an engine
volume, never on the host: it is an empty directory (`{}`) or a git repository (`git`, a base clone and one worktree
per instance). An agent lists declared names only. The volumes themselves are specified in `agent/test_agent_containers.py` and
`workspace/test_git_workspaces.py`, and the commands that fill them in `workspace/test_workspace_commands.py`.
"""

import pytest

from support import GIT_WORKSPACE, agent, mounts, spec


def workspaces(**declared):
    return {**GIT_WORKSPACE, **declared}


def test_an_undeclared_workspace_is_an_error(project):
    result = project.config(spec(agents={"coder": agent(workspaces=["nope"])}))
    assert result.returncode != 0
    assert "nope" in result.stderr


def test_a_single_workspace_is_the_working_directory(project):
    resolved = project.resolved(spec(workspaces={"docs": {}}, agents={"coder": agent(workspaces=["docs"])}))
    assert resolved["agents"]["coder"]["workdir"] == "/workspace/docs"
    assert "/workspace/docs" in mounts(resolved, "coder")


def test_two_workspaces_make_workspace_the_working_directory(project):
    resolved = project.resolved(
        spec(workspaces=workspaces(scratch={}), agents={"coder": agent(workspaces=["repo", "scratch"])})
    )
    assert resolved["agents"]["coder"]["workdir"] == "/workspace"
    assert {"/workspace/repo", "/workspace/scratch"} <= set(mounts(resolved, "coder"))


def test_workdir_overrides_the_default(project):
    resolved = project.resolved(
        spec(
            workspaces=workspaces(scratch={}),
            agents={"coder": agent(workspaces=["repo", "scratch"], workdir="scratch")},
        )
    )
    assert resolved["agents"]["coder"]["workdir"] == "/workspace/scratch"


def test_an_agent_without_workspaces_mounts_nothing(project):
    resolved = project.resolved(spec(agents={"coder": agent()}))
    assert resolved["agents"]["coder"]["workspaces"] == []


# --- there is no path: a workspace is a volume -------------------------------------------------------------


def test_path_is_not_a_workspace_key_and_the_error_says_what_to_do(project):
    project.write("name: demo\nworkspaces:\n  docs:\n    path: ./docs\n")
    result = project.sh("project check --resolved")
    assert result.returncode != 0
    assert 'egzo.yaml:4:5: workspaces.docs: unknown key "path"' in result.stderr
    assert "egzo workspace put" in result.stderr


def test_path_is_refused_on_a_git_workspace_too(project):
    document = spec(workspaces={"repo": {"git": {"url": "https://github.com/acme/shop.git"}, "path": "./clones/repo"}})
    result = project.config(document)
    assert result.returncode != 0
    assert 'unknown key "path"' in result.stderr


@pytest.mark.parametrize("workspace", [{}, {"git": {"url": "https://github.com/acme/shop.git"}}])
def test_the_resolved_workspace_has_no_path(project, workspace):
    resolved = project.resolved(spec(workspaces={"foo": workspace}, agents={"coder": agent(workspaces=["foo"])}))
    assert "path" not in resolved["workspaces"]["foo"]
    assert all("host_path" not in m for m in resolved["agents"]["coder"]["workspaces"])


def test_a_project_directory_reached_from_anywhere_resolves_the_same_workspaces(project, tmp_path):
    project.write(spec(workspaces=workspaces(scratch={}), agents={"coder": agent(workspaces=["repo", "scratch"])}))
    here = project.sh("project check --resolved").yaml()
    elsewhere = project.egzo.run("-f", project.root / "egzo.yaml", "project", "check", "--resolved", cwd=tmp_path).yaml()
    assert here["workspaces"] == elsewhere["workspaces"]
    assert here["agents"]["coder"]["workspaces"] == elsewhere["agents"]["coder"]["workspaces"]


# --- directory workspaces ----------------------------------------------------------------------------------


def test_a_directory_workspace_is_mounted_read_write_by_name(project):
    resolved = project.resolved(spec(workspaces={"docs": {}}, agents={"coder": agent(workspaces=["docs"])}))
    mount = mounts(resolved, "coder")["/workspace/docs"]
    assert (mount["name"], mount["mode"]) == ("docs", "rw")
    assert "host_path" not in mount


def test_ro_makes_a_directory_workspace_read_only(project):
    resolved = project.resolved(spec(workspaces={"docs": {}}, agents={"coder": agent(workspaces=["docs:ro"])}))
    assert mounts(resolved, "coder")["/workspace/docs"]["mode"] == "ro"


def test_an_inline_host_path_is_not_a_workspace(project):
    """Workspaces are declared once, at the top: an agent names a workspace, not a directory."""
    result = project.config(spec(agents={"coder": agent(workspaces=["./docs"])}))
    assert result.returncode != 0
    assert "./docs" in result.stderr and "workspaces" in result.stderr


def test_an_absolute_inline_path_is_not_a_workspace_either(project):
    result = project.config(spec(agents={"coder": agent(workspaces=["/srv/data:ro"])}))
    assert result.returncode != 0
    assert "/srv/data" in result.stderr


def test_nothing_is_mounted_unless_a_workspace_is_listed(project):
    resolved = project.resolved(spec(workspaces={"docs": {}, **workspaces()}, agents={"coder": agent()}))
    assert resolved["agents"]["coder"]["workspaces"] == []


# --- git workspaces ----------------------------------------------------------------------------------------


def test_a_git_workspace_is_mounted_as_a_worktree_of_its_base(project):
    resolved = project.resolved(spec(workspaces=workspaces(), agents={"coder": agent(workspaces=["repo"])}))
    mount = mounts(resolved, "coder")["/workspace/repo"]
    assert mount["mode"] == "rw"
    assert "host_path" not in mount  # the container gets the base and its own worktree volumes


def test_ro_on_a_git_workspace_is_an_error_that_explains_why(project):
    result = project.config(spec(workspaces=workspaces(), agents={"coder": agent(workspaces=["repo:ro"])}))
    assert result.returncode != 0
    assert "repo" in result.stderr
    assert "git" in result.stderr.lower()


def test_the_ro_error_offers_a_directory_workspace_and_no_cross_agent_reference(project):
    result = project.config(spec(workspaces=workspaces(), agents={"coder": agent(workspaces=["repo:ro"])}))
    assert result.returncode != 0
    assert "directory" in result.stderr
    assert "cross-agent" not in result.stderr.lower() and "<agent>" not in result.stderr


@pytest.mark.parametrize("url", ["git@github.com:acme/shop.git", "ssh://git@github.com/acme/shop.git", "/srv/repos/shop.git", "./shop"])
def test_git_sources_must_be_https(project, url):
    result = project.config(spec(workspaces={"repo": {"git": {"url": url}}}))
    assert result.returncode != 0
    assert "https" in result.stderr.lower()


def test_https_git_sources_are_accepted(project):
    result = project.config(spec(workspaces=workspaces()))
    assert result.returncode == 0, result.stderr


def test_a_workspace_cannot_be_both_a_git_repository_and_a_directory_source(project):
    document = spec(workspaces={"repo": {"git": {"url": "https://github.com/acme/shop.git"}, "source": "./repo"}})
    result = project.config(document)
    assert result.returncode != 0
    assert "source" in result.stderr


@pytest.mark.parametrize("reference", ["coder/repo:ro", "coder/repo", "ghost/repo:ro"])
def test_a_cross_agent_reference_is_not_a_workspace(project, reference):
    """A template cannot name an instance that does not exist yet, so one agent cannot mount another's worktree."""
    result = project.config(
        spec(
            workspaces=workspaces(),
            agents={"coder": agent(workspaces=["repo"]), "reviewer": agent(workspaces=[reference])},
        )
    )
    assert result.returncode != 0
    assert "repo" in result.stderr


def test_the_resolved_mounts_have_no_from_key(project):
    resolved = project.resolved(spec(workspaces=workspaces(), agents={"coder": agent(workspaces=["repo"])}))
    assert all("from" not in m for m in resolved["agents"]["coder"]["workspaces"])


# --- git sources carry no credentials and no option-looking values ---------------------------------------


def test_a_git_url_with_credentials_is_rejected_without_echoing_them(project):
    url = "https://alice:ghp_abcdefghijklmnopqrstuvwxyz0123456789@github.com/acme/shop.git"
    result = project.config(spec(workspaces={"repo": {"git": {"url": url}}}, agents={"coder": agent(workspaces=["repo"])}))
    assert result.returncode != 0
    assert "credentials" in result.stderr
    assert "ghp_" not in result.stderr and "alice" not in result.stderr


@pytest.mark.parametrize("branch", ["--detach", "-x", "a b", "a..b", "x~1", "x^", "x:y", "ends.lock", ""])
def test_a_git_branch_that_is_not_a_branch_name_is_rejected(project, branch):
    git = {"url": "https://github.com/acme/shop.git", "branch": branch}
    result = project.config(spec(workspaces={"repo": {"git": git}}, agents={"coder": agent(workspaces=["repo"])}))
    if branch == "":
        assert result.returncode == 0  # no branch means the default branch
    else:
        assert result.returncode != 0
        assert "branch" in result.stderr


@pytest.mark.parametrize("branch", ["main", "release/1.2", "feature/x_y-z", "v1.0.0"])
def test_ordinary_branch_names_are_accepted(project, branch):
    git = {"url": "https://github.com/acme/shop.git", "branch": branch}
    result = project.config(spec(workspaces={"repo": {"git": git}}, agents={"coder": agent(workspaces=["repo"])}))
    assert result.returncode == 0, result.stderr
