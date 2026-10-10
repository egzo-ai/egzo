# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""Vaults and secrets: the two backends (`env` and `pass`), what is refused, and `egzo secrets ls`.

A vault is a backend and the names of the secrets in it. Nothing says where a secret is: the backend knows."""

import pytest

from support import FakePass, agent, spec

ENV_WARNING = "Using ENV var based secret backend is not recommended."


def vault(backend, *secrets, **extra):
    return {"main": {"backend": backend, "secrets": list(secrets), **extra}}


def service(secret):
    """A service that uses a secret, so a spec can check the reference `vault/secret` resolves."""
    return {"default": {"services": {"deploy": {"hosts": ["api.example.com"], "inject": {"header": "X-Key"}, "secret": secret}}}}


def rows(result):
    """`egzo secrets ls` as {reference: the rest of its line}."""
    return {line.split()[0]: line for line in result.stdout.splitlines()[1:]}


@pytest.fixture
def fake_pass(tmp_path):
    return FakePass(tmp_path / "pass")


# --- a vault is a backend and a list of names --------------------------------------------------------------


def test_a_vault_lists_the_names_of_its_secrets(project):
    resolved = project.resolved(spec(vaults=vault("env", "TOKEN"), egress=service("main/TOKEN")))
    assert resolved["egress"]["default"]["services"]["deploy"]["secret"] == "main/TOKEN"


def test_a_secret_has_no_source_of_its_own(project):
    """The old `from:` form: the backend already says where secrets are read from."""
    result = project.config(spec(vaults={"main": {"backend": "env", "secrets": {"TOKEN": {"from": "env:TOKEN"}}}}))
    assert result.returncode != 0


def test_a_vault_needs_a_backend(project):
    result = project.config(spec(vaults={"main": {"secrets": ["TOKEN"]}}))
    assert result.returncode != 0
    assert "backend" in result.stderr


def test_an_unknown_backend_is_refused_and_names_the_ones_that_exist(project):
    result = project.config(spec(vaults=vault("not-a-backend", "TOKEN")))
    assert result.returncode != 0
    assert 'unknown backend "not-a-backend"' in result.stderr
    assert "env" in result.stderr and "pass" in result.stderr


def test_the_file_backend_no_longer_exists(project):
    """Secrets in plain files are not supported: `file` is refused exactly like a name nobody ever heard of."""
    unknown = project.config(spec(vaults=vault("not-a-backend", "TOKEN")))
    removed = project.config(spec(vaults=vault("file", "TOKEN")))
    assert removed.returncode != 0
    assert 'unknown backend "file"' in removed.stderr
    assert removed.stderr == unknown.stderr.replace("not-a-backend", "file")


def test_secret_names_in_a_pass_vault_can_be_hierarchical(project):
    resolved = project.resolved(spec(vaults=vault("pass", "company/project/test"), egress=service("main/company/project/test")))
    assert resolved["egress"]["default"]["services"]["deploy"]["secret"] == "main/company/project/test"


def test_a_reference_to_a_name_the_vault_does_not_list_is_refused(project):
    result = project.config(spec(vaults=vault("pass", "company/project/test"), egress=service("main/company/project")))
    assert result.returncode != 0
    assert "main/company/project" in result.stderr


@pytest.mark.parametrize("name", ["/absolute", "../outside", "a//b", "a/", "a/./b", "-c", "--version"])
def test_a_pass_secret_name_stays_inside_the_store(project, name):
    result = project.config(spec(vaults=vault("pass", name)))
    assert result.returncode != 0
    assert name in result.stderr


@pytest.mark.parametrize("name", ["company/token", "1TOKEN", "MY-TOKEN", "MY TOKEN"])
def test_an_env_secret_name_is_an_environment_variable_name(project, name):
    result = project.config(spec(vaults=vault("env", name)))
    assert result.returncode != 0
    assert name in result.stderr


def test_a_vault_name_cannot_contain_a_slash(project):
    """The reference `vault/secret` is split at the first slash."""
    result = project.config(spec(vaults={"a/b": {"backend": "pass", "secrets": ["token"]}}))
    assert result.returncode != 0
    assert "a/b" in result.stderr


# --- the env backend works and says it is not recommended -------------------------------------------------------------


def test_the_env_backend_warns_that_it_is_not_recommended(project):
    result = project.config(spec(vaults=vault("env", "TOKEN")))
    assert result.returncode == 0, result.stderr
    assert result.stderr.count(ENV_WARNING) == 1


def test_a_project_without_an_env_vault_gets_no_such_warning(project, fake_pass):
    result = project.config(spec(vaults=vault("pass", "token")), env=fake_pass.env)
    assert result.returncode == 0, result.stderr
    assert ENV_WARNING not in result.stderr


def test_the_env_backend_reads_the_variable_of_the_same_name(project):
    project.write(spec(vaults=vault("env", "SET_ONE", "UNSET_ONE")))
    result = project.run("secrets", "ls", env={"SET_ONE": "env-secret-value", "UNSET_ONE": ""})
    assert result.returncode == 0, result.stderr
    found = rows(result)
    assert "set" in found["main/SET_ONE"] and "missing" in found["main/UNSET_ONE"]
    assert "env-secret-value" not in result.stdout + result.stderr


# --- the pass backend runs `pass` -------------------------------------------------------------------------------


def test_the_pass_backend_runs_pass_show_with_the_name_and_its_slashes(project, fake_pass):
    fake_pass.put("company/project/test", "pass-secret-value\n")
    project.write(spec(vaults=vault("pass", "company/project/test", "company/project/gone")))
    result = project.run("secrets", "ls", env=fake_pass.env)
    assert result.returncode == 0, result.stderr
    found = rows(result)
    assert "set" in found["main/company/project/test"] and "missing" in found["main/company/project/gone"]
    assert "pass-secret-value" not in result.stdout + result.stderr
    assert (["show", "company/project/test"], "unset") in fake_pass.calls()


def test_a_pass_directory_is_not_a_secret(project, fake_pass):
    """`pass show company` on a directory prints a tree and succeeds: its first line must not become the secret."""
    fake_pass.put("company/token", "value\n")
    project.write(spec(vaults=vault("pass", "company")))
    found = rows(project.run("secrets", "ls", env=fake_pass.env))
    assert "directory" in found["main/company"]


def test_secrets_ls_says_what_pass_said_about_a_secret_it_cannot_read(project, fake_pass):
    fake_pass.put("empty", "\n")
    project.write(spec(vaults=vault("pass", "empty", "gone")))
    found = rows(project.run("secrets", "ls", env=fake_pass.env))
    assert "empty" in found["main/empty"]
    assert "missing" in found["main/gone"] and "not in the password store" in found["main/gone"]


def test_pass_gets_the_environment_of_the_user_untouched(project, fake_pass):
    """Another store is the user's business: PASSWORD_STORE_DIR and the like are set in the shell, not the project."""
    fake_pass.put("token", "value\n")
    project.write(spec(vaults=vault("pass", "token")))
    project.run("secrets", "ls", env={**fake_pass.env, "PASSWORD_STORE_DIR": "/spec/other-store"})
    assert {store_dir for _, store_dir in fake_pass.calls()} == {"/spec/other-store"}


@pytest.mark.parametrize("key", ["store", "path", "env", "password_store_dir", "gnupghome"])
def test_the_project_file_cannot_configure_pass(project, key):
    result = project.config(spec(vaults=vault("pass", "token", **{key: "/somewhere"})))
    assert result.returncode != 0
    assert key in result.stderr


def test_a_missing_pass_program_is_named_in_the_error(project, tmp_path):
    empty = tmp_path / "empty-path"
    empty.mkdir()
    project.write(spec(vaults=vault("pass", "token")))
    result = project.run("secrets", "ls", env={"PATH": str(empty)})
    assert result.returncode != 0
    assert "pass" in result.stderr


def test_egzo_never_writes_a_secret(project):
    """Secrets are managed with the user's own password manager: there is no command to set or remove one."""
    for command in ("set", "rm"):
        result = project.run("secrets", command, "main/token")
        assert result.returncode != 0
        assert f'unknown command "{command}"' in result.stderr


def test_a_secret_changed_in_pass_reaches_the_proxy_on_the_next_up(live_project, engine, agent_image, fake_pass):
    document = spec(
        vaults=vault("pass", "api/key"),
        egress=service("main/api/key"),
        agents={"coder": agent(harness="custom", image=agent_image)},
    )
    live_project.write(document)
    live_project.env.update(fake_pass.env)
    fake_pass.put("api/key", "one\n")
    assert live_project.run("up", timeout=300).returncode == 0
    assert "nothing to do" in live_project.run("up", timeout=300).stdout
    fake_pass.put("api/key", "two\n")
    second = live_project.run("up", timeout=300)
    assert "load egress policy" in second.stdout
