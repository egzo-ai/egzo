# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""Egress profiles: deny by default, services, extend, built-ins, validation."""

import pytest

from support import agent, anthropic_profile, profile, spec

DEPLOY_HOSTS = ["api.deploy.example.com"]
DEPLOY_INJECT = {"header": "Authorization", "value": "Bearer {secret}"}


def deploy_api(**fields):
    return {"hosts": DEPLOY_HOSTS, "inject": DEPLOY_INJECT, **fields}


# --- defaults: nothing is allowed unless declared ---------------------------------------------


def test_without_any_egress_an_agent_gets_an_empty_default_profile(project):
    resolved = project.resolved(spec(agents={"coder": agent()}))
    assert resolved["agents"]["coder"]["egress"] == "default"
    assert profile(resolved, "default")["allow"] == []
    assert profile(resolved, "default")["services"] == {}


def test_an_agent_naming_no_profile_gets_default(project):
    resolved = project.resolved(spec(egress={"default": anthropic_profile()}, agents={"coder": agent()}))
    assert resolved["agents"]["coder"]["egress"] == "default"
    assert "platform.claude.com" in profile(resolved, "default")["allow"]


def test_an_agent_links_to_one_named_profile(project):
    resolved = project.resolved(
        spec(
            egress={"default": anthropic_profile(), "strict": {}},
            agents={"coder": agent(egress="strict")},
        )
    )
    assert resolved["agents"]["coder"]["egress"] == "strict"


def test_an_unknown_profile_is_an_error(project):
    result = project.config(spec(agents={"coder": agent(egress="ghost")}))
    assert result.returncode != 0
    assert "ghost" in result.stderr


def test_there_is_no_deny_key_and_no_default_key(project):
    for key, value in (("deny", ["example.com"]), ("default", "deny")):
        result = project.config(spec(egress={"default": {key: value}}))
        assert result.returncode != 0, key
        assert key in result.stderr


def test_egress_must_be_a_map_of_profiles(project):
    result = project.config(spec(egress={"allow": ["example.com"]}))
    assert result.returncode != 0


def test_unknown_profile_keys_are_rejected(project):
    result = project.config(spec(egress={"default": {"nonsense": 1}}))
    assert result.returncode != 0
    assert "nonsense" in result.stderr


def test_allow_star_grants_the_whole_internet(project):
    resolved = project.resolved(spec(egress={"default": {"allow": ["*"]}}))
    assert profile(resolved, "default")["allow"] == ["*"]


def test_allow_accepts_hosts_and_globs(project):
    resolved = project.resolved(spec(egress={"default": {"allow": ["example.com", "*.pypi.org"]}}))
    assert set(profile(resolved, "default")["allow"]) == {"example.com", "*.pypi.org"}


# --- services ----------------------------------------------------------------------------------


def test_the_anthropic_oauth_service_is_built_in(project):
    """A Claude subscription token (`claude setup-token`) is not an API key: api.anthropic.com takes it as a bearer token."""
    profile_ = {"allow": ["platform.claude.com"], "services": {"anthropic-oauth": "main/ANTHROPIC_API_KEY"}}
    service = profile(project.resolved(spec(egress={"default": profile_})), "default")["services"]["anthropic-oauth"]
    assert service["hosts"] == ["api.anthropic.com"]
    assert service["inject"] == {"header": "Authorization", "value": "Bearer {secret}"}


def test_the_anthropic_service_is_built_in(project):
    service = profile(project.resolved(spec(egress={"default": anthropic_profile()})), "default")["services"]["anthropic"]
    assert service["hosts"] == ["api.anthropic.com"]
    assert service["inject"]["header"] == "x-api-key"
    assert service["secret"] == "main/ANTHROPIC_API_KEY"


def test_the_github_service_is_built_in(project):
    resolved = project.resolved(spec(egress={"default": {"services": {"github": "main/GITHUB_TOKEN"}}}))
    service = profile(resolved, "default")["services"]["github"]
    assert set(service["hosts"]) == {"github.com", "api.github.com"}
    assert service["secret"] == "main/GITHUB_TOKEN"


def test_an_object_defines_a_custom_service(project):
    resolved = project.resolved(
        spec(egress={"default": {"services": {"deploy-api": deploy_api(secret="main/DEPLOY_TOKEN")}}})
    )
    service = profile(resolved, "default")["services"]["deploy-api"]
    assert service["hosts"] == DEPLOY_HOSTS
    assert service["secret"] == "main/DEPLOY_TOKEN"


def test_a_service_object_needs_hosts(project):
    result = project.config(spec(egress={"default": {"services": {"deploy-api": {"inject": DEPLOY_INJECT}}}}))
    assert result.returncode != 0
    assert "hosts" in result.stderr


def test_a_service_that_injects_needs_a_secret(project):
    result = project.config(spec(egress={"default": {"services": {"deploy-api": deploy_api()}}}))
    assert result.returncode != 0
    assert "deploy-api" in result.stderr


def test_a_service_without_inject_is_a_pure_allowlist_and_needs_no_secret(project):
    resolved = project.resolved(
        spec(egress={"default": {"services": {"pypi": {"hosts": ["pypi.org", "files.pythonhosted.org"]}}}})
    )
    service = profile(resolved, "default")["services"]["pypi"]
    assert service["hosts"] == ["pypi.org", "files.pythonhosted.org"]
    assert "secret" not in service


def test_a_string_for_an_unknown_service_is_an_error(project):
    result = project.config(spec(egress={"default": {"services": {"nonesuch": "main/ANTHROPIC_API_KEY"}}}))
    assert result.returncode != 0
    assert "nonesuch" in result.stderr


def test_a_misspelled_service_suggests_the_close_name(project):
    result = project.config(spec(egress={"default": {"services": {"antropic": "main/ANTHROPIC_API_KEY"}}}))
    assert result.returncode != 0
    assert "anthropic" in result.stderr


def test_a_null_service_value_is_rejected(project):
    result = project.config(spec(egress={"default": {"services": {"anthropic": None}}}))
    assert result.returncode != 0


@pytest.mark.parametrize("reference", ["main/MISSING", "nope/ANTHROPIC_API_KEY", "not-a-reference"])
def test_a_secret_reference_must_exist_in_a_vault(project, reference):
    result = project.config(spec(egress={"default": {"services": {"anthropic": reference}}}))
    assert result.returncode != 0
    assert reference.split("/")[-1] in result.stderr


def test_two_services_sharing_a_host_with_different_injection_are_ambiguous(project):
    shared = {
        "hosts": DEPLOY_HOSTS,
        "secret": "main/DEPLOY_TOKEN",
    }
    result = project.config(
        spec(
            egress={
                "default": {
                    "services": {
                        "one": {**shared, "inject": {"header": "Authorization"}},
                        "two": {**shared, "inject": {"header": "X-Api-Key"}},
                    }
                }
            }
        )
    )
    assert result.returncode != 0
    assert DEPLOY_HOSTS[0] in result.stderr


# --- extend ------------------------------------------------------------------------------------


def test_extend_unions_allow_and_merges_services(project):
    resolved = project.resolved(
        spec(
            egress={
                "default": {"allow": ["a.example.com"], "services": {"anthropic": "main/ANTHROPIC_API_KEY"}},
                "coder": {"extend": "default", "allow": ["b.example.com"], "services": {"github": "main/GITHUB_TOKEN"}},
            }
        )
    )
    coder = profile(resolved, "coder")
    assert set(coder["allow"]) == {"a.example.com", "b.example.com"}
    assert set(coder["services"]) == {"anthropic", "github"}
    assert set(profile(resolved, "default")["services"]) == {"anthropic"}


def test_a_child_string_overrides_only_the_secret(project):
    resolved = project.resolved(
        spec(
            egress={
                "default": {"services": {"deploy-api": deploy_api(secret="main/DEPLOY_TOKEN")}},
                "staging": {"extend": "default", "services": {"deploy-api": "main/GITHUB_TOKEN"}},
            }
        )
    )
    service = profile(resolved, "staging")["services"]["deploy-api"]
    assert service["secret"] == "main/GITHUB_TOKEN"
    assert service["hosts"] == DEPLOY_HOSTS
    assert service["inject"]["header"] == "Authorization"


def test_a_child_object_replaces_the_inherited_definition(project):
    resolved = project.resolved(
        spec(
            egress={
                "default": {"services": {"deploy-api": deploy_api(secret="main/DEPLOY_TOKEN")}},
                "staging": {
                    "extend": "default",
                    "services": {
                        "deploy-api": {
                            "hosts": ["staging.deploy.example.com"],
                            "inject": {"header": "X-Key"},
                            "secret": "main/DEPLOY_TOKEN",
                        }
                    },
                },
            }
        )
    )
    service = profile(resolved, "staging")["services"]["deploy-api"]
    assert service["hosts"] == ["staging.deploy.example.com"]
    assert service["inject"]["header"] == "X-Key"


def test_a_string_can_bind_a_secret_to_an_inherited_custom_service(project):
    resolved = project.resolved(
        spec(
            egress={
                "base": {"services": {"deploy-api": deploy_api(secret="main/DEPLOY_TOKEN")}},
                "operator": {"extend": "base", "services": {"deploy-api": "main/GITHUB_TOKEN"}},
            }
        )
    )
    assert profile(resolved, "operator")["services"]["deploy-api"]["secret"] == "main/GITHUB_TOKEN"


def test_a_child_cannot_remove_what_it_inherits(project):
    resolved = project.resolved(
        spec(
            egress={
                "default": {"allow": ["a.example.com"]},
                "child": {"extend": "default", "allow": []},
            }
        )
    )
    assert profile(resolved, "child")["allow"] == ["a.example.com"]


def test_extend_cycles_are_rejected(project):
    result = project.config(spec(egress={"a": {"extend": "b"}, "b": {"extend": "a"}}))
    assert result.returncode != 0
    assert "cycle" in result.stderr.lower()


def test_an_unknown_parent_is_rejected(project):
    result = project.config(spec(egress={"child": {"extend": "ghost"}}))
    assert result.returncode != 0
    assert "ghost" in result.stderr


def test_extend_takes_a_single_parent(project):
    result = project.config(spec(egress={"a": {}, "b": {}, "child": {"extend": ["a", "b"]}}))
    assert result.returncode != 0


# --- reachability warnings ---------------------------------------------------------------------


def test_a_profile_that_cannot_reach_the_harness_provider_warns(project):
    result = project.config(spec(agents={"coder": agent()}))
    assert result.returncode == 0, result.stderr
    assert "warning" in result.stderr.lower()
    assert "api.anthropic.com" in result.stderr


def test_a_profile_that_reaches_the_provider_does_not_warn(project):
    result = project.config(spec(egress={"default": anthropic_profile()}, agents={"coder": agent()}))
    assert result.returncode == 0, result.stderr
    assert "warning" not in result.stderr.lower()


# --- host names are compared the way the proxy compares them -----------------------------------------------


def test_host_names_are_case_insensitive_and_a_trailing_dot_is_ignored(project):
    """The proxy lowercases; the file must not warn about a host that the proxy would allow."""
    document = spec(
        egress={"default": {"allow": ["Platform.Claude.COM.", "API.Anthropic.com"], "services": {"anthropic": "main/ANTHROPIC_API_KEY"}}},
        agents={"coder": agent()},
    )
    result = project.config(document)
    assert result.returncode == 0, result.stderr
    assert "cannot reach" not in result.stderr
    allow = result.yaml()["egress"]["default"]["allow"]
    assert "platform.claude.com" in allow and "api.anthropic.com" in allow


def test_a_secret_bound_to_a_service_that_injects_nothing_is_an_error(project):
    document = spec(
        egress={
            "default": {
                "services": {
                    "plain": {"hosts": ["example.com"]},
                }
            },
            "other": {"extend": "default", "services": {"plain": "main/DEPLOY_TOKEN"}},
        },
    )
    result = project.config(document)
    assert result.returncode != 0
    assert "plain" in result.stderr and "inject" in result.stderr and "placeholder" in result.stderr


def test_opencode_with_a_subscription_token_is_warned_about(project):
    """OpenCode sends its Anthropic credential as x-api-key; a bearer token injected for it cannot work."""
    document = spec(
        egress={"default": {"allow": ["models.opencode.ai"], "services": {"anthropic-oauth": "main/ANTHROPIC_API_KEY"}}},
        agents={"coder": agent(harness="opencode")},
    )
    result = project.config(document)
    assert result.returncode == 0, result.stderr
    assert "opencode" in result.stderr and "bearer" in result.stderr


# --- placeholders: a secret the agent types, swapped for the real one by the proxy ------------------------------


def login(**fields):
    """The smallest service with a placeholder: the agent gets $SITE_PASSWORD, the proxy swaps it on app.example.com."""
    return {"hosts": ["app.example.com"], "secret": "main/DEPLOY_TOKEN", "placeholder": "SITE_PASSWORD", **fields}


def test_a_service_can_give_agents_a_placeholder_for_its_secret(project):
    resolved = project.resolved(spec(egress={"default": {"services": {"login": login()}}}))
    service = profile(resolved, "default")["services"]["login"]
    assert service["secret"] == "main/DEPLOY_TOKEN" and service["placeholder"] == "SITE_PASSWORD"
    assert "inject" not in service


def test_a_placeholder_needs_a_secret(project):
    document = {"hosts": ["app.example.com"], "placeholder": "SITE_PASSWORD"}
    result = project.config(spec(egress={"default": {"services": {"login": document}}}))
    assert result.returncode != 0
    assert "login" in result.stderr and "secret" in result.stderr


def test_a_service_injects_or_has_a_placeholder_never_both(project):
    result = project.config(spec(egress={"default": {"services": {"login": login(inject={"header": "X-Key"})}}}))
    assert result.returncode != 0
    assert "inject" in result.stderr and "placeholder" in result.stderr


@pytest.mark.parametrize("name", ["1PASSWORD", "MY-PASSWORD", "my password", "HTTPS_PROXY"])
def test_a_placeholder_is_a_usable_environment_variable_name_that_egzo_does_not_set(project, name):
    result = project.config(spec(egress={"default": {"services": {"login": login(placeholder=name)}}}))
    assert result.returncode != 0
    assert name in result.stderr


def test_two_services_of_a_profile_cannot_share_a_placeholder_variable(project):
    other = login(hosts=["other.example.com"], secret="main/GITHUB_TOKEN")
    result = project.config(spec(egress={"default": {"services": {"login": login(), "other": other}}}))
    assert result.returncode != 0
    assert "SITE_PASSWORD" in result.stderr


def test_an_agent_cannot_also_set_a_placeholder_variable_in_its_env(project):
    document = spec(
        egress={"default": {"services": {"login": login()}}},
        agents={"coder": agent(harness="custom", image="alpine", env={"SITE_PASSWORD": "hunter2"})},
    )
    result = project.config(document)
    assert result.returncode != 0
    assert "SITE_PASSWORD" in result.stderr


def test_a_string_can_bind_another_secret_to_an_inherited_placeholder_service(project):
    resolved = project.resolved(
        spec(
            egress={
                "base": {"services": {"login": login()}},
                "operator": {"extend": "base", "services": {"login": "main/GITHUB_TOKEN"}},
            }
        )
    )
    service = profile(resolved, "operator")["services"]["login"]
    assert service["secret"] == "main/GITHUB_TOKEN" and service["placeholder"] == "SITE_PASSWORD"


# --- rules are by host only -------------------------------------------------------------------------------------


@pytest.mark.parametrize("key", ["path", "paths", "method", "methods"])
def test_a_service_cannot_be_limited_to_a_path_or_a_method(project, key):
    result = project.config(spec(egress={"default": {"services": {"login": login(**{key: ["/login"]})}}}))
    assert result.returncode != 0
    assert key in result.stderr
