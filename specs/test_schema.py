# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""`egzo project check --resolved`: validating egzo.yaml and printing the resolved configuration."""

import pytest

from support import GIT_WORKSPACE, agent, anthropic_profile, spec


def test_malformed_yaml_is_rejected(project):
    result = project.config("agents: [unclosed")
    assert result.returncode != 0
    assert result.stderr.strip()


def test_an_unknown_top_level_key_is_rejected_and_named(project):
    result = project.config(spec(nonsense={"a": 1}))
    assert result.returncode != 0
    assert "nonsense" in result.stderr


def test_an_agent_needs_a_harness(project):
    result = project.config(spec(agents={"coder": {}}))
    assert result.returncode != 0
    assert "harness" in result.stderr


def test_an_unknown_harness_is_rejected_and_named(project):
    result = project.config(spec(agents={"coder": agent(harness="emacs-ai")}))
    assert result.returncode != 0
    assert "emacs-ai" in result.stderr


def test_secret_looking_env_values_are_rejected(project):
    result = project.config(
        spec(agents={"coder": agent(env={"API_TOKEN": "sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789"})})
    )
    assert result.returncode != 0
    assert "sk-ant-api03" not in result.stderr


def test_a_plain_env_value_is_accepted(project):
    result = project.config(spec(agents={"coder": agent(env={"FOO": "bar"})}))
    assert result.returncode == 0, result.stderr


def test_config_never_prints_secret_values(project):
    secret = "spec-secret-value-0123456789"
    result = project.config(
        spec(
            egress={"default": anthropic_profile()},
            agents={"coder": agent()},
        ),
        env={"ANTHROPIC_API_KEY": secret},
    )
    assert result.returncode == 0, result.stderr
    assert secret not in result.stdout
    assert secret not in result.stderr


def test_a_prompt_that_is_not_an_existing_file_is_rejected(project):
    result = project.config(spec(agents={"coder": agent(harness="custom", image="alpine", prompt="./prompts/missing.md")}))
    assert result.returncode != 0
    assert "prompts/missing.md" in result.stderr
    (project.root / "prompts").mkdir()
    (project.root / "prompts" / "missing.md").write_text("be brief\n")
    assert project.config(spec(agents={"coder": agent(harness="custom", image="alpine", prompt="./prompts/missing.md")})).returncode == 0


def test_a_prompt_larger_than_64_kb_is_rejected_because_it_travels_in_the_template(project):
    (project.root / "prompts").mkdir()
    (project.root / "prompts" / "big.md").write_text("x" * (64 * 1024 + 1))
    result = project.config(spec(agents={"coder": agent(harness="custom", image="alpine", prompt="./prompts/big.md")}))
    assert result.returncode != 0
    assert "prompts/big.md" in result.stderr and "64 KB" in result.stderr


# --- names are identities: they become containers, networks, volumes, DNS names and refs ------------------


@pytest.mark.parametrize("name", ["control", "proxy", "prep"])
def test_an_agent_cannot_take_the_name_of_a_sidecar_or_egzos_own_identity(project, name):
    result = project.config(spec(agents={name: agent(harness="custom", image="alpine")}))
    assert result.returncode != 0
    assert name in result.stderr and "reserved" in result.stderr


@pytest.mark.parametrize("name", ["my agent", "Coder", "-dash", "a/b", "a:b", "ünï", ".hidden", "a" * 64])
def test_an_agent_name_is_a_plain_lowercase_name(project, name):
    result = project.config(spec(agents={name: agent(harness="custom", image="alpine")}))
    assert result.returncode != 0
    assert "agent" in result.stderr


@pytest.mark.parametrize("name", ["..", ".", "My Repo", "a b", "x" * 64])
def test_a_workspace_name_is_a_plain_lowercase_name(project, name):
    result = project.config(spec(workspaces={name: {}}))
    assert result.returncode != 0
    assert "workspace" in result.stderr


def test_plain_names_with_dashes_underscores_and_digits_are_accepted(project):
    document = spec(
        workspaces={"data_2": {}}, agents={"coder-1": agent(harness="custom", image="alpine", workspaces=["data_2"])}
    )
    assert project.config(document).returncode == 0


# --- nothing starts at `up`, so there is nothing to order: no depends_on -------------------------------


def test_an_agent_has_no_depends_on(project):
    result = project.config(spec(agents={"a": {"harness": "custom", "image": "alpine", "depends_on": ["b"]}, "b": agent()}))
    assert result.returncode != 0
    assert "depends_on" in result.stderr


# --- env: egzo owns its own variables -------------------------------------------------------------------


@pytest.mark.parametrize("variable", ["EGZO_TOKEN", "EGZO_CONTROL_URL", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "SSL_CERT_FILE", "GIT_SSL_CAINFO"])
def test_an_agent_cannot_override_the_variables_that_wire_it_to_the_sidecars(project, variable):
    result = project.config(spec(agents={"coder": agent(env={variable: "x"})}))
    assert result.returncode != 0
    assert variable in result.stderr


def test_a_key_named_users_is_fine_outside_the_places_users_could_be_declared(project):
    """`users` is an unknown key of a section, but a perfectly good name for a workspace or a variable."""
    document = spec(
        workspaces={"users": {}},
        agents={"coder": agent(harness="custom", image="alpine", env={"users": "4"}, workspaces=["users"])},
    )
    result = project.config(document)
    assert result.returncode == 0, result.stderr


# --- keys that would do nothing are not accepted ----------------------------------------------------------


@pytest.mark.parametrize(
    "document, key",
    [
        (spec(proxy={"audit": False}), "audit"),
        (spec(control={"tools": ["status"]}), "tools"),
        (spec(agents={"coder": agent(tools=["control"])}), "tools"),
    ],
)
def test_a_key_that_has_no_effect_is_an_error_not_a_silent_no_op(project, document, key):
    result = project.config(document)
    assert result.returncode != 0
    assert key in result.stderr


def test_the_proxy_image_can_be_overridden_and_is_shown(project):
    resolved = project.resolved(spec(proxy={"image": "registry.example/egzo:9"}))
    assert resolved["proxy"]["image"] == "registry.example/egzo:9"


# --- resources and paths --------------------------------------------------------------------------------


@pytest.mark.parametrize("resources", [{"cpus": -1}, {"memory": "lots"}, {"memory": "-4g"}])
def test_invalid_resources_are_rejected_when_the_file_is_read(project, resources):
    result = project.config(spec(agents={"coder": agent(harness="custom", image="alpine", resources=resources)}))
    assert result.returncode != 0
    assert "resources" in result.stderr or "memory" in result.stderr or "cpus" in result.stderr


def test_a_workdir_cannot_climb_out_of_its_workspace(project):
    result = project.config(
        spec(
            workspaces={"repo": {}},
            agents={"coder": agent(harness="custom", image="alpine", workspaces=["repo"], workdir="repo/../../etc")},
        )
    )
    assert result.returncode != 0
    assert "workdir" in result.stderr


# --- unknown keys: every one is reported, where it is, with a suggestion for a typo -------------------------------------


def test_an_unknown_key_says_where_it_is_and_what_the_valid_keys_are(project):
    result = project.config(spec(workspaces={"repo": {"git": {"url": "https://github.com/acme/shop.git"}, "mode": "clone"}}))
    assert result.returncode != 0
    assert 'egzo.yaml:' in result.stderr
    assert 'workspaces.repo: unknown key "mode" (valid keys: git)' in result.stderr
    assert "worktree" not in result.stderr  # no story about what the key used to do


def test_a_typo_gets_a_suggestion(project):
    result = project.config(spec(agents={"coder": agent(imge="x")}))
    assert result.returncode != 0
    assert 'agents.coder: unknown key "imge", did you mean "image"?' in result.stderr


def test_every_unknown_key_is_reported_not_only_the_first(project):
    result = project.config(spec(nonsense=1, agents={"coder": agent(imge="x", harnes="y")}, workspaces={"w": {"gti": "./w"}}))
    assert result.returncode != 0
    for expected in ('unknown key "nonsense"', 'unknown key "imge"', 'unknown key "harnes"', 'unknown key "gti"'):
        assert expected in result.stderr
    assert "yaml: unmarshal errors" not in result.stderr


def test_an_unknown_key_is_reported_with_its_line_and_column(project):
    project.write("name: demo\nworkspaces:\n  repo:\n    gti: ./x\n")
    result = project.sh("project check --resolved")
    assert result.returncode != 0
    assert 'egzo.yaml:4:5: workspaces.repo: unknown key "gti", did you mean "git"?' in result.stderr


def test_an_unknown_key_in_a_service_definition_is_reported_the_same_way(project):
    result = project.config(spec(egress={"default": {"services": {"x": {"hosts": ["a.com"], "hostss": []}}}}))
    assert result.returncode != 0
    assert 'egress.default.services.x: unknown key "hostss", did you mean "hosts"?' in result.stderr


def test_project_check_prints_every_error_and_exits_1(project):
    project.write(spec(nonsense=1, agents={"coder": agent(imge="x")}))
    result = project.sh("project check")
    assert result.returncode == 1
    fails = [line for line in result.stdout.splitlines() if line.startswith("fail")]
    assert len(fails) == 2, result.stdout
    assert any("nonsense" in line for line in fails) and any("imge" in line for line in fails)


def test_a_wrong_type_is_reported_with_its_line(project):
    project.write("name: demo\nagents:\n  coder:\n    harness: custom\n    workspaces: not-a-list\n")
    result = project.sh("project check --resolved")
    assert result.returncode != 0
    assert "egzo.yaml:5" in result.stderr


# --- the inject section ----------------------------------------------------------------------------------------


def test_inject_settings_are_validated(project):
    base = {"harness": "custom", "image": "alpine"}
    for inject, complaint in (
        ({"human_quiet": "soon"}, "human_quiet"),
        ({"ack_timeout": "-3s"}, "ack_timeout"),
        ({"idle_signal": "vibes"}, "idle_signal"),
        ({"quiescence": "abc"}, "quiescence"),
        ({"nonsense": 1}, "nonsense"),
    ):
        result = project.config(spec(agents={"coder": {**base, "inject": inject}}))
        assert result.returncode != 0, inject
        assert complaint in result.stderr


def test_inject_defaults_are_resolved(project):
    resolved = project.resolved(spec(agents={"coder": {"harness": "claude-code"}}, egress={"default": {"allow": ["x.test"]}}))
    assert resolved["agents"]["coder"]["inject"] == {"human_quiet": "30s", "ack_timeout": "60s", "idle_signal": "state", "quiescence": "5s"}
    resolved = project.resolved(spec(agents={"coder": {"harness": "custom", "image": "alpine"}}))
    assert resolved["agents"]["coder"]["inject"]["idle_signal"] == "quiescence"
