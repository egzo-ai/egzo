# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""The project's secret key (`secret_key`): every project names one, read from a vault like any secret. It seals what egzo keeps at rest
(the CA's private key first). What the file must say about it; what happens at rest is in `project/test_project_secret_key.py`."""

import re

import pytest

from support import SECRET_KEY, SECRET_KEY_VARIABLE, FakePass, spec, uses


def key_line(result):
    """The `project check` line about the secret key's secret."""
    return next(line for line in result.stdout.splitlines() if re.match(rf"(?:ok|warn|fail)\s+secret key/{SECRET_KEY_VARIABLE} ", line))


def test_a_project_without_a_secret_key_is_refused_with_the_command_that_makes_one_in_its_own_vault(project):
    result = project.config(spec(secret_key=None, vaults={"main": {"backend": "pass"}}))
    assert result.returncode != 0
    assert "secret_key" in result.stderr
    assert f"openssl rand -hex 32 | pass insert --multiline egzo/{project.name}-key" in result.stderr
    assert f"secret_key: main/egzo/{project.name}-key" in result.stderr


def test_without_any_vault_the_command_says_to_declare_one(project):
    result = project.config(spec(secret_key=None, vaults={}))
    assert result.returncode != 0
    assert "secret_key" in result.stderr and "backend: pass" in result.stderr and "openssl rand -hex 32" in result.stderr


def test_with_an_env_vault_only_the_command_sets_a_variable(project):
    result = project.config(spec(secret_key=None))
    assert result.returncode != 0
    assert "secret_key: main/" in result.stderr and "$(openssl rand -hex 32)" in result.stderr


def test_the_secret_key_is_a_secret_reference_listed_with_the_others(project):
    resolved = project.resolved(spec(**uses("main/TOKEN")))
    assert resolved["secret_key"] == SECRET_KEY["secret_key"]
    assert resolved["vaults"]["key"]["secrets"] == [SECRET_KEY_VARIABLE]


@pytest.mark.parametrize("reference, word", [("ghost/KEY", "ghost"), ("key/NOT-A-NAME", "NOT-A-NAME"), ("no-slash", "no-slash")])
def test_the_secret_key_follows_the_rules_of_any_secret_reference(project, reference, word):
    result = project.config(spec(secret_key=reference, vaults={"key": {"backend": "env"}}))
    assert result.returncode != 0
    assert "secret_key" in result.stderr and word in result.stderr


def test_project_check_reads_the_secret_key_and_refuses_one_shorter_than_32_bytes(project):
    project.write(spec())
    good = project.sh("project check")
    assert good.returncode == 0, good.stdout + good.stderr
    assert key_line(good).startswith("ok")

    short = project.sh("project check", env={SECRET_KEY_VARIABLE: "x" * 31})
    assert short.returncode == 1
    line = key_line(short)
    assert line.startswith("fail") and "32 bytes" in line
    assert "x" * 31 not in short.stdout + short.stderr

    missing = project.sh("project check", env={SECRET_KEY_VARIABLE: ""})
    assert missing.returncode == 1 and key_line(missing).startswith("fail")


def test_a_secret_key_in_pass_is_read_like_any_pass_secret(project, tmp_path):
    fake_pass = FakePass(tmp_path / "pass")
    fake_pass.put("egzo/shop-key", "f" * 64 + "\n")
    project.write(spec(secret_key="main/egzo/shop-key", vaults={"main": {"backend": "pass"}}))
    result = project.sh("project check", env=fake_pass.env)
    assert result.returncode == 0, result.stdout + result.stderr
    assert any(line.startswith("ok") and "main/egzo/shop-key" in line for line in result.stdout.splitlines())
