# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""`egzo project init`: scaffolding a small, explicit egzo.yaml."""

import stat
import subprocess

import pytest
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


def test_init_for_claude_code_signs_in_with_a_claude_subscription_and_needs_no_secret_but_the_key(project):
    result = project.sh("project init --harness claude-code")
    assert result.returncode == 0, result.stderr
    document = yaml.safe_load((project.root / "egzo.yaml").read_text())
    assert document["egress"]["default"]["services"] == {"claude": {"credential": {"method": "oauth"}}}
    assert "ANTHROPIC_API_KEY" not in (project.root / "egzo.yaml").read_text()
    assert "/login" in result.stdout, "init says where to sign in"
    assert project.sh("project check --resolved").returncode == 0


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


def test_init_in_a_git_repository_has_git_ignore_dotenv_and_keeps_the_rest_of_gitignore(project):
    """The secret key init writes to .env must not be committed by the next `git add .`."""
    git(project, "init", "-q")
    (project.root / ".gitignore").write_text("node_modules/")
    result = project.sh("project init")
    assert result.returncode == 0, result.stderr
    assert (project.root / ".gitignore").read_text() == "node_modules/\n.env\n"
    assert ".gitignore" in result.stdout


@pytest.mark.parametrize("ignore", [".env\n", "*.env\n/.env\n", ".*\n"])
def test_init_leaves_gitignore_alone_when_git_already_ignores_dotenv(project, ignore):
    git(project, "init", "-q")
    (project.root / ".gitignore").write_text(ignore)
    assert project.sh("project init").returncode == 0
    assert (project.root / ".gitignore").read_text() == ignore


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


def test_init_makes_the_secret_key_and_keeps_it_in_dotenv(project):
    result = project.sh("project init")
    assert result.returncode == 0, result.stderr
    document = yaml.safe_load((project.root / "egzo.yaml").read_text())
    vault, _, name = document["secret_key"].partition("/")
    assert document["vaults"][vault] == {"backend": "env"}
    dotenv = project.root / ".env"
    lines = [line for line in dotenv.read_text().splitlines() if line and not line.startswith("#")]
    assert len(lines) == 1 and lines[0].startswith(f"{name}=")
    value = lines[0].partition("=")[2]
    assert len(value) >= 32
    assert value not in result.stdout + result.stderr
    assert stat.S_IMODE(dotenv.stat().st_mode) == 0o600
    assert ".env" in result.stdout
    check = project.sh("project check")
    assert check.returncode == 0, check.stdout + check.stderr


def test_init_adds_the_secret_key_to_an_existing_dotenv_and_keeps_its_lines(project):
    (project.root / ".env").write_text("DATABASE_URL=postgres://localhost/shop")
    assert project.sh("project init").returncode == 0
    text = (project.root / ".env").read_text()
    assert text.startswith("DATABASE_URL=postgres://localhost/shop\n")
    assert "EGZO_PROJECT_SECRET_KEY=" in text


def test_init_never_replaces_a_secret_key_already_in_dotenv(project):
    """Another key cannot open what the first one sealed."""
    existing = "EGZO_PROJECT_SECRET_KEY=" + "a" * 64 + "\n"
    (project.root / ".env").write_text(existing)
    assert project.sh("project init").returncode == 0
    assert (project.root / ".env").read_text() == existing


def test_init_writes_no_secret_to_a_dotenv_that_git_tracks(project):
    """A tracked .env is committed: init prints the command that makes the key instead."""
    git(project, "init", "-q")
    (project.root / ".env").write_text("PUBLIC=1\n")
    git(project, "add", ".env")
    result = project.sh("project init")
    assert result.returncode == 0, result.stderr
    assert (project.root / ".env").read_text() == "PUBLIC=1\n"
    assert "openssl rand -hex 32" in result.stdout
