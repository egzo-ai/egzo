# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""Workspaces: declaration, defaults, references, mounts and working directory.

A workspace is declared once, at the top of the project file, and belongs to the project. It is a directory on the
host (`path`, `./workspaces/<name>` by default) or a git repository (`git`, whose `path` holds the base clone and one
worktree per agent). An agent lists declared names only.
"""

from pathlib import Path

import pytest

from support import GIT_WORKSPACE, agent, mounts, spec


def workspaces(**declared):
    return {**GIT_WORKSPACE, **declared}


def path_of(resolved, name):
    return Path(resolved["workspaces"][name]["path"])


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


# --- the path of a workspace -------------------------------------------------------------------------------


def test_a_workspace_without_a_path_lives_under_workspaces_in_the_project_directory(project):
    resolved = project.resolved(spec(workspaces={"foo": {}}))
    assert path_of(resolved, "foo") == project.root / "workspaces" / "foo"


def test_an_empty_workspace_is_the_same_as_one_with_its_default_path(project):
    implicit = project.resolved(spec(workspaces={"foo": {}}))["workspaces"]["foo"]
    explicit = project.resolved(spec(workspaces={"foo": {"path": "./workspaces/foo"}}))["workspaces"]["foo"]
    assert implicit == explicit


def test_a_git_workspace_without_a_path_lives_under_workspaces_too(project):
    assert path_of(project.resolved(spec(workspaces=workspaces())), "repo") == project.root / "workspaces" / "repo"


def test_path_relocates_a_workspace_and_is_relative_to_the_project_directory(project):
    resolved = project.resolved(spec(workspaces={"docs": {"path": "./docs"}}))
    assert path_of(resolved, "docs") == project.root / "docs"


def test_path_may_be_absolute(project, tmp_path):
    target = tmp_path / "elsewhere"
    assert path_of(project.resolved(spec(workspaces={"docs": {"path": str(target)}})), "docs") == target


def test_path_relocates_a_git_workspace(project):
    document = spec(workspaces={"repo": {"git": {"url": "https://github.com/acme/shop.git"}, "path": "./clones/repo"}})
    assert path_of(project.resolved(document), "repo") == project.root / "clones" / "repo"


def test_the_path_is_relative_to_the_project_directory_when_run_from_a_subdirectory(project):
    project.mkdir("docs")
    project.mkdir("src/deep/docs")  # a ./docs next to the shell must not be mistaken for the project's
    project.write(spec(workspaces={"docs": {"path": "./docs"}}, agents={"coder": agent(workspaces=["docs"])}))
    result = project.egzo.run("config", cwd=project.root / "src" / "deep")
    assert result.returncode == 0, result.stderr
    assert path_of(result.yaml(), "docs") == project.root / "docs"
    assert result.yaml()["name"] == project.name


def test_the_path_is_relative_to_the_project_directory_when_run_from_outside_it(project, tmp_path):
    outside = tmp_path / "elsewhere"
    (outside / "docs").mkdir(parents=True)  # again, not the project's ./docs
    project.write(spec(workspaces={"docs": {"path": "./docs"}}, agents={"coder": agent(workspaces=["docs"])}))
    result = project.egzo.run("-f", project.root / "egzo.yaml", "config", cwd=outside)
    assert result.returncode == 0, result.stderr
    assert path_of(result.yaml(), "docs") == project.root / "docs"
    assert result.yaml()["name"] == project.name


def test_the_default_path_is_the_project_directorys_when_run_from_outside_it(project, tmp_path):
    project.write(spec(workspaces={"foo": {}}))
    result = project.egzo.run("-f", project.root / "egzo.yaml", "config", cwd=tmp_path)
    assert result.returncode == 0, result.stderr
    assert path_of(result.yaml(), "foo") == project.root / "workspaces" / "foo"


def test_a_project_reached_through_a_symlink_is_the_same_project(project, tmp_path):
    """Project identity is the directory: through a symlink it must resolve to the same real paths."""
    project.write(spec(workspaces=workspaces(), agents={"coder": agent(workspaces=["repo"])}))
    link = tmp_path / "link"
    link.symlink_to(project.root)
    direct = project.run("config").yaml()["workspaces"]["repo"]["path"]
    through = project.egzo.run("-f", link / "egzo.yaml", "config", cwd=tmp_path).yaml()["workspaces"]["repo"]["path"]
    assert through == direct


# --- directory workspaces ----------------------------------------------------------------------------------


def test_a_directory_workspace_is_mounted_from_its_path(project):
    resolved = project.resolved(spec(workspaces={"docs": {"path": "./docs"}}, agents={"coder": agent(workspaces=["docs"])}))
    mount = mounts(resolved, "coder")["/workspace/docs"]
    assert mount["host_path"] == str(project.root / "docs")
    assert mount["mode"] == "rw"


def test_a_workspace_without_a_source_is_a_host_directory_and_not_a_volume(project):
    resolved = project.resolved(
        spec(workspaces={"scratch": {}}, agents={"a": agent(workspaces=["scratch"]), "b": agent(workspaces=["scratch"])})
    )
    for name in ("a", "b"):
        mount = mounts(resolved, name)["/workspace/scratch"]
        assert mount["host_path"] == str(project.root / "workspaces" / "scratch")
        assert mount["mode"] == "rw"


def test_ro_makes_a_directory_workspace_read_only(project):
    resolved = project.resolved(spec(workspaces={"docs": {}}, agents={"coder": agent(workspaces=["docs:ro"])}))
    assert mounts(resolved, "coder")["/workspace/docs"]["mode"] == "ro"


def test_an_inline_host_path_is_not_a_workspace(project):
    """Paths are declared once, at the top: an agent names a workspace, not a directory."""
    project.mkdir("docs")
    result = project.config(spec(agents={"coder": agent(workspaces=["./docs"])}))
    assert result.returncode != 0
    assert "./docs" in result.stderr and "workspaces" in result.stderr


def test_an_absolute_inline_path_is_not_a_workspace_either(project):
    result = project.config(spec(agents={"coder": agent(workspaces=["/srv/data:ro"])}))
    assert result.returncode != 0
    assert "/srv/data" in result.stderr


def test_two_workspaces_cannot_share_a_path(project):
    result = project.config(spec(workspaces={"a": {"path": "./data"}, "b": {"path": "./data"}}))
    assert result.returncode != 0
    assert "data" in result.stderr


@pytest.mark.parametrize(
    "declared",
    [
        {"outer": {"path": "./data"}, "inner": {"path": "./data/inner"}},
        {"repo": {"git": {"url": "https://github.com/acme/shop.git"}, "path": "./workspaces"}, "foo": {}},
        {"state": {"path": "./.egzo/state"}},
        {"root": {"path": "/"}},
    ],
)
def test_overlapping_or_dangerous_paths_are_refused(project, declared):
    result = project.config(spec(workspaces=declared))
    assert result.returncode != 0
    assert "path" in result.stderr


def test_nothing_is_mounted_from_the_host_unless_a_workspace_is_listed(project):
    resolved = project.resolved(spec(workspaces={"docs": {}, **workspaces()}, agents={"coder": agent()}))
    assert resolved["agents"]["coder"]["workspaces"] == []


# --- git workspaces ----------------------------------------------------------------------------------------


def test_a_git_workspace_is_mounted_as_a_worktree_of_its_base(project):
    resolved = project.resolved(spec(workspaces=workspaces(), agents={"coder": agent(workspaces=["repo"])}))
    mount = mounts(resolved, "coder")["/workspace/repo"]
    assert mount["mode"] == "rw"
    assert "host_path" not in mount  # the container gets the base and its own worktree, not the directory


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


@pytest.mark.parametrize("mode", ["clone", "worktree", "shared"])
def test_the_mode_key_is_gone_and_the_error_says_so(project, mode):
    """Every agent gets a worktree of the base, always: there is nothing left to choose."""
    document = spec(workspaces={"repo": {"git": {"url": "https://github.com/acme/shop.git"}, "mode": mode}})
    result = project.config(document)
    assert result.returncode != 0
    assert "mode" in result.stderr and "worktree" in result.stderr


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
