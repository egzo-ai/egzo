# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""`hub:` in the project file and the link sidecar: a project dials out to its hub and proves its key. An operator makes a link code, the
project presents it with `egzo project register`, and from then on the link does what the hub asks with egzo's verbs and nothing else. The
hub here is fixtures/fakehub, which is core's real hub side of the protocol.

The story follows one project: not registered, registered, working, dropped, revoked, unlinked."""

import base64

import pytest

from support import agent, spec


def document(engine, egzo, hub):
    image = engine.session_image(egzo.binary)
    return spec(hub={"url": hub.url, "ca": str(hub.ca)}, agents={"web": agent(harness="custom", image=image, env={"FAKE_TUI": "cat"})})


@pytest.fixture(scope="module")
def linked(shared_project, engine, egzo, fake_hub):
    shared_project.write(document(engine, egzo, fake_hub))
    shared_project.up_output = shared_project.ok("project up").stdout
    return shared_project


def link_of(engine, project):
    found = [c for c in engine.containers(project.name) if c.name == f"{project.name}-link-1"]
    return found[0] if found else None


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


def test_up_starts_the_link_says_it_is_not_registered_and_does_not_wait_for_anyone(linked, fake_hub, engine):
    assert "link: not registered: run `egzo project register`" in linked.up_output
    assert fake_hub.registration(linked.name) is None, "a key the hub does not know leaves nothing on it"

    linked.ok("agent spawn web early")  # the project runs without the hub
    linked.ok("agent rm --force early")


def test_the_link_has_a_network_of_its_own_the_socket_and_a_sidecars_limits_and_nothing_listens(linked, engine):
    link = link_of(engine, linked)
    assert link is not None
    host = link.raw["HostConfig"]
    assert list(link.raw["NetworkSettings"]["Networks"]) == [f"{linked.name}_link"]
    assert not link.raw["NetworkSettings"]["Ports"]
    assert host["ReadonlyRootfs"] and host["CapDrop"] == ["ALL"] and "no-new-privileges" in host["SecurityOpt"]
    assert link.raw["Config"]["User"].split(":")[0] == "65532", "not root: only the socket's group"
    assert any(m["Destination"] == "/run/engine.sock" for m in link.raw["Mounts"])
    assert any(m.get("Name") == f"{linked.name}_link" for m in link.raw["Mounts"])


def test_the_secret_key_is_never_in_the_links_settings_and_the_key_on_the_volume_is_sealed(linked, engine):
    link = link_of(engine, linked)
    assert not [e for e in link.raw["Config"]["Env"] if "SECRET_KEY" in e]
    sealed = engine.in_volumes({f"{linked.name}_link": "/l"}, "base64 -w0 /l/link.key")
    assert sealed.returncode == 0
    assert len(base64.b64decode(sealed.stdout)) > 32, "more than a bare Ed25519 seed: a sealed record"
    assert sealed.stdout.strip() != "", "the key is on the volume"
