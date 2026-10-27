# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""An OAuth service: the proxy captures the tokens of a sign-in made in an agent, gives the client stand-ins, swaps a stand-in for the
real access token on the service's hosts and paths, answers the client's refreshes itself, renews the real tokens ahead of their expiry
and keeps them sealed in its volume. The agent never holds a token that works without the proxy.

A stub provider and API (fixtures/oauth_stub.py) run on the project's egress network under two names, with a certificate of a CA the
project trusts upstream: what `proxy.upstream` is for. No account and no internet are needed."""

import json
import time

import pytest

from oauthkit import API, IDP, SERVICE, UPSTREAM, payload, start_stub, stop_stub
from support import custom, spec

OTHER_KEY = "another-spec-secret-key-" + "fedcba9876543210" * 3


@pytest.fixture
def oauth_project(live_project, engine, agent_image):
    p = live_project
    start = start_stub(engine, p, agent_image)
    p.write(spec(proxy=UPSTREAM, egress={"default": {"services": {"idp": SERVICE}}}, agents={"coder": custom(agent_image)}))
    p.ok("project up", timeout=300)
    yield p, start()
    stop_stub(engine, p)


def curl(engine, project, name, *args):
    """curl in an instance; the body of the answer, with the status code on the last line."""
    container = engine.instance(project.name, name).name
    result = engine.exec(container, "curl", "-sS", "-m", "20", "-w", "\n%{http_code}", *args)
    assert result.returncode == 0, result.stderr
    body, _, status = result.stdout.rpartition("\n")
    return int(status), body


def sign_in(engine, project, name):
    """What a client does at the end of a device-code sign-in: exchange the code at the token URL."""
    status, body = curl(engine, project, name, "-X", "POST", f"https://{IDP}/oauth/token",
                        "-d", "grant_type=authorization_code", "-d", "code=spec-code", "-d", "client_id=app_spec",
                        "-d", f"redirect_uri=https://{IDP}/deviceauth/callback")
    assert status == 200, body
    return json.loads(body)


def api_saw(engine, project, name, stub, token, path="/backend-api/me"):
    """The Authorization header the API got for a request the agent sent with `token`."""
    status, body = curl(engine, project, name, "-H", f"Authorization: Bearer {token}", f"https://{API}{path}")
    return status, (stub.requests()[-1]["authorization"] if status != 401 else None), body


def wait_until(condition, what, timeout=90):
    deadline = time.time() + timeout
    while time.time() < deadline:
        if condition():
            return
        time.sleep(2)
    pytest.fail(f"timed out waiting for {what}")


def private_volume(engine, project, image):
    proxy = next(c for c in engine.containers(project.name) if c.name == f"{project.name}-proxy-1")
    volume = {m["Destination"]: m["Name"] for m in proxy.raw["Mounts"]}["/ca-private"]
    listed = engine.run("run", "--rm", "-v", f"{volume}:/v:ro", image, "sh", "-c", 'cat /v/*')
    assert listed.returncode == 0, listed.stderr
    return listed.stdout


def test_a_sign_in_in_an_agent_is_captured_and_the_agent_only_ever_holds_stand_ins(oauth_project, engine, agent_image):
    p, stub = oauth_project
    spawned = p.ok("agent spawn coder", timeout=300)
    assert "idp" in spawned.stdout + spawned.stderr and "sign in" in (spawned.stdout + spawned.stderr).lower(), "spawn says the service has no sign-in"
    coder = "coder-1"
    check = p.sh("project check", timeout=120)
    assert any(line.startswith("warn") and "idp" in line and "sign" in line for line in check.stdout.splitlines()), check.stdout

    # The exchange reaches the provider; the client gets stand-ins that carry the real claims
    held = sign_in(engine, p, coder)
    assert len(stub.grants("authorization_code")) == 1
    assert held["access_token"] != held["refresh_token"]
    assert payload(held["access_token"])["sub"] == "user-spec" and payload(held["id_token"])["email"] == "spec@example.com"

    status, seen, _ = api_saw(engine, p, coder, stub, held["access_token"])
    assert status == 200 and seen.startswith("Bearer ") and seen != f"Bearer {held['access_token']}", "the API got the real token"
    real_access = seen.removeprefix("Bearer ")
    stable = ("sub", "https://api.openai.com/auth")  # exp and the like change when the proxy renews, which it may have done already
    assert [payload(real_access)[k] for k in stable] == [payload(held["access_token"])[k] for k in stable], "a stand-in carries the real claims"

    assert api_saw(engine, p, coder, stub, "someone-elses-token")[1] == "Bearer someone-elses-token", "any other token is left alone"
    assert api_saw(engine, p, coder, stub, held["access_token"], "/other")[0] == 404
    assert stub.requests()[-1]["authorization"] == f"Bearer {held['access_token']}", "outside the service's paths a stand-in stays a stand-in"

    upgrade = engine.exec(engine.instance(p.name, coder).name, "curl", "-sS", "-m", "10", "-i", "--http1.1",
                          "-H", "Connection: Upgrade", "-H", "Upgrade: websocket", "-H", "Sec-WebSocket-Version: 13",
                          "-H", "Sec-WebSocket-Key: c3BlYy13ZWJzb2NrZXQta2V5", "-H", f"Authorization: Bearer {held['access_token']}",
                          f"https://{API}/backend-api/ws")
    assert "HTTP/1.1 101" in upgrade.stdout, upgrade.stdout + upgrade.stderr
    assert stub.requests()[-1]["authorization"].startswith("Bearer ") and stub.requests()[-1]["authorization"] != f"Bearer {held['access_token']}"

    # The proxy renews the real chain on its own and saves the rotated refresh token; the stand-ins keep working
    wait_until(lambda: len(stub.grants("refresh_token")) >= 2, "the proxy to renew the chain twice")
    assert not any(held["refresh_token"] in json.dumps(r) or held["access_token"] in json.dumps(r["body"] or {}) for r in stub.grants("refresh_token"))
    status, seen, _ = api_saw(engine, p, coder, stub, held["access_token"])
    assert status == 200 and seen != f"Bearer {real_access}", "the injected token is the renewed one"

    # The client's own refresh is answered by the proxy: new stand-ins, and the stub never sees a stand-in
    before = len(stub.requests())
    status, body = curl(engine, p, coder, "-X", "POST", f"https://{IDP}/oauth/token", "-H", "Content-Type: application/json",
                        "-d", json.dumps({"grant_type": "refresh_token", "refresh_token": held["refresh_token"], "client_id": "app_spec"}))
    assert status == 200, body
    refreshed = json.loads(body)
    assert all(held["refresh_token"] not in json.dumps(r) for r in stub.requests()[before:])
    assert api_saw(engine, p, coder, stub, refreshed["access_token"])[0] == 200

    # Sealed at rest, opened again after a restart; a second agent shares the sign-in
    current = stub.requests()[-1]["authorization"].removeprefix("Bearer ")
    at_rest = private_volume(engine, p, agent_image)
    assert current not in at_rest and held["access_token"] not in at_rest
    p.ok("restart proxy", timeout=300)
    assert api_saw(engine, p, coder, stub, refreshed["access_token"])[0] == 200
    second = p.spawn("coder")
    assert api_saw(engine, p, second, stub, refreshed["access_token"])[0] == 200
    assert "ok" in [line.split()[0] for line in p.sh("project check", timeout=120).stdout.splitlines() if "idp" in line]


def test_a_sign_in_ends_with_a_revoke_a_dead_chain_a_logout_or_another_secret_key(oauth_project, engine, agent_image):
    p, stub = oauth_project
    coder = p.spawn("coder")

    # A client's logout revokes the real token and signs the project out
    held = sign_in(engine, p, coder)
    status, _ = curl(engine, p, coder, "-X", "POST", f"https://{IDP}/oauth/revoke", "-d", f"token={held['refresh_token']}")
    assert status == 200
    (revoked,) = [r for r in stub.requests() if r["path"] == "/oauth/revoke"]
    assert revoked["body"]["token"] != held["refresh_token"], "the provider got the real token"
    status, _, body = api_saw(engine, p, coder, stub, held["access_token"])
    assert status == 401 and "sign in" in body.lower()

    # A chain the provider ended: the proxy's renewal fails, the client's refresh gets invalid_grant, project check says so
    held = sign_in(engine, p, coder)
    stub.end_chain()
    wait_until(lambda: "invalid_grant" in curl(engine, p, coder, "-X", "POST", f"https://{IDP}/oauth/token",
                                               "-d", "grant_type=refresh_token", "-d", f"refresh_token={held['refresh_token']}")[1],
               "the proxy to find the chain dead")
    check = p.sh("project check", timeout=120)
    assert check.returncode == 1 and any(line.startswith("fail") and "idp" in line and "sign in" in line for line in check.stdout.splitlines()), check.stdout
    assert api_saw(engine, p, coder, stub, held["access_token"])[0] == 401

    # A new sign-in is captured; `egzo proxy logout` ends it
    held = sign_in(engine, p, coder)
    assert api_saw(engine, p, coder, stub, held["access_token"])[0] == 200
    p.ok("proxy logout idp", timeout=120)
    assert api_saw(engine, p, coder, stub, held["access_token"])[0] == 401

    # Sealed with another secret key, a sign-in cannot be opened: moving to another key signs the project out
    held = sign_in(engine, p, coder)
    p.ok("project rotate-ca", env={"EGZO_SPEC_SECRET_KEY": OTHER_KEY}, timeout=300)
    p.ok("project up", env={"EGZO_SPEC_SECRET_KEY": OTHER_KEY}, timeout=300)
    assert api_saw(engine, p, coder, stub, held["access_token"])[0] == 401
