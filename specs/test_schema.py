"""`egzo config`: validating egzo.yaml and printing the resolved configuration."""

import pytest

from support import GIT_WORKSPACE, agent, anthropic_profile, spec


@pytest.mark.todo
def test_the_minimal_level_1_file_is_valid(project):
    result = project.config(
        spec(
            egress={"default": anthropic_profile()},
            workspaces=GIT_WORKSPACE,
            agents={"claude": agent(workspaces=["repo"])},
        )
    )
    assert result.returncode == 0, result.stderr


@pytest.mark.todo
def test_a_project_without_agents_is_valid(project):
    assert project.config(spec()).returncode == 0


@pytest.mark.todo
def test_malformed_yaml_is_rejected(project):
    result = project.config("agents: [unclosed")
    assert result.returncode != 0
    assert result.stderr.strip()


@pytest.mark.todo
def test_an_unknown_top_level_key_is_rejected_and_named(project):
    result = project.config(spec(nonsense={"a": 1}))
    assert result.returncode != 0
    assert "nonsense" in result.stderr


@pytest.mark.todo
def test_users_are_never_declared_in_the_file(project):
    result = project.config(spec(users={"alice": {"roles": ["admin"]}}))
    assert result.returncode != 0
    assert "users" in result.stderr


@pytest.mark.todo
def test_users_are_rejected_inside_an_agent_too(project):
    result = project.config(spec(agents={"coder": agent(users=["alice"])}))
    assert result.returncode != 0
    assert "users" in result.stderr


@pytest.mark.todo
def test_an_agent_needs_a_harness(project):
    result = project.config(spec(agents={"coder": {}}))
    assert result.returncode != 0
    assert "harness" in result.stderr


@pytest.mark.todo
def test_an_unknown_harness_is_rejected_and_named(project):
    result = project.config(spec(agents={"coder": agent(harness="emacs-ai")}))
    assert result.returncode != 0
    assert "emacs-ai" in result.stderr


@pytest.mark.todo
def test_secret_looking_env_values_are_rejected(project):
    result = project.config(
        spec(agents={"coder": agent(env={"API_TOKEN": "sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789"})})
    )
    assert result.returncode != 0
    assert "sk-ant-api03" not in result.stderr


@pytest.mark.todo
def test_a_plain_env_value_is_accepted(project):
    result = project.config(spec(agents={"coder": agent(env={"FOO": "bar"})}))
    assert result.returncode == 0, result.stderr


@pytest.mark.todo
def test_a_vault_secret_needs_a_known_source_scheme(project):
    document = spec()
    document["vaults"]["main"]["secrets"]["BAD"] = {"from": "ftp:somewhere"}
    result = project.config(document)
    assert result.returncode != 0
    assert "ftp" in result.stderr


@pytest.mark.todo
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
