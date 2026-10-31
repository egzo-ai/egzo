# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""`hub:` in the project file and the link sidecar: a project dials out to its hub and proves its key. An operator makes a link code, the
project presents it with `egzo project register`, and from then on the link does what the hub asks with egzo's verbs and nothing else. The
hub here is fixtures/fakehub, which is core's real hub side of the protocol.

The story follows one project: not registered, registered, working, dropped, revoked, unlinked."""

import base64
import json
import time

import pytest

from hubkit import eventually
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


def row(project, name):
    return [r for r in json.loads(project.ok("ps --json").stdout) if r["service"] == name][0]


def check(project):
    return project.run("project", "check")


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


def test_a_project_that_is_not_registered_is_a_warning_and_the_link_stops_dialing(linked, fake_hub):
    assert "not registered" in row(linked, "link")["status"]
    result = check(linked)
    assert result.returncode == 0, "a project runs without its hub"
    assert "warn" in result.stdout and "not registered" in result.stdout
    dials = fake_hub.dials()
    time.sleep(3)
    assert fake_hub.dials() == dials, "the hub said no, and asking again changes nothing until a person does something"


def test_a_bad_code_is_refused_and_says_so_and_can_be_followed_by_a_good_one(linked, fake_hub):
    result = linked.run("project", "register", input="egzo-link-wrong\n")
    assert result.returncode != 0 and "did not take this code" in result.stderr
    assert fake_hub.registration(linked.name) is None
    assert "not registered" in row(linked, "link")["status"]


def test_register_with_no_code_at_all_is_refused_before_anything_is_sent(linked, fake_hub):
    dials = fake_hub.dials()
    result = linked.run("project", "register", input="\n")
    assert result.returncode != 0 and "not a link code" in result.stderr
    assert fake_hub.dials() == dials


def test_register_presents_the_code_once_and_the_project_is_approved(linked, fake_hub):
    code = fake_hub.code("alice")
    good = fake_hub.codes()
    said = linked.ok("project register", input=code + "\n").stdout
    assert "owned by alice" in said
    registration = fake_hub.registration(linked.name)
    assert registration["State"] == "approved"
    assert registration["Templates"] == ["web"], "the project says what it holds"
    assert registration["Host"]
    assert fake_hub.codes() == good - 1, "the code is spent"

    assert eventually(lambda: "owned by alice" in row(linked, "link")["status"])
    result = check(linked)
    assert result.returncode == 0 and "owned by alice" in result.stdout


def test_register_again_says_so_and_spends_nothing(linked, fake_hub):
    code = fake_hub.code("bob")
    good = fake_hub.codes()
    result = linked.run("project", "register", input=code + "\n")
    assert result.returncode != 0 and "registered already" in result.stderr
    assert fake_hub.codes() == good
    assert fake_hub.registration(linked.name)["Owner"] == "alice"


def test_the_code_is_in_no_setting_no_log_and_no_status(linked, fake_hub, engine):
    link = link_of(engine, linked)
    assert not [e for e in link.raw["Config"]["Env"] if "egzo-link-" in e]
    assert "egzo-link-" not in " ".join(link.raw["Config"]["Cmd"] or [])
    assert "egzo-link-" not in engine.run("logs", link.name).stdout + engine.run("logs", link.name).stderr
    assert "egzo-link-" not in linked.ok("ps --json").stdout


def test_the_hub_asks_and_the_link_does_every_verb(linked, fake_hub, engine):
    p, hub = linked.name, fake_hub
    assert hub.must(p, "published")["Templates"].keys() == {"web"}
    answer = hub.verb(p, "spawn", Template="web", Name="fromhub", Actor="user:7")
    assert answer["result"] == "fromhub" and answer["steps"], "progress comes as `egzo spawn` prints it"
    assert any(r["name"] == "fromhub" and r["actor"] == "user:7" for r in json.loads(linked.ok("ps --json").stdout))

    project = hub.must(p, "project")
    assert project["Name"] == p and [a["Name"] for a in project["Agents"]] == ["fromhub"]

    hub.must(p, "stop", Agent="fromhub")
    assert [a["State"] for a in hub.must(p, "project")["Agents"]] != ["running"]
    hub.must(p, "start", Agent="fromhub")
    assert [a["State"] for a in hub.must(p, "project")["Agents"]] == ["running"]

    assert "web" in hub.must(p, "spec")
    assert isinstance(hub.must(p, "logs", Container="control", Tail=5), str)
    assert isinstance(hub.must(p, "events", Limit=5), (list, type(None)))


def test_a_terminal_goes_through_the_link_both_ways(linked, fake_hub):
    output = fake_hub.must(linked.name, "attach", Agent="fromhub", Input="through the link\n", Seconds=3)
    assert "through the link" in output


def test_the_feeds_are_pushed(linked, fake_hub, engine):
    p, hub = linked.name, fake_hub
    assert eventually(lambda: hub.feed(p, "project")), "the project, at once"
    before = len(hub.feed(p, "project"))
    hub.must(p, "stop", Agent="fromhub")
    assert eventually(lambda: len(hub.feed(p, "project")) > before), "and each time it changes"
    hub.must(p, "start", Agent="fromhub")
    assert eventually(lambda: hub.feed(p, "status"))

    container = engine.instance(p, "fromhub").name
    engine.exec(container, "curl", "-sS", "-m", "5", "https://denied.example.com")
    assert eventually(lambda: [e for e in hub.feed(p, "audit") if e.get("Host", e.get("host")) == "denied.example.com"])


def test_the_hub_can_ask_nothing_else(linked, fake_hub):
    assert "no verb" in fake_hub.verb(linked.name, "exec")["error"]
    assert "no agent" in fake_hub.verb(linked.name, "stop", Agent="nobody")["error"]


def test_an_agent_cannot_reach_the_link(linked, engine):
    link = link_of(engine, linked)
    address = list(link.raw["NetworkSettings"]["Networks"].values())[0]["IPAddress"]
    container = engine.instance(linked.name, "fromhub")
    assert f"{linked.name}_link" not in container.raw["NetworkSettings"]["Networks"]
    assert engine.exec(container.name, "ping", "-c", "1", "-W", "2", address).returncode != 0


def test_register_needs_a_hub_and_a_running_link(project, engine, egzo, fake_hub):
    project.write(spec(agents={"web": agent(harness="custom", image=engine.session_image(egzo.binary), env={"FAKE_TUI": "cat"})}))
    result = project.run("project", "register", input="egzo-link-x\n")
    assert result.returncode != 0 and "names no hub" in result.stderr
    project.write(document(engine, egzo, fake_hub))
    result = project.run("project", "register", input="egzo-link-x\n")
    assert result.returncode != 0 and "project up" in result.stderr
