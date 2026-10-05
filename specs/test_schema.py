"""`egzo config`: validating egzo.yaml and printing the resolved configuration."""

import pytest

from support import GIT_WORKSPACE, agent, anthropic_profile, spec


def test_the_minimal_level_1_file_is_valid(project):
    result = project.config(
        spec(
            egress={"default": anthropic_profile()},
            workspaces=GIT_WORKSPACE,
            agents={"claude": agent(workspaces=["repo"])},
        )
    )
    assert result.returncode == 0, result.stderr


def test_a_project_without_agents_is_valid(project):
    assert project.config(spec()).returncode == 0


def test_malformed_yaml_is_rejected(project):
    result = project.config("agents: [unclosed")
    assert result.returncode != 0
    assert result.stderr.strip()


def test_an_unknown_top_level_key_is_rejected_and_named(project):
    result = project.config(spec(nonsense={"a": 1}))
    assert result.returncode != 0
    assert "nonsense" in result.stderr


def test_users_are_never_declared_in_the_file(project):
    result = project.config(spec(users={"alice": {"roles": ["admin"]}}))
    assert result.returncode != 0
    assert "users" in result.stderr


def test_users_are_rejected_inside_an_agent_too(project):
    result = project.config(spec(agents={"coder": agent(users=["alice"])}))
    assert result.returncode != 0
    assert "users" in result.stderr


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


def test_a_vault_secret_needs_a_known_source_scheme(project):
    document = spec()
    document["vaults"]["main"]["secrets"]["BAD"] = {"from": "ftp:somewhere"}
    result = project.config(document)
    assert result.returncode != 0
    assert "ftp" in result.stderr


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


# --- names are identities: they become containers, networks, volumes, DNS names and refs ------------------


@pytest.mark.parametrize("name", ["control", "proxy", "prep", "shared"])
def test_an_agent_cannot_take_the_name_of_a_sidecar_or_a_reserved_directory(project, name):
    result = project.config(spec(agents={name: agent(harness="custom", image="alpine")}))
    assert result.returncode != 0
    assert name in result.stderr and "reserved" in result.stderr


@pytest.mark.parametrize("name", ["control", "ca", "ca-private", "egress", "coder-home"])
def test_a_workspace_cannot_take_the_name_of_a_resource_egzo_owns(project, name):
    """A workspace called `control` would hand the agent the control volume, which holds the key every token derives from."""
    result = project.config(
        spec(workspaces={name: {}}, agents={"coder": agent(harness="custom", image="alpine", workspaces=[name])})
    )
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


# --- depends_on -----------------------------------------------------------------------------------------


def test_a_depends_on_cycle_is_rejected_and_the_cycle_is_named(project):
    custom = {"harness": "custom", "image": "alpine"}
    result = project.config(
        spec(agents={"a": {**custom, "depends_on": ["b"]}, "b": {**custom, "depends_on": ["c"]}, "c": {**custom, "depends_on": ["a"]}})
    )
    assert result.returncode != 0
    assert "cycle" in result.stderr
    assert "a -> b -> c -> a" in result.stderr


def test_an_agent_cannot_depend_on_itself(project):
    result = project.config(spec(agents={"a": {"harness": "custom", "image": "alpine", "depends_on": ["a"]}}))
    assert result.returncode != 0
    assert "cycle" in result.stderr or "itself" in result.stderr


def test_a_depends_on_chain_without_a_cycle_is_accepted(project):
    custom = {"harness": "custom", "image": "alpine"}
    document = spec(agents={"a": custom, "b": {**custom, "depends_on": ["a"]}, "c": {**custom, "depends_on": ["a", "b"]}})
    assert project.config(document).returncode == 0


# --- env: egzo owns its own variables -------------------------------------------------------------------


@pytest.mark.parametrize("variable", ["EGZO_TOKEN", "EGZO_CONTROL_URL", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "SSL_CERT_FILE", "GIT_SSL_CAINFO"])
def test_an_agent_cannot_override_the_variables_that_wire_it_to_the_sidecars(project, variable):
    result = project.config(spec(agents={"coder": agent(env={variable: "x"})}))
    assert result.returncode != 0
    assert variable in result.stderr


def test_a_key_named_users_is_fine_outside_the_places_users_could_be_declared(project):
    """`users` is rejected where a users section would be (see above), not wherever the word appears."""
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


def test_a_host_directory_holding_the_engine_socket_is_warned_about(project):
    result = project.config(spec(agents={"coder": agent(harness="custom", image="alpine", workspaces=["/var/run"])}))
    assert result.returncode == 0
    assert "docker.sock" in result.stderr


def test_mounting_the_filesystem_root_is_rejected(project):
    result = project.config(spec(agents={"coder": agent(harness="custom", image="alpine", workspaces=["/"])}))
    assert result.returncode != 0
