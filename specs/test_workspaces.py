"""Workspaces: declaration, references, mounts, working directory and git sources."""

from pathlib import Path

import pytest

from support import GIT_WORKSPACE, agent, mounts, spec


def workspaces(**declared):
    return {**GIT_WORKSPACE, **declared}


def test_an_undeclared_workspace_is_an_error(project):
    result = project.config(spec(agents={"coder": agent(workspaces=["nope"])}))
    assert result.returncode != 0
    assert "nope" in result.stderr


def test_a_single_workspace_is_the_working_directory(project):
    resolved = project.resolved(spec(workspaces=workspaces(), agents={"coder": agent(workspaces=["repo"])}))
    assert resolved["agents"]["coder"]["workdir"] == "/workspace/repo"
    assert "/workspace/repo" in mounts(resolved, "coder")


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
            agents={"coder": agent(workspaces=["repo", "scratch"], workdir="repo")},
        )
    )
    assert resolved["agents"]["coder"]["workdir"] == "/workspace/repo"


def test_an_agent_without_workspaces_mounts_nothing(project):
    resolved = project.resolved(spec(agents={"coder": agent()}))
    assert resolved["agents"]["coder"]["workspaces"] == []


def test_a_declared_workspace_without_a_source_is_a_shared_volume(project):
    resolved = project.resolved(
        spec(workspaces={"scratch": {}}, agents={"a": agent(workspaces=["scratch"]), "b": agent(workspaces=["scratch"])})
    )
    assert mounts(resolved, "a")["/workspace/scratch"]["name"] == "scratch"
    assert mounts(resolved, "b")["/workspace/scratch"]["name"] == "scratch"
    assert mounts(resolved, "a")["/workspace/scratch"]["mode"] == "rw"


def test_an_inline_host_path_is_mounted_by_its_basename(project):
    docs = project.mkdir("docs")
    resolved = project.resolved(spec(agents={"coder": agent(workspaces=["./docs"])}))
    mount = mounts(resolved, "coder")["/workspace/docs"]
    assert mount["host_path"] == str(docs)
    assert mount["mode"] == "rw"


def test_a_relative_host_path_is_relative_to_the_project_directory_when_run_from_a_subdirectory(project):
    docs = project.mkdir("docs")
    project.mkdir("src/deep/docs")  # a ./docs next to the shell must not be mistaken for the project's
    project.write(spec(agents={"coder": agent(workspaces=["./docs"])}))
    result = project.egzo.run("config", cwd=project.root / "src" / "deep")
    assert result.returncode == 0, result.stderr
    assert mounts(result.yaml(), "coder")["/workspace/docs"]["host_path"] == str(docs)
    assert result.yaml()["name"] == project.name


def test_a_relative_host_path_is_relative_to_the_project_directory_when_run_from_outside_it(project, tmp_path):
    docs = project.mkdir("docs")
    outside = tmp_path / "elsewhere"
    (outside / "docs").mkdir(parents=True)  # again, not the project's ./docs
    project.write(spec(agents={"coder": agent(workspaces=["./docs"])}))
    result = project.egzo.run("-f", project.root / "egzo.yaml", "config", cwd=outside)
    assert result.returncode == 0, result.stderr
    assert mounts(result.yaml(), "coder")["/workspace/docs"]["host_path"] == str(docs)
    assert result.yaml()["name"] == project.name


def test_a_git_workspace_path_is_relative_to_the_project_directory_when_run_from_outside_it(project, tmp_path):
    document = spec(workspaces={"repo": {"git": {"url": "https://github.com/acme/shop.git"}, "path": "./clones/repo"}})
    project.write(document)
    result = project.egzo.run("-f", project.root / "egzo.yaml", "config", cwd=tmp_path)
    assert result.returncode == 0, result.stderr
    assert Path(result.yaml()["workspaces"]["repo"]["path"]) == project.root / "clones" / "repo"


def test_ro_makes_a_host_path_read_only(project):
    project.mkdir("docs")
    resolved = project.resolved(spec(agents={"coder": agent(workspaces=["./docs:ro"])}))
    assert mounts(resolved, "coder")["/workspace/docs"]["mode"] == "ro"


def test_inline_paths_with_the_same_basename_collide(project):
    project.mkdir("a/docs")
    project.mkdir("b/docs")
    result = project.config(spec(agents={"coder": agent(workspaces=["./a/docs", "./b/docs"])}))
    assert result.returncode != 0
    assert "docs" in result.stderr


def test_nothing_is_mounted_from_the_host_unless_the_file_says_so(project):
    resolved = project.resolved(spec(workspaces=workspaces(), agents={"coder": agent(workspaces=["repo"])}))
    assert all("host_path" not in m for m in resolved["agents"]["coder"]["workspaces"])


def test_ro_on_a_git_workspace_is_an_error_that_explains_why(project):
    result = project.config(spec(workspaces=workspaces(), agents={"coder": agent(workspaces=["repo:ro"])}))
    assert result.returncode != 0
    assert "repo" in result.stderr
    assert "git" in result.stderr.lower()


@pytest.mark.parametrize("url", ["git@github.com:acme/shop.git", "ssh://git@github.com/acme/shop.git", "/srv/repos/shop.git", "./shop"])
def test_git_sources_must_be_https(project, url):
    result = project.config(spec(workspaces={"repo": {"git": {"url": url}}}))
    assert result.returncode != 0
    assert "https" in result.stderr.lower()


def test_https_git_sources_are_accepted(project):
    result = project.config(spec(workspaces=workspaces()))
    assert result.returncode == 0, result.stderr


def test_the_default_git_mode_is_a_clone_per_agent(project):
    assert project.resolved(spec(workspaces=workspaces()))["workspaces"]["repo"]["mode"] == "clone"


@pytest.mark.parametrize("mode", ["clone", "worktree", "shared"])
def test_git_modes_are_accepted(project, mode):
    document = spec(workspaces={"repo": {"git": {"url": "https://github.com/acme/shop.git"}, "mode": mode}})
    assert project.resolved(document)["workspaces"]["repo"]["mode"] == mode


def test_an_unknown_git_mode_is_rejected(project):
    document = spec(workspaces={"repo": {"git": {"url": "https://github.com/acme/shop.git"}, "mode": "bind"}})
    result = project.config(document)
    assert result.returncode != 0
    assert "bind" in result.stderr


def test_git_workspaces_live_in_the_project_directory_by_default(project):
    resolved = project.resolved(spec(workspaces=workspaces()))
    assert Path(resolved["workspaces"]["repo"]["path"]) == project.root / ".egzo" / "workspaces" / "repo"


def test_path_relocates_a_git_workspace(project):
    document = spec(workspaces={"repo": {"git": {"url": "https://github.com/acme/shop.git"}, "path": "./clones/repo"}})
    assert Path(project.resolved(document)["workspaces"]["repo"]["path"]) == project.root / "clones" / "repo"


def test_path_may_be_absolute(project, tmp_path):
    target = tmp_path / "elsewhere"
    document = spec(workspaces={"repo": {"git": {"url": "https://github.com/acme/shop.git"}, "path": str(target)}})
    assert Path(project.resolved(document)["workspaces"]["repo"]["path"]) == target


def test_a_cross_agent_reference_mounts_the_other_agents_workspace_read_only(project):
    resolved = project.resolved(
        spec(
            workspaces=workspaces(),
            agents={"coder": agent(workspaces=["repo"]), "reviewer": agent(workspaces=["coder/repo:ro"])},
        )
    )
    mount = mounts(resolved, "reviewer")["/workspace/coder/repo"]
    assert mount["mode"] == "ro"
    assert mount["from"] == "coder"


def test_a_cross_agent_reference_must_be_read_only(project):
    result = project.config(
        spec(
            workspaces=workspaces(),
            agents={"coder": agent(workspaces=["repo"]), "reviewer": agent(workspaces=["coder/repo"])},
        )
    )
    assert result.returncode != 0
    assert ":ro" in result.stderr


def test_an_agent_cannot_reference_itself(project):
    result = project.config(
        spec(workspaces=workspaces(), agents={"coder": agent(workspaces=["repo", "coder/repo:ro"])})
    )
    assert result.returncode != 0


def test_a_cross_agent_reference_needs_the_other_agent_to_list_the_workspace(project):
    result = project.config(
        spec(
            workspaces=workspaces(scratch={}),
            agents={"coder": agent(workspaces=["repo"]), "reviewer": agent(workspaces=["coder/scratch:ro"])},
        )
    )
    assert result.returncode != 0
    assert "scratch" in result.stderr


def test_a_cross_agent_reference_to_an_unknown_agent_is_an_error(project):
    result = project.config(
        spec(workspaces=workspaces(), agents={"reviewer": agent(workspaces=["ghost/repo:ro"])})
    )
    assert result.returncode != 0
    assert "ghost" in result.stderr
