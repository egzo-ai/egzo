# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""The servers the specs bring along so that they reach nothing on the internet. A fixture that does not do what the specs
rely on makes every spec built on it lie, so each one is checked here, from a client that is not egzo."""

import pytest


@pytest.fixture
def ssh_client(ssh_server, engine):
    """Run `ssh` against the server from another container of the same image (it carries the client), with the given key."""

    def run(command, key=None, user=None, extra=()):
        key = ssh_server.private_key if key is None else key
        script = (
            "umask 077; printf '%s\\n' \"$KEY\" > /tmp/key; "
            "ssh -i /tmp/key -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes "
            "-o UserKnownHostsFile=/tmp/known -o ConnectTimeout=10 "
            f"-p {ssh_server.port} "
            + " ".join(extra)
            + f" -l {user or ssh_server.user} {ssh_server.host} {command}"
        )
        script = "printf '%s\\n' \"$HOSTKEY\" > /tmp/known; " + script
        return engine.run(
            "run", "--rm", "-e", f"KEY={key.strip()}", "-e", f"HOSTKEY=[{ssh_server.host}]:{ssh_server.port} {ssh_server.host_key}",
            "--entrypoint", "sh", ssh_server.image, "-c", script,
        )

    return run


def test_the_ssh_server_accepts_its_key_and_runs_commands_as_an_unprivileged_user(ssh_client):
    result = ssh_client("id -un")
    assert result.returncode == 0, result.stderr
    assert result.stdout.strip() == "ops"


def test_the_ssh_server_host_key_is_the_one_it_reports(ssh_server, ssh_client):
    """Pinning a host key only means something if the fixture says which key the server really has."""
    assert ssh_server.fingerprint.startswith("SHA256:")
    scanned = ssh_server.sh("ssh-keyscan -t ed25519 127.0.0.1 2>/dev/null > /tmp/scan; ssh-keygen -lf /tmp/scan")
    assert scanned.stdout.split()[1] == ssh_server.fingerprint
    assert ssh_client("true", extra=["-o", "HostKeyAlgorithms=ssh-ed25519"]).returncode == 0


def test_the_ssh_server_refuses_a_key_it_does_not_know(ssh_server, ssh_client):
    private, _public, _fingerprint = ssh_server.keygen()
    refused = ssh_client("true", key=private)
    assert refused.returncode != 0
    assert "Permission denied" in refused.stderr


def test_the_ssh_server_refuses_root_and_passwords(ssh_server, ssh_client):
    assert "Permission denied" in ssh_client("true", user="root").stderr
    assert ssh_server.sh("grep -E '^PasswordAuthentication no' /etc/ssh/sshd_config").returncode == 0
