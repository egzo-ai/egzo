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

from hubkit import FakeHub, eventually, hub_host
from support import SECRET_KEY_VARIABLE, agent, spec


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


def register(project, hub, owner="alice"):
    return project.ok("project register", input=hub.code(owner) + "\n")


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


def test_a_dry_run_of_a_linked_project_runs_nothing(project, engine, egzo, fake_hub):
    project.write(document(engine, egzo, fake_hub))
    absent = "egzo-spec-absent:never-built"
    result = project.run("project", "up", "--dry-run", env={"EGZO_IMAGE": absent})
    assert result.returncode == 1 and "would create" in result.stdout, "exit 1: there are changes to make, and it could say which"
    assert "not up to date" in project.run("project", "check", env={"EGZO_IMAGE": absent}).stdout


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
    dropped = set(host["CapDrop"])  # Podman lists the default capabilities it drops instead of ALL
    assert host["ReadonlyRootfs"] and ("ALL" in dropped or {"CAP_CHOWN", "CAP_KILL", "CAP_NET_BIND_SERVICE"} <= dropped)
    assert "no-new-privileges" in host["SecurityOpt"]
    if engine.rootless:  # root in the container is the user who owns the engine, which is who the socket belongs to
        assert link.raw["Config"]["User"].split(":")[0] in ("0", "root")
    else:
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


def test_a_dropped_connection_comes_back_by_itself(linked, fake_hub):
    before = fake_hub.registration(linked.name)["Connections"]
    fake_hub.drop(linked.name)
    assert eventually(lambda: fake_hub.registration(linked.name)["Connections"] > before)
    assert eventually(lambda: fake_hub.verb(linked.name, "project").get("error") is None)


def test_up_again_changes_nothing_and_recreating_the_link_keeps_its_key(linked, fake_hub):
    key = fake_hub.registration(linked.name)["Key"]
    assert "nothing to do" in linked.ok("project up").stdout
    assert "nothing to do" in linked.ok("project up --dry-run").stdout, "a dry run sees the link as it is"
    linked.ok("project up --recreate")
    assert eventually(lambda: fake_hub.registration(linked.name)["Online"])
    assert fake_hub.registration(linked.name)["Key"] == key
    assert eventually(lambda: "owned by alice" in row(linked, "link")["status"])


def test_revoked_ends_what_the_hub_may_do_and_check_fails(linked, fake_hub):
    fake_hub.revoke(linked.name, "the laptop was stolen")
    assert eventually(lambda: "revoked" in row(linked, "link")["status"])
    assert "the laptop was stolen" in row(linked, "link")["status"]
    assert fake_hub.registration(linked.name)["Online"] is False, "nothing of a revoked project stays connected"
    result = check(linked)
    assert result.returncode != 0 and "revoked" in result.stdout


def test_a_revoked_link_stops_dialing_and_a_code_does_not_help_until_an_operator_forgets_it(linked, fake_hub):
    dials = fake_hub.dials()
    time.sleep(3)
    assert fake_hub.dials() == dials
    code = fake_hub.code("alice")
    good = fake_hub.codes()
    result = linked.run("project", "register", input=code + "\n")
    assert result.returncode != 0 and "forget this project" in result.stderr
    assert fake_hub.codes() == good, "a revoked key spends nothing"

    before = fake_hub.registration(linked.name)
    fake_hub.forget(linked.name)
    said = linked.ok("project register", input=code + "\n").stdout
    assert "owned by alice" in said, "the way back for a link that was revoked by mistake"
    after = fake_hub.registration(linked.name)
    assert after["Key"] == before["Key"]
    fake_hub.revoke(linked.name)
    assert eventually(lambda: "revoked" in row(linked, "link")["status"])


def test_removing_hub_from_the_file_unlinks_and_removes_the_link(linked, fake_hub, engine, egzo):
    document_without = spec(agents={"web": agent(harness="custom", image=engine.session_image(egzo.binary), env={"FAKE_TUI": "cat"})})
    linked.ok("agent rm --force fromhub")
    linked.write(document_without)
    said = linked.ok("project up").stdout
    assert "the hub revoked this project" in said, "and it says what happened, not that the hub forgot it"
    assert linked.name not in fake_hub.forgotten(), "a revoked project cannot erase its own record"
    assert fake_hub.registration(linked.name)["State"] == "revoked"
    assert link_of(engine, linked) is None
    assert not engine.volume_exists(f"{linked.name}_link")
    assert engine.run("network", "inspect", f"{linked.name}_link").returncode != 0
    assert [c for c in engine.containers(linked.name) if c.name.endswith("-control-1")]


def test_down_tells_the_hub(live_project, engine, egzo, fake_hub):
    live_project.write(document(engine, egzo, fake_hub))
    live_project.ok("project up")
    register(live_project, fake_hub)
    said = live_project.ok("project down --force").stdout
    assert "forgotten" in said
    assert live_project.name in fake_hub.forgotten()
    assert not engine.containers(live_project.name)


def test_down_of_a_project_that_was_never_registered_says_so_at_once(live_project, engine, egzo, fake_hub):
    live_project.write(document(engine, egzo, fake_hub))
    live_project.ok("project up")
    began = time.time()
    said = live_project.ok("project down --force").stdout
    assert "was not registered" in said and "not told" not in said
    assert time.time() - began < 60
    assert live_project.name not in fake_hub.forgotten()


def test_register_needs_a_hub_and_a_running_link(project, engine, egzo, fake_hub):
    project.write(spec(agents={"web": agent(harness="custom", image=engine.session_image(egzo.binary), env={"FAKE_TUI": "cat"})}))
    result = project.run("project", "register", input="egzo-link-x\n")
    assert result.returncode != 0 and "names no hub" in result.stderr
    project.write(document(engine, egzo, fake_hub))
    result = project.run("project", "register", input="egzo-link-x\n")
    assert result.returncode != 0 and "project up" in result.stderr


def test_a_hub_that_is_away_is_said_and_the_project_keeps_running(live_project, engine, egzo, tmp_path):
    away = FakeHub(tmp_path / "away", hub_host(engine))
    live_project.write(document(engine, egzo, away))
    live_project.ok("project up")
    register(live_project, away)
    away.stop()
    assert eventually(lambda: "unreachable" in row(live_project, "link")["status"])
    result = check(live_project)
    assert result.returncode == 0 and "unreachable" in result.stdout, "the project runs without the hub"
    live_project.ok("agent spawn web still")
    assert engine.instance(live_project.name, "still")


def test_a_key_sealed_with_another_secret_key_is_refused_and_says_so(live_project, engine, egzo, fake_hub):
    live_project.write(document(engine, egzo, fake_hub))
    live_project.ok("project up")
    register(live_project, fake_hub)
    other = "b" * 64
    result = live_project.run("project", "up", "--recreate", env={SECRET_KEY_VARIABLE: other})
    assert result.returncode != 0 and "another secret key" in result.stderr


def test_rotate_ca_moves_the_links_key_to_the_new_secret_key(live_project, engine, egzo, fake_hub):
    live_project.write(document(engine, egzo, fake_hub))
    live_project.ok("project up")
    register(live_project, fake_hub)
    key = fake_hub.registration(live_project.name)["Key"]
    other = "b" * 64
    live_project.ok("project rotate-ca", env={SECRET_KEY_VARIABLE: other}, timeout=300)
    live_project.ok("project up", env={SECRET_KEY_VARIABLE: other})
    link = f"{live_project.name}-link-1"
    assert engine.run("restart", link).returncode == 0
    assert eventually(lambda: "locked" in row(live_project, "link")["status"])
    live_project.ok("project up", env={SECRET_KEY_VARIABLE: other})
    assert eventually(lambda: fake_hub.registration(live_project.name)["Online"])
    assert fake_hub.registration(live_project.name)["Key"] == key


def test_after_a_reboot_up_brings_the_project_back_with_the_same_key_and_nobody_registers_again(live_project, engine, egzo, fake_hub):
    """`up` is required after a restart. Who runs it is outside egzo."""
    p = live_project
    p.write(document(engine, egzo, fake_hub))
    p.ok("project up")
    register(p, fake_hub)
    p.ok("agent spawn web worker")
    registered = fake_hub.registration(p.name)
    codes = fake_hub.codes()

    assert engine.run("stop", *[c.name for c in engine.containers(p.name)]).returncode == 0  # what a reboot leaves
    assert eventually(lambda: not fake_hub.registration(p.name)["Online"]), "the hub sees it go"

    p.ok("project up")
    assert eventually(lambda: "owned by alice" in row(p, "link")["status"])
    assert fake_hub.registration(p.name)["Key"] == registered["Key"]
    assert fake_hub.registration(p.name)["ID"] == registered["ID"]
    assert fake_hub.codes() == codes, "nobody needed one"

    assert eventually(lambda: fake_hub.verb(p.name, "project").get("error") is None), "the verbs are back"
    fake_hub.must(p.name, "start", Agent="worker")
    container = engine.instance(p.name, "worker").name
    denied = lambda: engine.exec(container, "curl", "-s", "-m", "5", "-o", "/dev/null", "-w", "%{http_connect}", "https://denied.example.com").stdout
    assert eventually(lambda: denied() == "403"), f"the proxy has its policy again and refuses by it, not with a 503: {denied()}"


def test_a_link_that_restarted_alone_is_locked_and_says_what_opens_it(live_project, engine, egzo, fake_hub):
    p = live_project
    p.write(document(engine, egzo, fake_hub))
    p.ok("project up")
    link = f"{p.name}-link-1"
    assert link_of(engine, p).raw["HostConfig"]["RestartPolicy"]["Name"] == "unless-stopped"
    started = engine.inspect(link)["State"]["StartedAt"]
    engine.exec(link, "kill", "-TERM", "1")  # the process ends: the engine starts it again, without the secret key
    assert eventually(lambda: engine.inspect(link)["State"]["StartedAt"] != started and engine.inspect(link)["State"]["Running"])
    assert eventually(lambda: "locked" in row(p, "link")["status"])
    assert "egzo project up" in row(p, "link")["status"]
    assert "locked" in check(p).stdout
    result = p.run("project", "register", input=fake_hub.code() + "\n")
    assert result.returncode != 0 and "locked" in result.stderr, "the link holds no key to present a code with"
    p.ok("project up")
    assert eventually(lambda: "not registered" in row(p, "link")["status"])
