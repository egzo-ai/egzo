# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""`egzo project init`: scaffolding a small, explicit egzo.yaml."""

import subprocess

import yaml


def git(project, *args):
    subprocess.run(["git", *args], cwd=project.root, check=True, capture_output=True)


def test_init_scaffolds_a_file_that_egzo_accepts(project):
    result = project.sh("project init")
    assert result.returncode == 0, result.stderr
    assert (project.root / "egzo.yaml").exists()
    assert project.sh("project check --resolved").returncode == 0


def test_init_scaffolds_one_agent_with_the_requested_harness(project):
    assert project.sh("project init --harness opencode").returncode == 0
    document = yaml.safe_load((project.root / "egzo.yaml").read_text())
    assert [a["harness"] for a in document["agents"].values()] == ["opencode"]


def test_init_never_overwrites_an_existing_file(project):
    project.write("name: mine\n")
    result = project.sh("project init")
    assert result.returncode != 0
    assert (project.root / "egzo.yaml").read_text() == "name: mine\n"


def test_init_rejects_an_unknown_harness(project):
    result = project.sh("project init --harness emacs-ai")
    assert result.returncode != 0
    assert "emacs-ai" in result.stderr
    assert not (project.root / "egzo.yaml").exists()


def test_init_mounts_nothing_from_the_directory(project):
    assert project.sh("project init").returncode == 0
    resolved = project.sh("project check --resolved").yaml()
    for agent in resolved["agents"].values():
        assert all("host_path" not in mount for mount in agent["workspaces"])


def test_init_leaves_gitignore_alone_because_egzo_keeps_nothing_in_the_directory(project):
    git(project, "init", "-q")
    (project.root / ".gitignore").write_text("node_modules/\n")
    assert project.sh("project init").returncode == 0
    assert (project.root / ".gitignore").read_text() == "node_modules/\n"


def test_init_outside_a_git_repository_leaves_gitignore_alone(project):
    assert project.sh("project init").returncode == 0
    assert not (project.root / ".gitignore").exists()


def test_init_points_the_workspace_at_the_origin_over_https(project):
    git(project, "init", "-q")
    git(project, "remote", "add", "origin", "git@github.com:acme/shop.git")
    assert project.sh("project init").returncode == 0
    document = yaml.safe_load((project.root / "egzo.yaml").read_text())
    assert document["workspaces"]["repo"]["git"]["url"] == "https://github.com/acme/shop.git"


def test_init_never_copies_credentials_from_the_origin_url(project):
    git(project, "init", "-q")
    git(project, "remote", "add", "origin", "https://alice:ghp_abcdefghijklmnopqrstuvwxyz0123456789@github.com/acme/shop.git")
    assert project.sh("project init").returncode == 0
    text = (project.root / "egzo.yaml").read_text()
    assert "ghp_" not in text and "alice" not in text
    assert "github.com/acme/shop.git" in text


def test_init_for_the_custom_harness_scaffolds_a_file_egzo_accepts(project):
    assert project.sh("project init --harness custom").returncode == 0
    assert project.sh("project check --resolved").returncode == 0
