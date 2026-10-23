# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""What an agent may reach through the proxy: the profile of its template (deny unless allowed), what the audit trail records about it,
`inspect` and wildcard services, and how instances are bound to their profile (and unbound). These specs need the internet:
example.com, example.org and httpbin.org are fetched for real, and fail when they cannot be reached. One project is up for the whole
module; the profiles are in proxykit.py and a spec spawns the one it is about."""

import json

import pytest

from proxykit import SECRET, Probe, document

pytestmark = pytest.mark.usefixtures("internet")


@pytest.fixture(scope="module")
def sandbox(shared_project, engine):
    shared_project.env.update({"DEPLOY_TOKEN": SECRET, "SITE_SECRET": "unused"})
    shared_project.up(document(engine.agent_image(), "allow", "open", "none", "inspect", "wild", "wildsub"))
    return shared_project


@pytest.fixture(scope="module")
def probe(sandbox, engine):
    return Probe(engine, sandbox)


@pytest.fixture(autouse=True)
def start_from_no_instance(sandbox, engine):
    sandbox.retire()
    sandbox.write(document(engine.agent_image(), "allow", "open", "none", "inspect", "wild", "wildsub"))
    yield


def test_each_instance_gets_the_profile_of_its_template_and_every_decision_is_in_the_audit_trail(sandbox, probe):
    none, allow, open_ = sandbox.spawn("none"), sandbox.spawn("allow"), sandbox.spawn("open")  # spawned one after the other, long after the policy was loaded
    assert probe.status(allow, "example.com").stdout == "200"
    refused = probe.status(allow, "example.org")
    assert refused.returncode != 0 and "403" in refused.stderr, "a host outside the profile is refused"
    reached = probe.status(none, "example.com")
    assert reached.returncode != 0 and "403" in reached.stderr, "a profile that allows nothing"
    assert probe.status(open_, "example.org").stdout == "200", "allow * grants the whole internet through the proxy"
    assert probe.container(allow).labels["ai.egzo.service"] == "allow"

    events = probe.audit(agent=allow)
    allowed = next(e for e in events if e["host"] == "example.com" and e["event"] == "connect")
    assert allowed["decision"] == "allow" and allowed["mode"] == "tunnel" and allowed["protocol"] == "https"
    assert allowed["agent"] == allow and allowed["profile"] == "allow"
    assert allowed["instance"] == probe.container(allow).labels["ai.egzo.instance-id"], "the name for people, the id for history"
    denied = next(e for e in events if e["host"] == "example.org")
    assert denied["event"] == "connect" and denied["decision"] == "deny" and denied["profile"] == "allow"
    assert denied["reason"]["code"] == "not-in-profile" and denied["reason"]["text"], "a code for tools, a text for people"
    (closed,) = [e for e in events if e["conn"] == allowed["conn"] and e["event"] == "close"]
    assert closed["bytes_up"] > 0 and closed["bytes_down"] > 0 and "duration_ms" in closed and "decision" not in closed
    assert denied["conn"] != allowed["conn"], "each connection has an id of its own"
    assert not [e for e in events if e["event"] == "request"], "an allowed host is a tunnel and no path is logged"


def test_inspect_logs_the_method_path_and_status_and_a_wildcard_service_covers_what_its_pattern_says_and_an_exact_one_still_injects(sandbox, probe):
    inspect, wild, wildsub = sandbox.spawn("inspect"), sandbox.spawn("wild"), sandbox.spawn("wildsub")
    probe.curl(inspect, "-o", "/dev/null", "https://example.com/some/page?q=zq9query")
    (request,) = probe.requests_to("example.com", inspect)
    assert request["method"] == "GET" and request["path"] == "/some/page" and request["status"] >= 200 and request["decision"] == "allow"
    connection = [e["event"] for e in probe.audit(agent=inspect) if e["conn"] == request["conn"]]
    assert connection == ["connect", "request", "close"], "a terminated host's requests belong to its connection, which is closed like a tunnel"
    assert next(e for e in probe.audit(agent=inspect) if e["conn"] == request["conn"])["mode"] == "inspect"
    assert "zq9query" not in json.dumps(probe.audit())

    probe.curl(wild, "-o", "/dev/null", "https://example.com/some/page?q=zq9query")
    echoed = probe.curl(wild, "https://httpbin.org/headers")
    assert json.loads(echoed.stdout)["headers"]["X-Egzo-Secret"] == SECRET, "the exact service still injects where a wildcard one covers the host"
    assert [r["path"] for r in probe.requests_to("example.com", wild)] == ["/some/page"]
    assert [r["path"] for r in probe.requests_to("httpbin.org", wild)] == ["/headers"]
    assert "zq9query" not in json.dumps(probe.audit()) and SECRET not in json.dumps(probe.audit())

    assert probe.status(wildsub, "www.example.com").stdout in ("200", "301", "302", "404")
    assert [r["method"] for r in probe.requests_to("www.example.com", wildsub)] == ["GET"]
    for host in ("example.com", "example.org"):  # the pattern is for the names below it, as in `allow`
        refused = probe.status(wildsub, host)
        assert refused.returncode != 0 and "403" in refused.stderr, host


def test_the_bindings_of_instances_to_their_profile_follow_spawn_rm_and_the_restarts_of_the_proxy(sandbox, probe, engine):
    p = sandbox
    first, second = p.spawn("allow"), p.spawn("allow")
    borrowed = probe.env(first)["HTTPS_PROXY"]
    through = ["--proxy", borrowed, "-o", "/dev/null", "-w", "%{http_code}", "https://example.com/"]
    assert probe.curl(second, *through).stdout == "200", "the proxy authenticates the credential"
    p.ok("agent rm --force", first)
    revoked = probe.curl(second, *through)
    assert revoked.returncode != 0 and "407" in revoked.stderr, "removing an instance makes its credentials stop working"

    # the bindings live in the proxy's memory: stop and start lose them, and `up` gives them back
    engine.run("stop", probe.container("proxy").name)
    engine.run("start", probe.container("proxy").name)
    p.ok("project up")
    assert probe.status(second, "example.com").stdout == "200"

    # a restart through egzo uses the published profiles, not a file edited since, and an instance spawned after it gets its template's profile
    edited = document(engine.agent_image(), "allow", "open", "none", "inspect", "wild", "wildsub")
    edited["egress"]["allow"] = {"allow": ["example.org"]}
    p.write(edited)  # edited, not applied by `up`
    p.ok("restart proxy")
    third = p.spawn("allow")
    assert probe.status(second, "example.com").stdout == "200" and probe.status(third, "example.com").stdout == "200"

    # the CA survives recreating the proxy, and the policy comes back
    def ca():
        reply = engine.exec(probe.container("proxy").name, "/egzo", "proxy", "request", "GET", "/policy")
        return json.loads(reply.stdout)["ca"]

    before, name = ca(), probe.container("proxy").name
    p.write(document(engine.agent_image(), "allow", "open", "none", "inspect", "wild", "wildsub"))
    p.ok("project up --recreate")
    assert probe.container("proxy").name == name and ca() == before
    assert probe.status(second, "example.com").stdout == "200"
