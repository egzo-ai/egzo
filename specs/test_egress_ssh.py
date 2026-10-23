# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""ssh servers in an egress profile (`egress.<profile>.ssh`): the declaration, validation, extend. What the proxy does with them
(the agent's ssh, the key that never reaches it, the pinned host key) is in `proxy/test_proxy_ssh.py`."""

import pytest

from support import spec

FINGERPRINT = "SHA256:giDwc8qRcN2ytSupvU/QCL4YGA5l7fUGresaokyLYt4"
PUBLIC_KEY = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMGFJIY0eOCEYAre+qhPAFyN2YPymAD03FxBn1v6yyZz"


VAULTS = {"main": {"backend": "env", "secrets": ["PROD_SSH_KEY"]}}


def ssh_spec(**sections):
    return spec(vaults=VAULTS, **sections)


def server(**fields):
    return {"host": "prod.example.com", "user": "deploy", "key": "main/PROD_SSH_KEY", "host_key": FINGERPRINT, **fields}


def ssh_of(resolved, profile="default"):
    return resolved["egress"][profile]["ssh"]


def test_a_profile_without_ssh_servers_resolves_to_none(project):
    assert ssh_of(project.resolved(ssh_spec(egress={"default": {}}))) == {}


def test_a_profile_declares_ssh_servers_by_name(project):
    resolved = project.resolved(ssh_spec(egress={"default": {"ssh": {"prod": server()}}}))
    assert ssh_of(resolved)["prod"] == {
        "host": "prod.example.com", "port": 22, "user": "deploy", "key": "main/PROD_SSH_KEY", "host_key": FINGERPRINT,
    }


def test_a_port_and_a_public_key_line_can_pin_the_server(project):
    resolved = project.resolved(ssh_spec(egress={"default": {"ssh": {"prod": server(port=2222, host_key=PUBLIC_KEY)}}}))
    assert ssh_of(resolved)["prod"]["port"] == 2222
    assert ssh_of(resolved)["prod"]["host_key"] == PUBLIC_KEY


def test_an_ssh_server_is_not_a_service_and_opens_no_https_host(project):
    resolved = project.resolved(ssh_spec(egress={"default": {"ssh": {"prod": server()}}}))
    assert resolved["egress"]["default"]["allow"] == []
    assert resolved["egress"]["default"]["services"] == {}


@pytest.mark.parametrize("field", ["host", "user", "key", "host_key"])
def test_every_field_but_the_port_is_required(project, field):
    definition = server()
    del definition[field]
    result = project.config(ssh_spec(egress={"default": {"ssh": {"prod": definition}}}))
    assert result.returncode != 0
    assert "prod" in result.stderr and field in result.stderr


@pytest.mark.parametrize("host", ["*.example.com", "*", "", "prod.example.com:22", "ssh://prod.example.com", "prod example.com", "prod..example.com", "x" * 64 + ".example.com"])
def test_the_host_is_one_exact_name_or_address(project, host):
    result = project.config(ssh_spec(egress={"default": {"ssh": {"prod": server(host=host)}}}))
    assert result.returncode != 0
    assert "host" in result.stderr


@pytest.mark.parametrize("port", [0, 65536, -1, "ssh"])
def test_the_port_is_a_port_number(project, port):
    result = project.config(ssh_spec(egress={"default": {"ssh": {"prod": server(port=port)}}}))
    assert result.returncode != 0
    assert "port" in result.stderr


@pytest.mark.parametrize("user", ["", "root user", "a@b", "-oProxyCommand=x", "x" * 33])
def test_the_remote_user_is_a_plain_account_name(project, user):
    result = project.config(ssh_spec(egress={"default": {"ssh": {"prod": server(user=user)}}}))
    assert result.returncode != 0
    assert "user" in result.stderr


@pytest.mark.parametrize("name", ["Prod", "-prod", "prod server", "pro/d", "p" * 33, "root"])
def test_the_name_of_a_server_is_what_an_agent_types(project, name):
    """It becomes the `Host` alias in the agent, so it is a lowercase label (and `root` would read as the remote user)."""
    result = project.config(ssh_spec(egress={"default": {"ssh": {name: server()}}}))
    assert result.returncode != 0
    assert "ssh" in result.stderr


@pytest.mark.parametrize("key", ["main/MISSING", "nope/PROD_SSH_KEY", "not-a-reference"])
def test_the_key_is_a_secret_reference_that_exists_in_a_vault(project, key):
    result = project.config(ssh_spec(egress={"default": {"ssh": {"prod": server(key=key)}}}))
    assert result.returncode != 0
    assert key.split("/")[-1] in result.stderr


@pytest.mark.parametrize("host_key", ["", "SHA256:short", "MD5:aa:bb", "ssh-ed25519 notbase64!", "ssh-ed25519", FINGERPRINT + "A", "trust-on-first-use"])
def test_the_host_key_is_pinned_as_a_fingerprint_or_a_public_key(project, host_key):
    """There is no trust on first use: the key is given, and a malformed one is an error rather than a server nobody can verify."""
    result = project.config(ssh_spec(egress={"default": {"ssh": {"prod": server(host_key=host_key)}}}))
    assert result.returncode != 0
    assert "host_key" in result.stderr


def test_ssh_servers_have_no_setting_that_weakens_the_checks(project):
    for key, value in (("strict_host_key_checking", False), ("password", "x"), ("forward_agent", True), ("command", "sh")):
        result = project.config(ssh_spec(egress={"default": {"ssh": {"prod": server(**{key: value})}}}))
        assert result.returncode != 0, key
        assert key in result.stderr


def test_ssh_is_a_map_of_servers(project):
    assert project.config(ssh_spec(egress={"default": {"ssh": [server()]}})).returncode != 0
    assert project.config(ssh_spec(egress={"default": {"ssh": {"prod": None}}})).returncode != 0


def test_extend_merges_ssh_servers_by_name_with_the_child_winning(project):
    resolved = project.resolved(
        ssh_spec(
            egress={
                "default": {"ssh": {"prod": server(), "staging": server(host="staging.example.com")}},
                "ops": {"extend": "default", "ssh": {"prod": server(host="prod2.example.com", user="admin"), "db": server(host="db.example.com")}},
            }
        )
    )
    assert set(ssh_of(resolved, "ops")) == {"prod", "staging", "db"}
    assert ssh_of(resolved, "ops")["prod"]["host"] == "prod2.example.com"
    assert ssh_of(resolved, "ops")["prod"]["user"] == "admin"
    assert ssh_of(resolved)["prod"]["host"] == "prod.example.com"


def test_the_resolved_output_never_holds_the_key_itself(project):
    """It names the secret (`main/PROD_SSH_KEY`); the value is read by `up` and goes to the proxy alone."""
    result = project.config(ssh_spec(egress={"default": {"ssh": {"prod": server()}}}))
    assert result.returncode == 0
    assert "PRIVATE KEY" not in result.stdout
