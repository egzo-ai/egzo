# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""`hub:` in the project file and the link sidecar: a project dials out to its hub and proves its key. An operator makes a link code, the
project presents it with `egzo project register`, and from then on the link does what the hub asks with egzo's verbs and nothing else. The
hub here is fixtures/fakehub, which is core's real hub side of the protocol.

The story follows one project: not registered, registered, working, dropped, revoked, unlinked."""

import pytest

from support import spec


def test_the_hub_is_an_address_or_an_address_with_a_ca(project, tmp_path):
    project.write(spec(hub="https://hub.example.org/"))
    assert project.ok("project check --resolved").yaml()["hub"] == {"url": "https://hub.example.org"}
    project.write(spec(hub="http://hub.example.org"))
    assert "plain http" in project.ok("project check").stdout


@pytest.mark.parametrize("wrong", ["hub.example.org", "ftp://hub.example.org", "https://", "https://u:p@hub.example.org", "https://hub.example.org/path"])
def test_a_hub_that_is_not_an_address_is_refused(project, wrong):
    project.write(spec(hub=wrong))
    result = project.run("project", "check")
    assert result.returncode != 0 and "hub" in result.stdout


def test_a_ca_that_is_not_certificates_is_refused(project):
    (project.root / "ca.pem").write_text("not a certificate")
    project.write(spec(hub={"url": "https://hub.example.org", "ca": "ca.pem"}))
    result = project.run("project", "check")
    assert result.returncode != 0 and "hub.ca" in result.stdout
