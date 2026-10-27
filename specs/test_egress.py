# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""Egress profiles in the project file: deny by default, services, extend, built-ins, validation. What the proxy does with them is in `proxy/`."""

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


@pytest.mark.parametrize("host", ["a..example.com", ".example.com", "x" * 64 + ".example.com", ("a" * 63 + ".") * 4 + "com", "exa mple.com", "ex/ample.com"])
def test_an_allowed_host_is_a_host_name_or_address(project, host):
    result = project.config(spec(egress={"default": {"allow": [host]}}))
    assert result.returncode != 0
    assert "allow" in result.stderr or "host" in result.stderr


# --- services ----------------------------------------------------------------------------------


def test_the_anthropic_oauth_service_is_built_in(project):
    """A Claude subscription token (`claude setup-token`) is not an API key: api.anthropic.com takes it as a bearer token."""
    profile_ = {"allow": ["platform.claude.com"], "services": {"anthropic-oauth": "main/ANTHROPIC_API_KEY"}}
    service = profile(project.resolved(spec(egress={"default": profile_})), "default")["services"]["anthropic-oauth"]
    assert service["hosts"] == ["api.anthropic.com"]
    assert service["inject"] == {"header": "Authorization", "value": "Bearer {secret}"}


def test_the_opencode_service_is_built_in(project):
    """The key of an OpenCode Go or Console workspace (a service account's) is a bearer token for opencode.ai, which is also the
    website: it goes only to the paths OpenCode sends it to, the model calls and the workspace's config and web search."""
    profile_ = {"allow": ["models.opencode.ai"], "services": {"opencode": "main/ANTHROPIC_API_KEY"}}
    service = profile(project.resolved(spec(egress={"default": profile_})), "default")["services"]["opencode"]
    assert service["hosts"] == ["opencode.ai"]
    assert service["paths"] == ["/inference/", "/zen/", "/console/api/v2/config", "/console/api/websearch"]
    assert service["inject"] == {"header": "Authorization", "value": "Bearer {secret}"}
    assert service["credential"]["secret"] == "main/ANTHROPIC_API_KEY"


def test_the_anthropic_service_is_built_in(project):
    service = profile(project.resolved(spec(egress={"default": anthropic_profile()})), "default")["services"]["anthropic"]
    assert service["hosts"] == ["api.anthropic.com"]
    assert service["inject"]["header"] == "x-api-key"
    assert service["credential"]["secret"] == "main/ANTHROPIC_API_KEY"


def test_the_openai_service_is_built_in(project):
    """An OpenAI API key is a bearer token for api.openai.com: what Codex and OpenCode use with an API key."""
    resolved = project.resolved(spec(egress={"default": {"services": {"openai": "main/DEPLOY_TOKEN"}}}))
    service = profile(resolved, "default")["services"]["openai"]
    assert service["hosts"] == ["api.openai.com"]
    assert service["inject"] == {"header": "Authorization", "value": "Bearer {secret}"}


@pytest.mark.parametrize("services, warns", [({}, True), ({"openai": "main/DEPLOY_TOKEN"}, False), ({"chatgpt": {"credential": {"method": "oauth"}}}, False)])
def test_codex_needs_chatgpt_or_the_openai_api(project, services, warns):
    result = project.config(spec(egress={"default": {"services": services}}, agents={"coder": agent(harness="codex")}))
    assert result.returncode == 0, result.stderr
    assert ("cannot reach chatgpt.com or api.openai.com" in result.stderr) == warns, result.stderr


def test_the_github_service_is_built_in(project):
    resolved = project.resolved(spec(egress={"default": {"services": {"github": "main/GITHUB_TOKEN"}}}))
    service = profile(resolved, "default")["services"]["github"]
    assert set(service["hosts"]) == {"github.com", "api.github.com"}
    assert service["credential"]["secret"] == "main/GITHUB_TOKEN"


def test_the_github_service_sends_the_token_as_basic_authentication(project):
    """git over HTTPS refuses `Authorization: Bearer`; GitHub takes the token as the password of a Basic credential."""
    resolved = project.resolved(spec(egress={"default": {"services": {"github": "main/GITHUB_TOKEN"}}}))
    service = profile(resolved, "default")["services"]["github"]
    assert service["inject"] == {"header": "Authorization", "basic_user": "x-access-token"}


def test_a_custom_service_can_send_its_secret_as_basic_authentication(project):
    """The built-in github service is nothing but this: a custom service can say the same."""
    basic = {"header": "Authorization", "basic_user": "x-access-token"}
    resolved = project.resolved(
        spec(egress={"default": {"services": {"git-host": deploy_api(inject=basic, credential="main/DEPLOY_TOKEN")}}})
    )
    assert profile(resolved, "default")["services"]["git-host"]["inject"] == basic


def test_basic_authentication_and_a_value_template_are_exclusive(project):
    inject = {"header": "Authorization", "value": "Bearer {secret}", "basic_user": "x-access-token"}
    result = project.config(spec(egress={"default": {"services": {"git-host": deploy_api(inject=inject, credential="main/DEPLOY_TOKEN")}}}))
    assert result.returncode != 0
    assert "basic_user" in result.stderr and "value" in result.stderr


# --- a service can be limited to the paths of its API ---------------------------------------------------------


def test_a_service_can_be_limited_to_path_prefixes(project):
    """A host that is also a website or a console gets the secret only on the paths of its API."""
    resolved = project.resolved(
        spec(egress={"default": {"services": {"deploy-api": deploy_api(credential="main/DEPLOY_TOKEN", paths=["/api/v1/", "/upload"])}}})
    )
    assert profile(resolved, "default")["services"]["deploy-api"]["paths"] == ["/api/v1/", "/upload"]


def test_a_service_without_paths_injects_on_every_path(project):
    resolved = project.resolved(spec(egress={"default": {"services": {"deploy-api": deploy_api(credential="main/DEPLOY_TOKEN")}}}))
    assert "paths" not in profile(resolved, "default")["services"]["deploy-api"]


@pytest.mark.parametrize("prefix", ["api/v1/", "", "/api/../admin/", "/api/./v1", "/api?x=1", "/api#top", "/api/%2e%2e/", "/api\\v1", "/api v1", "https://example.com/api/"])
def test_a_path_prefix_is_a_plain_absolute_path(project, prefix):
    result = project.config(spec(egress={"default": {"services": {"deploy-api": deploy_api(credential="main/DEPLOY_TOKEN", paths=[prefix])}}}))
    assert result.returncode != 0
    assert "deploy-api" in result.stderr and "path" in result.stderr


def test_paths_cannot_be_empty(project):
    """No paths at all would inject nowhere: leave the key out to inject on every path."""
    result = project.config(spec(egress={"default": {"services": {"deploy-api": deploy_api(credential="main/DEPLOY_TOKEN", paths=[])}}}))
    assert result.returncode != 0
    assert "paths" in result.stderr


@pytest.mark.parametrize("service", [
    {"hosts": ["app.example.com"], "paths": ["/api/"]},
    {"hosts": ["app.example.com"], "credential": "main/DEPLOY_TOKEN", "placeholder": "APP_PASSWORD", "paths": ["/login"]},
], ids=["no-secret", "placeholder"])
def test_paths_limit_an_injected_header_and_nothing_else(project, service):
    """A placeholder goes where the agent types it, and a service with nothing to inject has nothing to limit."""
    result = project.config(spec(egress={"default": {"services": {"app": service}}}))
    assert result.returncode != 0
    assert "paths" in result.stderr and "inject" in result.stderr


@pytest.mark.parametrize("paths", [["/admin/"], None], ids=["other-paths", "every-path"])
def test_two_services_cannot_put_one_credential_on_one_host_with_different_paths(project, paths):
    """Only one service decides where a host's credential goes: its prefixes belong in that one service."""
    other = deploy_api(secret="main/DEPLOY_TOKEN", **({"paths": paths} if paths else {}))
    services = {"deploy-api": deploy_api(secret="main/DEPLOY_TOKEN", paths=["/api/v1/"]), "deploy-admin": other}
    result = project.config(spec(egress={"default": {"services": services}}))
    assert result.returncode != 0
    assert "deploy-api" in result.stderr and "deploy-admin" in result.stderr and "paths" in result.stderr


def test_an_object_defines_a_custom_service(project):
    resolved = project.resolved(
        spec(egress={"default": {"services": {"deploy-api": deploy_api(credential="main/DEPLOY_TOKEN")}}})
    )
    service = profile(resolved, "default")["services"]["deploy-api"]
    assert service["hosts"] == DEPLOY_HOSTS
    assert service["credential"]["secret"] == "main/DEPLOY_TOKEN"


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
    assert "credential" not in service


def test_a_service_without_a_secret_may_cover_hosts_with_wildcards(project):
    """Service hosts take the patterns `allow` takes. Names are folded like any host, wildcards included."""
    resolved = project.resolved(
        spec(
            egress={
                "default": {
                    "services": {
                        "docs": {"hosts": ["*.Example.COM.", "docs.example.org"], "inspect": True},
                        "everything": {"hosts": ["*"]},
                    }
                }
            }
        )
    )
    services = profile(resolved, "default")["services"]
    assert services["docs"]["hosts"] == ["*.example.com", "docs.example.org"] and services["docs"]["inspect"] is True
    assert services["everything"]["hosts"] == ["*"]


@pytest.mark.parametrize("hosts", [["*.example.com"], ["api.example.com", "*"]])
@pytest.mark.parametrize("secret", [{"inject": DEPLOY_INJECT}, {"placeholder": "SITE_PASSWORD"}])
def test_a_secret_binds_to_exact_hosts_never_to_a_wildcard(project, hosts, secret):
    """`*.example.com` would hand the secret to every subdomain, including ones somebody else controls."""
    service = {"hosts": hosts, "credential": "main/DEPLOY_TOKEN", **secret}
    result = project.config(spec(egress={"default": {"services": {"deploy-api": service}}}))
    assert result.returncode != 0
    wildcard = next(host for host in hosts if "*" in host)
    assert "deploy-api" in result.stderr and wildcard in result.stderr and "exact" in result.stderr


def test_a_secret_cannot_be_bound_to_an_inherited_wildcard_service(project):
    browse = {"browse": {"hosts": ["*"], "inspect": True}}
    project.resolved(spec(egress={"default": {"services": browse}, "other": {"extend": "default"}}))
    document = spec(egress={"default": {"services": browse}, "other": {"extend": "default", "services": {"browse": "main/DEPLOY_TOKEN"}}})
    result = project.config(document)
    assert result.returncode != 0
    assert "browse" in result.stderr and "inject" in result.stderr


@pytest.mark.parametrize("key", ["insecure", "skip_verify", "verify", "tls", "ech", "ca"])
def test_a_service_has_no_setting_that_weakens_tls(project, key):
    """egzo has no switch for accepting a certificate it cannot verify, or a hello it cannot read. An unknown key is
    an error rather than ignored, so a file asking for one cannot look as if it worked."""
    result = project.config(spec(egress={"default": {"services": {"docs": {"hosts": ["example.com"], key: True}}}}))
    assert result.returncode != 0
    assert key in result.stderr


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


@pytest.mark.parametrize("reference", ["main/NOT-A-NAME", "nope/ANTHROPIC_API_KEY", "not-a-reference"])
def test_a_secret_reference_names_a_vault_and_a_name_its_backend_can_read(project, reference):
    result = project.config(spec(egress={"default": {"services": {"anthropic": reference}}}))
    assert result.returncode != 0
    assert reference.split("/")[-1] in result.stderr


def test_two_services_sharing_a_host_with_different_injection_are_ambiguous(project):
    shared = {
        "hosts": DEPLOY_HOSTS,
        "credential": "main/DEPLOY_TOKEN",
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
                "default": {"services": {"deploy-api": deploy_api(credential="main/DEPLOY_TOKEN")}},
                "staging": {"extend": "default", "services": {"deploy-api": "main/GITHUB_TOKEN"}},
            }
        )
    )
    service = profile(resolved, "staging")["services"]["deploy-api"]
    assert service["credential"]["secret"] == "main/GITHUB_TOKEN"
    assert service["hosts"] == DEPLOY_HOSTS
    assert service["inject"]["header"] == "Authorization"


def test_a_child_object_replaces_the_inherited_definition(project):
    resolved = project.resolved(
        spec(
            egress={
                "default": {"services": {"deploy-api": deploy_api(credential="main/DEPLOY_TOKEN")}},
                "staging": {
                    "extend": "default",
                    "services": {
                        "deploy-api": {
                            "hosts": ["staging.deploy.example.com"],
                            "inject": {"header": "X-Key"},
                            "credential": "main/DEPLOY_TOKEN",
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
                "base": {"services": {"deploy-api": deploy_api(credential="main/DEPLOY_TOKEN")}},
                "operator": {"extend": "base", "services": {"deploy-api": "main/GITHUB_TOKEN"}},
            }
        )
    )
    assert profile(resolved, "operator")["services"]["deploy-api"]["credential"]["secret"] == "main/GITHUB_TOKEN"


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


def test_a_wildcard_service_is_an_allow_and_counts_for_reaching_the_provider(project):
    """A service that covers every host reaches the model API too, so there is nothing to warn about."""
    result = project.config(spec(vaults={}, egress={"default": {"services": {"web": {"hosts": ["*"]}}}}, agents={"coder": agent()}))
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
    return {"hosts": ["app.example.com"], "credential": "main/DEPLOY_TOKEN", "placeholder": "SITE_PASSWORD", **fields}


def test_a_service_can_give_agents_a_placeholder_for_its_secret(project):
    resolved = project.resolved(spec(egress={"default": {"services": {"login": login()}}}))
    service = profile(resolved, "default")["services"]["login"]
    assert service["credential"]["secret"] == "main/DEPLOY_TOKEN" and service["placeholder"] == "SITE_PASSWORD"
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
    other = login(hosts=["other.example.com"], credential="main/GITHUB_TOKEN")
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
    assert service["credential"]["secret"] == "main/GITHUB_TOKEN" and service["placeholder"] == "SITE_PASSWORD"


# --- no rule by method ------------------------------------------------------------------------------------------


@pytest.mark.parametrize("key", ["path", "method", "methods"])
def test_a_service_cannot_be_limited_to_a_method(project, key):
    """Only `paths`, and only for an injected header (above)."""
    result = project.config(spec(egress={"default": {"services": {"login": login(**{key: ["/login"]})}}}))
    assert result.returncode != 0
    assert key in result.stderr
