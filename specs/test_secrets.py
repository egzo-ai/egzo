# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""Vaults and secrets: the built-in vault `env`, the `pass` backend, what is refused, and what `egzo project check` says about a
secret it cannot read.

A vault is a name and a backend. A secret is declared where it is used, by its reference `<vault>/<name>`: nothing lists it
a second time, and nothing says where it is: the backend knows. The environment is the vault `env`, which every project has
and no file declares: `env/NAME` is the variable NAME."""

import re

import pytest

from support import FakePass, agent, spec, uses

ENV_WARNING = "Using ENV var based secret backend is not recommended."


def vault(backend, **extra):
    return {"main": {"backend": backend, **extra}}


def rows(result):
    """The secret lines of `egzo project check` (`ok   secret env/X is set`) as {reference: the rest of its line}."""
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
    resolved = project.resolved(spec(**uses("main/token", "main/other", backend="pass")))
    assert resolved["egress"]["default"]["services"]["svc0"]["credential"]["secret"] == "main/token"
    assert resolved["vaults"]["main"] == {"backend": "pass", "secrets": ["other", "token"]}


def test_a_secret_referred_to_twice_is_listed_once(project):
    document = spec(**uses("env/TOKEN"))
    document["egress"]["other"] = {"services": {"deploy": {"hosts": ["deploy.example.com"], "inject": {"header": "X-Key"}, "credential": "env/TOKEN"}}}
    assert project.resolved(document)["vaults"]["env"]["secrets"].count("TOKEN") == 1


def test_a_vault_cannot_list_its_secrets(project):
    """No backward compatibility: the list was a second copy of the references."""
    result = project.config(spec(vaults=vault("pass", secrets=["token"]), egress=uses("main/token")["egress"]))
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
    assert "pass" in result.stderr


def test_there_is_no_file_backend(project):
    """A file of secrets is only the env backend's `.env`, as Compose has it: `file` is refused exactly like a name nobody ever heard of."""
    unknown = project.config(spec(vaults=vault("not-a-backend")))
    removed = project.config(spec(vaults=vault("file")))
    assert removed.returncode != 0
    assert 'unknown backend "file"' in removed.stderr
    assert removed.stderr == unknown.stderr.replace("not-a-backend", "file")


def test_a_reference_to_an_unknown_vault_is_refused_and_names_the_vaults_that_exist(project):
    document = spec(**uses("ghost/TOKEN"), vaults={"other": {"backend": "pass"}})
    result = project.config(document)
    assert result.returncode != 0
    assert '"ghost"' in result.stderr
    assert "env" in result.stderr and "other" in result.stderr


def test_a_vault_nothing_refers_to_is_a_warning(project, fake_pass):
    fake_pass.put("token", "a-secret\n")
    document = spec(**uses("main/token", backend="pass"))
    document["vaults"]["spare"] = {"backend": "pass"}
    project.write(document)
    result = project.sh("project check", env=fake_pass.env)
    assert result.returncode == 0, result.stdout + result.stderr
    warnings = [line for line in result.stdout.splitlines() if line.startswith("warn")]
    assert any('"spare"' in line for line in warnings), result.stdout
    assert not any('"main"' in line for line in warnings), result.stdout


# --- the environment is the built-in vault `env` ----------------------------------------------------------------------


def test_the_env_vault_needs_no_declaration(project):
    document = spec(**uses("env/TOKEN"))
    assert "vaults" not in document
    resolved = project.resolved(document)
    assert resolved["vaults"]["env"]["backend"] == "env"
    assert "TOKEN" in resolved["vaults"]["env"]["secrets"]


def test_the_env_vault_is_listed_only_when_something_refers_to_it(project):
    resolved = project.resolved(spec(**uses("main/token", backend="pass"), secret_key="main/egzo/key"))
    assert "env" not in resolved["vaults"]


def test_the_env_vault_is_never_a_vault_nothing_refers_to(project, fake_pass):
    fake_pass.put("egzo/key", "k" * 32 + "\n")
    fake_pass.put("token", "a-secret\n")
    project.write(spec(**uses("main/token", backend="pass"), secret_key="main/egzo/key"))
    result = project.sh("project check", env=fake_pass.env)
    assert result.returncode == 0, result.stdout + result.stderr
    assert '"env"' not in result.stdout + result.stderr


@pytest.mark.parametrize("backend", ["pass", "env"])
def test_env_is_a_reserved_vault_name(project, backend):
    result = project.config(spec(vaults={"env": {"backend": backend}}))
    assert result.returncode != 0
    assert 'vault "env"' in result.stderr and "reserved" in result.stderr


def test_there_is_no_env_backend_to_declare(project):
    """No backward compatibility, and one way to say it: the environment is the vault `env`, and the error says how to write it."""
    result = project.config(spec(vaults={"main": {"backend": "env"}}))
    assert result.returncode != 0
    assert 'vault "main"' in result.stderr and "env/" in result.stderr


def test_secret_names_in_a_pass_vault_can_be_hierarchical(project):
    resolved = project.resolved(spec(**uses("main/company/project/test", backend="pass")))
    assert resolved["egress"]["default"]["services"]["svc0"]["credential"]["secret"] == "main/company/project/test"
    assert resolved["vaults"]["main"]["secrets"] == ["company/project/test"]


@pytest.mark.parametrize("name", ["/absolute", "../outside", "a//b", "a/", "a/./b", "-c", "--version"])
def test_a_pass_secret_name_stays_inside_the_store(project, name):
    result = project.config(spec(**uses(f"main/{name}", backend="pass")))
    assert result.returncode != 0
    assert name in result.stderr


@pytest.mark.parametrize("name", ["company/token", "1TOKEN", "MY-TOKEN", "MY TOKEN"])
def test_an_env_secret_name_is_an_environment_variable_name(project, name):
    result = project.config(spec(**uses(f"env/{name}")))
    assert result.returncode != 0
    assert name in result.stderr


def test_a_vault_name_cannot_contain_a_slash(project):
    """The reference `vault/secret` is split at the first slash."""
    result = project.config(spec(vaults={"a/b": {"backend": "pass"}}))
    assert result.returncode != 0
    assert "a/b" in result.stderr


# --- the env backend works and says it is not recommended -------------------------------------------------------------


def test_the_env_backend_warns_that_it_is_not_recommended(project):
    result = project.config(spec(**uses("env/TOKEN")))
    assert result.returncode == 0, result.stderr
    assert result.stderr.count(ENV_WARNING) == 1


def test_a_project_without_an_env_vault_gets_no_such_warning(project, fake_pass):
    result = project.config(spec(**uses("main/token", backend="pass"), secret_key="main/egzo/key"), env=fake_pass.env)
    assert result.returncode == 0, result.stderr
    assert ENV_WARNING not in result.stderr


def test_the_env_backend_reads_the_variable_of_the_same_name(project):
    project.write(spec(**uses("env/SET_ONE", "env/UNSET_ONE")))
    result = project.sh("project check", env={"SET_ONE": "env-secret-value", "UNSET_ONE": ""})
    assert result.returncode == 1, result.stderr  # a secret that cannot be read is an error
    found = rows(result)
    assert "set" in found["env/SET_ONE"] and "missing" in found["env/UNSET_ONE"]
    assert "env-secret-value" not in result.stdout + result.stderr


# --- the env backend also reads .env in the project directory, as Docker Compose does ----------------------------------
#
# A secret key is at least 32 bytes and `project check` says how many a short one has: the specs below see the value
# egzo got through that, without ever printing it.

KEY_32 = "k" * 32
KEY_31 = "k" * 31


def keyed(**sections):
    """A file whose secret key is `env/KEY`, the variable KEY."""
    return spec(**sections, secret_key="env/KEY")


def test_an_env_secret_missing_from_the_environment_is_read_from_dotenv_in_the_project_directory(project):
    project.write(spec(**uses("env/FROM_FILE")))
    project.write("FROM_FILE=dotenv-secret-value\n", filename=".env")
    result = project.sh("project check")
    assert result.returncode == 0, result.stdout + result.stderr
    assert "is set" in rows(result)["env/FROM_FILE"]
    assert "dotenv-secret-value" not in result.stdout + result.stderr


def test_project_check_says_a_secret_came_from_dotenv(project):
    project.write(spec(**uses("env/FROM_FILE", "env/FROM_SHELL")))
    project.write("FROM_FILE=a-value\n", filename=".env")
    found = rows(project.sh("project check", env={"FROM_SHELL": "b-value"}))
    assert ".env" in found["env/FROM_FILE"]
    assert ".env" not in found["env/FROM_SHELL"]


def test_the_environment_wins_over_dotenv(project):
    """Compose's order: a variable of the shell is never replaced by the file's."""
    project.write(keyed())
    project.write(f"KEY={KEY_32}\n", filename=".env")
    result = project.sh("project check", env={"KEY": KEY_31})
    assert "31 bytes" in rows(result)["env/KEY"]


def test_a_variable_set_to_nothing_in_the_environment_still_hides_dotenv(project):
    """Compose's order again: set counts, empty or not. The line says why the file was not read."""
    project.write(spec(**uses("env/TOKEN")))
    project.write("TOKEN=a-value\n", filename=".env")
    result = project.sh("project check", env={"TOKEN": ""})
    assert result.returncode == 1
    line = rows(result)["env/TOKEN"]
    assert "is set" not in line and ".env" in line


@pytest.mark.parametrize(
    "text",
    [
        f"KEY={KEY_31} # a comment after a space\n",
        f'KEY="{KEY_31}"\n',
        f"KEY='{KEY_31}'\n",
        f"export KEY={KEY_31}\n",
        f"# a comment line\n\nKEY={KEY_31}\n",
        f"PART={KEY_31[:20]}\nKEY=${{PART}}{KEY_31[20:]}\n",
        f"KEY='${{PART}}{KEY_31[:24]}'\n",  # single quotes are literal: `${PART}` is 7 bytes
    ],
)
def test_dotenv_is_read_with_the_syntax_of_compose(project, text):
    project.write(keyed())
    project.write(text, filename=".env")
    assert "31 bytes" in rows(project.sh("project check"))["env/KEY"]


def test_a_value_in_dotenv_may_refer_to_the_environment(project):
    project.write(keyed())
    project.write(f"KEY=${{FROM_SHELL}}{KEY_31[:11]}\n", filename=".env")
    assert "31 bytes" in rows(project.sh("project check", env={"FROM_SHELL": KEY_31[:20]}))["env/KEY"]


def test_the_project_secret_key_can_be_kept_in_dotenv(project):
    project.write(keyed())
    project.write(f"KEY={KEY_32}\n", filename=".env")
    result = project.sh("project check")
    assert result.returncode == 0, result.stdout + result.stderr


def test_dotenv_is_the_one_next_to_the_project_file_not_in_the_current_directory(project):
    project.write(keyed())
    project.write(f"KEY={KEY_31}\n", filename=".env")
    below = project.mkdir("src/deeper")
    (below / ".env").write_text(f"KEY={KEY_32}\n")
    result = project.egzo.run("project", "check", cwd=below, env=project.env)
    assert "31 bytes" in rows(result)["env/KEY"]


def test_a_dotenv_that_is_a_directory_is_ignored(project):
    project.write(spec(**uses("env/TOKEN")))
    project.mkdir(".env")
    result = project.sh("project check", env={"TOKEN": "a-value"})
    assert result.returncode == 0, result.stdout + result.stderr


def test_a_broken_dotenv_names_the_file_and_line_and_never_a_value(project):
    project.write(spec(**uses("env/TOKEN")))
    project.write("FIRST=fine\nBAD@NAME=a-secret-value\n", filename=".env")
    result = project.sh("project check")
    assert result.returncode == 1
    line = rows(result)["env/TOKEN"]
    assert ".env" in line and "line 2" in line, line
    assert "a-secret-value" not in result.stdout + result.stderr


def test_an_unterminated_quote_in_dotenv_never_shows_the_value(project):
    """Compose's parser quotes the value in its own message; egzo does not pass it on."""
    project.write(spec(**uses("env/TOKEN")))
    project.write('FIRST=fine\nTOKEN="unterminated-secret-value\n', filename=".env")
    result = project.sh("project check")
    assert result.returncode == 1
    assert ".env" in rows(result)["env/TOKEN"]
    assert "unterminated-secret-value" not in result.stdout + result.stderr


def test_a_broken_dotenv_is_not_read_when_the_environment_has_every_secret(project):
    project.write(spec(**uses("env/TOKEN")))
    project.write('TOKEN="unterminated\n', filename=".env")
    result = project.sh("project check", env={"TOKEN": "a-value"})
    assert result.returncode == 0, result.stdout + result.stderr



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
