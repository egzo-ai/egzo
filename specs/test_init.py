# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""`egzo init`: scaffolding a small, explicit egzo.yaml."""

import subprocess

import yaml


def git(project, *args):
    subprocess.run(["git", *args], cwd=project.root, check=True, capture_output=True)


def test_init_scaffolds_a_file_that_egzo_accepts(project):
    result = project.run("init")
    assert result.returncode == 0, result.stderr
    assert (project.root / "egzo.yaml").exists()
    assert project.run("config").returncode == 0


def test_init_scaffolds_one_agent_with_the_requested_harness(project):
    assert project.run("init", "--harness", "opencode").returncode == 0
    document = yaml.safe_load((project.root / "egzo.yaml").read_text())
    assert [a["harness"] for a in document["agents"].values()] == ["opencode"]


def test_init_never_overwrites_an_existing_file(project):
    project.write("name: mine\n")
    result = project.run("init")
    assert result.returncode != 0
    assert (project.root / "egzo.yaml").read_text() == "name: mine\n"


def test_init_rejects_an_unknown_harness(project):
    result = project.run("init", "--harness", "emacs-ai")
    assert result.returncode != 0
    assert "emacs-ai" in result.stderr
    assert not (project.root / "egzo.yaml").exists()


def test_init_mounts_nothing_from_the_directory(project):
    assert project.run("init").returncode == 0
    resolved = project.run("config").yaml()
    for agent in resolved["agents"].values():
        assert all("host_path" not in mount for mount in agent["workspaces"])


def test_init_in_a_git_repository_ignores_the_workspace_data(project):
    git(project, "init", "-q")
    assert project.run("init").returncode == 0
    assert ".egzo/" in (project.root / ".gitignore").read_text().splitlines()


def test_init_does_not_duplicate_the_ignore_entry(project):
    git(project, "init", "-q")
    (project.root / ".gitignore").write_text("node_modules/\n.egzo/\n")
    assert project.run("init").returncode == 0
    assert (project.root / ".gitignore").read_text().splitlines().count(".egzo/") == 1


def test_init_outside_a_git_repository_leaves_gitignore_alone(project):
    assert project.run("init").returncode == 0
    assert not (project.root / ".gitignore").exists()


def test_init_points_the_workspace_at_the_origin_over_https(project):
    git(project, "init", "-q")
    git(project, "remote", "add", "origin", "git@github.com:acme/shop.git")
    assert project.run("init").returncode == 0
    document = yaml.safe_load((project.root / "egzo.yaml").read_text())
    assert document["workspaces"]["repo"]["git"]["url"] == "https://github.com/acme/shop.git"


def test_init_never_copies_credentials_from_the_origin_url(project):
    git(project, "init", "-q")
    git(project, "remote", "add", "origin", "https://alice:ghp_abcdefghijklmnopqrstuvwxyz0123456789@github.com/acme/shop.git")
    assert project.run("init").returncode == 0
    text = (project.root / "egzo.yaml").read_text()
    assert "ghp_" not in text and "alice" not in text
    assert "github.com/acme/shop.git" in text


def test_init_for_the_custom_harness_scaffolds_a_file_egzo_accepts(project):
    assert project.run("init", "--harness", "custom").returncode == 0
    assert project.run("config").returncode == 0
