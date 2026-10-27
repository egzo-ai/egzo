# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""Vaults and secrets: the two backends (`env` and `pass`), what is refused, and what `egzo project check` says about a secret it cannot read.

A vault is a name and a backend. A secret is declared where it is used, by its reference `<vault>/<name>`: nothing lists it
a second time, and nothing says where it is: the backend knows."""

import re

import pytest

from support import FakePass, agent, spec, uses

ENV_WARNING = "Using ENV var based secret backend is not recommended."


def vault(backend, **extra):
    return {"main": {"backend": backend, **extra}}


def rows(result):
    """The secret lines of `egzo project check` (`ok   secret main/X is set`) as {reference: the rest of its line}."""
    found = {}
    for line in result.stdout.splitlines():
        match = re.match(r"(?:ok|warn|fail)\s+secret (\S+) (.*)", line)
        if match:
            found[match.group(1)] = line
    return found


@pytest.fixture
def fake_pass(tmp_path):
    return FakePass(tmp_path / "pass")


# --- a vault is a name and a backend; a reference declares the secret ----------------------------------------------


def test_a_reference_declares_the_secret_and_the_resolved_file_lists_it_under_its_vault(project):
    resolved = project.resolved(spec(**uses("main/TOKEN", "main/OTHER")))
    assert resolved["egress"]["default"]["services"]["svc0"]["secret"] == "main/TOKEN"
    assert resolved["vaults"]["main"] == {"backend": "env", "secrets": ["OTHER", "TOKEN"]}


def test_a_secret_referred_to_twice_is_listed_once(project):
    document = spec(**uses("main/TOKEN"))
    document["egress"]["other"] = {"services": {"deploy": {"hosts": ["deploy.example.com"], "inject": {"header": "X-Key"}, "secret": "main/TOKEN"}}}
    assert project.resolved(document)["vaults"]["main"]["secrets"] == ["TOKEN"]


def test_a_vault_cannot_list_its_secrets(project):
    """No backward compatibility: the list was a second copy of the references."""
    result = project.config(spec(vaults=vault("env", secrets=["TOKEN"]), egress=uses("main/TOKEN")["egress"]))
    assert result.returncode != 0
    assert "secrets" in result.stderr


def test_a_vault_needs_a_backend(project):
    result = project.config(spec(vaults={"main": {}}))
    assert result.returncode != 0
    assert "backend" in result.stderr


def test_an_unknown_backend_is_refused_and_names_the_ones_that_exist(project):
    result = project.config(spec(vaults=vault("not-a-backend")))
    assert result.returncode != 0
    assert 'unknown backend "not-a-backend"' in result.stderr
    assert "env" in result.stderr and "pass" in result.stderr


def test_there_is_no_file_backend(project):
    """Secrets in plain files are not supported: `file` is refused exactly like a name nobody ever heard of."""
    unknown = project.config(spec(vaults=vault("not-a-backend")))
    removed = project.config(spec(vaults=vault("file")))
    assert removed.returncode != 0
    assert 'unknown backend "file"' in removed.stderr
    assert removed.stderr == unknown.stderr.replace("not-a-backend", "file")


def test_a_reference_to_an_unknown_vault_is_refused_and_names_the_vaults_that_exist(project):
    document = spec(**uses("ghost/TOKEN"))
    document["vaults"]["other"] = {"backend": "env"}
    result = project.config(document)
    assert result.returncode != 0
    assert '"ghost"' in result.stderr
    assert "main" in result.stderr and "other" in result.stderr


def test_a_vault_nothing_refers_to_is_a_warning(project):
    document = spec(**uses("main/TOKEN"))
    document["vaults"]["spare"] = {"backend": "pass"}
    project.write(document)
    result = project.sh("project check", env={"TOKEN": "a-secret"})
    assert result.returncode == 0, result.stdout + result.stderr
    warnings = [line for line in result.stdout.splitlines() if line.startswith("warn")]
    assert any('"spare"' in line for line in warnings), result.stdout
    assert not any('"main"' in line for line in warnings), result.stdout


def test_secret_names_in_a_pass_vault_can_be_hierarchical(project):
    resolved = project.resolved(spec(**uses("main/company/project/test", backend="pass")))
    assert resolved["egress"]["default"]["services"]["svc0"]["secret"] == "main/company/project/test"
    assert resolved["vaults"]["main"]["secrets"] == ["company/project/test"]


@pytest.mark.parametrize("name", ["/absolute", "../outside", "a//b", "a/", "a/./b", "-c", "--version"])
def test_a_pass_secret_name_stays_inside_the_store(project, name):
    result = project.config(spec(**uses(f"main/{name}", backend="pass")))
    assert result.returncode != 0
    assert name in result.stderr


@pytest.mark.parametrize("name", ["company/token", "1TOKEN", "MY-TOKEN", "MY TOKEN"])
def test_an_env_secret_name_is_an_environment_variable_name(project, name):
    result = project.config(spec(**uses(f"main/{name}")))
    assert result.returncode != 0
    assert name in result.stderr


def test_a_vault_name_cannot_contain_a_slash(project):
    """The reference `vault/secret` is split at the first slash."""
    result = project.config(spec(vaults={"a/b": {"backend": "pass"}}))
    assert result.returncode != 0
    assert "a/b" in result.stderr


# --- the env backend works and says it is not recommended -------------------------------------------------------------


def test_the_env_backend_warns_that_it_is_not_recommended(project):
    result = project.config(spec(**uses("main/TOKEN")))
    assert result.returncode == 0, result.stderr
    assert result.stderr.count(ENV_WARNING) == 1


def test_a_project_without_an_env_vault_gets_no_such_warning(project, fake_pass):
    result = project.config(spec(**uses("main/token", backend="pass")), env=fake_pass.env)
    assert result.returncode == 0, result.stderr
    assert ENV_WARNING not in result.stderr


def test_the_env_backend_reads_the_variable_of_the_same_name(project):
    project.write(spec(**uses("main/SET_ONE", "main/UNSET_ONE")))
    result = project.sh("project check", env={"SET_ONE": "env-secret-value", "UNSET_ONE": ""})
    assert result.returncode == 1, result.stderr  # a secret that cannot be read is an error
    found = rows(result)
    assert "set" in found["main/SET_ONE"] and "missing" in found["main/UNSET_ONE"]
    assert "env-secret-value" not in result.stdout + result.stderr


# --- the pass backend runs `pass` -------------------------------------------------------------------------------


def test_the_pass_backend_runs_pass_show_with_the_name_and_its_slashes(project, fake_pass):
    fake_pass.put("company/project/test", "pass-secret-value\n")
    project.write(spec(**uses("main/company/project/test", "main/company/project/gone", backend="pass")))
    result = project.sh("project check", env=fake_pass.env)
    assert result.returncode == 1, result.stderr  # one of the two entries is not in the store
    found = rows(result)
    assert "set" in found["main/company/project/test"] and "missing" in found["main/company/project/gone"]
    assert "pass-secret-value" not in result.stdout + result.stderr
    assert (["show", "company/project/test"], "unset") in fake_pass.calls()


def test_a_pass_directory_is_not_a_secret(project, fake_pass):
    """`pass show company` on a directory prints a tree and succeeds: its first line must not become the secret."""
    fake_pass.put("company/token", "value\n")
    project.write(spec(**uses("main/company", backend="pass")))
    found = rows(project.sh("project check", env=fake_pass.env))
    assert "directory" in found["main/company"]


def test_project_check_says_what_pass_said_about_a_secret_it_cannot_read(project, fake_pass):
    fake_pass.put("empty", "\n")
    project.write(spec(**uses("main/empty", "main/gone", backend="pass")))
    found = rows(project.sh("project check", env=fake_pass.env))
    assert "empty" in found["main/empty"]
    assert "missing" in found["main/gone"] and "not in the password store" in found["main/gone"]


def test_pass_gets_the_environment_of_the_user_untouched(project, fake_pass):
    """Another store is the user's business: PASSWORD_STORE_DIR and the like are set in the shell, not the project."""
    fake_pass.put("token", "value\n")
    project.write(spec(**uses("main/token", backend="pass")))
    project.sh("project check", env={**fake_pass.env, "PASSWORD_STORE_DIR": "/spec/other-store"})
    assert {store_dir for _, store_dir in fake_pass.calls()} == {"/spec/other-store"}


@pytest.mark.parametrize("key", ["store", "path", "env", "password_store_dir", "gnupghome"])
def test_the_project_file_cannot_configure_pass(project, key):
    result = project.config(spec(vaults=vault("pass", **{key: "/somewhere"})))
    assert result.returncode != 0
    assert key in result.stderr


def test_a_missing_pass_program_is_named_in_the_error(project, tmp_path):
    empty = tmp_path / "empty-path"
    empty.mkdir()
    project.write(spec(**uses("main/token", backend="pass")))
    result = project.sh("project check", env={"PATH": str(empty)})
    assert result.returncode != 0
    assert "pass" in result.stderr


def test_a_secret_changed_in_pass_reaches_the_proxy_on_the_next_up(live_project, engine, agent_image, fake_pass):
    document = spec(
        **uses("main/api/key", backend="pass"),
        agents={"coder": agent(harness="custom", image=agent_image)},
    )
    live_project.write(document)
    live_project.env.update(fake_pass.env)
    fake_pass.put("api/key", "one\n")
    assert live_project.sh("project up", timeout=300).returncode == 0
    assert "nothing to do" in live_project.sh("project up", timeout=300).stdout
    fake_pass.put("api/key", "two\n")
    second = live_project.sh("project up", timeout=300)
    assert "load egress policy" in second.stdout
