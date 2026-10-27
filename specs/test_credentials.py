# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""A service's credential: how the proxy gets what it puts into requests. `method: secret` reads a secret from a vault at `up`
(a string is short for it); `method: oauth` captures the tokens of a sign-in and renews them in the proxy. What the file says; what the
proxy does with an OAuth sign-in is in `proxy/test_proxy_oauth.py`."""

import pytest

from support import profile, spec

DEPLOY = {"hosts": ["api.deploy.example.com"], "inject": {"header": "Authorization", "value": "Bearer {secret}"}}
OAUTH = {"method": "oauth", "token_url": "https://auth.example.com/oauth/token"}


def service_of(project, services, name):
    return profile(project.resolved(spec(egress={"default": {"services": services}})), "default")["services"][name]


# --- method: secret ---------------------------------------------------------------------------------------------


def test_a_string_is_short_for_a_secret_credential(project):
    short = service_of(project, {"deploy-api": {**DEPLOY, "credential": "main/DEPLOY_TOKEN"}}, "deploy-api")
    long = service_of(project, {"deploy-api": {**DEPLOY, "credential": {"method": "secret", "secret": "main/DEPLOY_TOKEN"}}}, "deploy-api")
    assert short["credential"] == long["credential"] == {"method": "secret", "secret": "main/DEPLOY_TOKEN"}


def test_a_service_has_no_secret_key_of_its_own_any_more(project):
    """No backward compatibility: the secret is the service's credential."""
    result = project.config(spec(egress={"default": {"services": {"deploy-api": {**DEPLOY, "secret": "main/DEPLOY_TOKEN"}}}}))
    assert result.returncode != 0
    assert "secret" in result.stderr and "credential" in result.stderr


def test_an_object_without_hosts_binds_a_built_in_service_like_a_string(project):
    bound = service_of(project, {"anthropic": {"credential": "main/ANTHROPIC_API_KEY"}}, "anthropic")
    short = service_of(project, {"anthropic": "main/ANTHROPIC_API_KEY"}, "anthropic")
    assert bound == short
    assert bound["hosts"] == ["api.anthropic.com"]


def test_an_object_without_hosts_for_a_service_that_does_not_exist_is_refused(project):
    result = project.config(spec(egress={"default": {"services": {"ghost": {"credential": "main/DEPLOY_TOKEN"}}}}))
    assert result.returncode != 0
    assert "ghost" in result.stderr


def test_a_binding_can_only_name_the_credential(project):
    """Without hosts the object binds; changing what the service is takes a full definition."""
    result = project.config(spec(egress={"default": {"services": {"anthropic": {"credential": "main/ANTHROPIC_API_KEY", "paths": ["/v1/"]}}}}))
    assert result.returncode != 0
    assert "anthropic" in result.stderr and "hosts" in result.stderr


def test_an_unknown_method_is_refused_and_names_the_ones_that_exist(project):
    result = project.config(spec(egress={"default": {"services": {"deploy-api": {**DEPLOY, "credential": {"method": "magic"}}}}}))
    assert result.returncode != 0
    assert '"magic"' in result.stderr and "secret" in result.stderr and "oauth" in result.stderr


def test_a_secret_credential_needs_its_secret(project):
    result = project.config(spec(egress={"default": {"services": {"deploy-api": {**DEPLOY, "credential": {"method": "secret"}}}}}))
    assert result.returncode != 0
    assert "deploy-api" in result.stderr and "secret" in result.stderr


# --- method: oauth ----------------------------------------------------------------------------------------------


def test_an_oauth_service_names_its_token_url_and_injects_no_header_of_its_own(project):
    """The proxy swaps the stand-in bearer token the client sends for the real one: there is no header to configure."""
    service = service_of(project, {"idp": {"hosts": ["api.example.com"], "paths": ["/v1/"], "credential": OAUTH}}, "idp")
    assert service["credential"] == OAUTH
    assert service["paths"] == ["/v1/"]
    assert "inject" not in service and "placeholder" not in service


@pytest.mark.parametrize("extra, word", [
    ({"inject": {"header": "Authorization"}}, "inject"),
    ({"placeholder": "IDP_TOKEN"}, "placeholder"),
])
def test_an_oauth_service_has_no_inject_and_no_placeholder(project, extra, word):
    result = project.config(spec(egress={"default": {"services": {"idp": {"hosts": ["api.example.com"], "credential": OAUTH, **extra}}}}))
    assert result.returncode != 0
    assert "idp" in result.stderr and word in result.stderr


@pytest.mark.parametrize("token_url", [None, "http://auth.example.com/oauth/token", "auth.example.com/oauth/token", "https://*.example.com/token", "https://auth.example.com:8443/token"])
def test_the_token_url_is_an_https_url_on_port_443_of_an_exact_host(project, token_url):
    credential = {"method": "oauth"} if token_url is None else {"method": "oauth", "token_url": token_url}
    result = project.config(spec(egress={"default": {"services": {"idp": {"hosts": ["api.example.com"], "credential": credential}}}}))
    assert result.returncode != 0
    assert "idp" in result.stderr and "token_url" in result.stderr


def test_an_oauth_credential_reads_no_vault(project):
    """The tokens are captured at sign-in and kept by the proxy: nothing is read from a vault, nothing is written to one."""
    result = project.config(spec(egress={"default": {"services": {"idp": {"hosts": ["api.example.com"], "credential": {**OAUTH, "secret": "main/X"}}}}}))
    assert result.returncode != 0
    assert "secret" in result.stderr


def test_chatgpt_is_a_built_in_oauth_service_limited_to_the_api_paths(project):
    """chatgpt.com is also ChatGPT's website: the sign-in's token goes only under /backend-api/, where Codex's calls go."""
    service = service_of(project, {"chatgpt": {"credential": {"method": "oauth"}}}, "chatgpt")
    assert service["hosts"] == ["chatgpt.com"]
    assert service["paths"] == ["/backend-api/"]
    assert service["credential"]["method"] == "oauth"
    assert service["credential"]["token_url"] == "https://auth.openai.com/oauth/token"


def test_chatgpt_cannot_be_bound_to_a_secret(project):
    result = project.config(spec(egress={"default": {"services": {"chatgpt": "main/DEPLOY_TOKEN"}}}))
    assert result.returncode != 0
    assert "chatgpt" in result.stderr and "oauth" in result.stderr


def test_a_secret_service_cannot_be_bound_to_a_sign_in(project):
    result = project.config(spec(egress={"default": {"services": {"anthropic": {"credential": {"method": "oauth"}}}}}))
    assert result.returncode != 0
    assert "anthropic" in result.stderr and "oauth" in result.stderr
