# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""Secrets and the proxy: the credential is injected by the proxy and the agent never holds it; a placeholder the agent types is swapped
for the secret on its way out; nothing a secret touches is recorded; and what happens to all of it when the secret is rotated, missing or
refused. The injection and placeholder specs need httpbin.org, which echoes what it receives, and fail when it cannot be reached. One
project is up for the whole module for the first specs; the last, which changes the policy and the secrets, has its own."""

import base64
import json
import re

import pytest

from conftest import require_reachable
from proxykit import SECRET, SITE_SECRET, Probe, document
from support import FakePass, anthropic_profile, custom, spec

pytestmark = pytest.mark.usefixtures("httpbin")
PLACEHOLDER = re.compile(r"egzo-ph-[0-9a-f]{32}")


@pytest.fixture(scope="module")
def sandbox(shared_project, engine):
    shared_project.env.update({"DEPLOY_TOKEN": SECRET, "SITE_SECRET": SITE_SECRET})
    shared_project.up(document(engine.agent_image(), "echo", "paths", "basic", "login", "plain"))
    return shared_project


@pytest.fixture(scope="module")
def probe(sandbox, engine):
    return Probe(engine, sandbox)


@pytest.fixture(autouse=True)
def start_from_no_instance(sandbox):
    sandbox.retire()
    yield


def nothing_of_it_is_recorded(sandbox, engine, probe, secret, name):
    assert secret not in engine.exec(probe.container(name).name, "env").stdout
    for resource in engine.resources(sandbox.name):
        assert secret not in json.dumps(resource.raw), f"{resource.kind} {resource.name}"
    assert secret not in probe.proxy_log()


def test_the_proxy_injects_the_credential_the_agent_never_sees_it_and_what_the_agent_sends_never_overrides_it(sandbox, engine, probe):
    name = sandbox.spawn("echo")
    echoed = probe.curl(name, "https://httpbin.org/headers")
    assert echoed.returncode == 0, echoed.stderr
    assert json.loads(echoed.stdout)["headers"]["X-Egzo-Secret"] == SECRET
    forged = probe.curl(name, "-H", "X-Egzo-Secret: forged", "https://httpbin.org/headers")
    assert json.loads(forged.stdout)["headers"]["X-Egzo-Secret"] == SECRET
    assert SECRET not in json.dumps(probe.container(name).raw)
    nothing_of_it_is_recorded(sandbox, engine, probe, SECRET, name)

    basic = sandbox.spawn("basic")  # what git over HTTPS needs: base64(user:secret) after `Basic`, built by the proxy
    sent = json.loads(probe.curl(basic, "https://httpbin.org/headers").stdout)["headers"]["Authorization"]
    assert sent == "Basic " + base64.b64encode(f"x-access-token:{SECRET}".encode()).decode()
    assert SECRET not in engine.exec(probe.container(basic).name, "env").stdout


def test_a_service_limited_to_paths_injects_only_under_them_and_refuses_a_path_that_could_climb_out(sandbox, engine, probe):
    """httpbin.org/anything/<path> echoes any path: the website and the API on one host, the API under /anything/api/."""
    name = sandbox.spawn("paths")

    def sent(path, *extra):
        echoed = probe.curl(name, "--path-as-is", *extra, f"https://httpbin.org{path}")
        assert echoed.returncode == 0, echoed.stderr
        return json.loads(echoed.stdout)["headers"].get("X-Egzo-Secret")

    assert sent("/anything/api/v1/models") == SECRET
    assert sent("/anything/api/") == SECRET
    assert sent("/anything/console/config") is None, "the host is allowed, the secret stays home"
    assert sent("/anything/apix") is None, "a prefix matches whole segments"
    assert sent("/anything/console", "-H", "X-Egzo-Secret: the-agents-own") == "the-agents-own", "a header the agent sends outside the paths is its own"
    assert sent("/anything/api/v1", "-H", "X-Egzo-Secret: forged") == SECRET
    # what APIs send: an escaped slash in a name (GitLab's group%2Fproject), a space, a colon (Google's :generateContent)
    for legitimate in ("/anything/api/projects/group%2Fproject", "/anything/api/files/my%20file.md", "/anything/api/models/m:generateContent"):
        assert sent(legitimate) == SECRET, legitimate

    climbing = (
        "/anything/api/../console", "/anything/api/%2e%2e/console", "/anything/api/%2E%2E%2Fconsole", "/anything/api/..%5cconsole",
        "/anything/api/..\\console", "/anything/api/..;/console", "/anything/api/..%20/console",
        # what no API sends either: a character that needs no escape escaped, an escape escaped, a byte outside ASCII
        "/anything/%61pi/v1", "/anything/api/%252e%252e/console", "/anything/api/%EF%BC%8E%EF%BC%8E/console", "/anything/api/x%00",
    )
    for path in climbing:
        refused = probe.curl(name, "--path-as-is", "-o", "/dev/null", "-w", "%{http_code}", f"https://httpbin.org{path}")
        assert refused.stdout == "400", f"{path}: {refused.stdout} {refused.stderr}"
    requests = probe.requests_to("httpbin.org", agent=name)
    denied = [e for e in requests if e["decision"] == "deny"]
    assert len(denied) == len(climbing) and {e["reason"]["code"] for e in denied} == {"path"}, denied
    assert not any("injected" in e for e in denied)
    nothing_of_it_is_recorded(sandbox, engine, probe, SECRET, name)


def test_the_audit_says_of_each_request_whether_the_credential_went_with_it_and_why_not(sandbox, probe):
    """The connection names the service; each request names the service whose credential the proxy set, or says why it set none."""
    name = sandbox.spawn("paths")
    for path in ("/anything/api/v1", "/anything/console", "/anything/api/projects/group%2Fproject"):
        assert probe.curl(name, "--path-as-is", f"https://httpbin.org{path}").returncode == 0
    probe.curl(name, "-H", "X-Egzo-Secret: the-agents-own", "https://httpbin.org/anything/home")

    events = probe.audit(agent=name, host="httpbin.org")
    connects = [e for e in events if e["event"] == "connect"]
    assert connects and all(e["mode"] == "inject" and e["service"] == "echo" for e in connects), connects
    allowed = {e["path"]: e for e in events if e["event"] == "request" and e["decision"] == "allow"}
    assert allowed["/anything/api/v1"]["injected"] == "echo" and "reason" not in allowed["/anything/api/v1"]
    outside = allowed["/anything/console"]
    assert "injected" not in outside and outside["reason"]["code"] == "outside-paths" and "echo" in outside["reason"]["text"]
    assert "X-Egzo-Secret" in allowed["/anything/home"]["reason"]["text"], "the header the agent sent itself is told apart"
    escaped = allowed["/anything/api/projects/group/project"]
    assert escaped["injected"] == "echo" and escaped["raw_path"] == "/anything/api/projects/group%2Fproject", "what went on the wire"

    unlimited = sandbox.spawn("echo")
    probe.curl(unlimited, "https://httpbin.org/headers")
    request = next(e for e in probe.requests_to("httpbin.org", agent=unlimited))
    assert request["injected"] == "echo" and "reason" not in request


def test_spawn_needs_no_secret_because_the_profiles_were_loaded_by_up(sandbox, probe):
    """Spawn only binds the instance's token to its profile's name: the hub, which never reads secrets, can do it."""
    spawned = sandbox.sh("agent spawn echo", env={"DEPLOY_TOKEN": ""}, timeout=300)  # the secret is unreadable now
    assert spawned.returncode == 0, spawned.stderr
    echoed = probe.curl("echo-1", "https://httpbin.org/headers")
    assert json.loads(echoed.stdout)["headers"]["X-Egzo-Secret"] == SECRET
    assert SECRET not in spawned.stdout + spawned.stderr


def test_the_agent_types_a_placeholder_and_the_proxy_swaps_it_for_the_secret_where_it_should_and_nowhere_else(sandbox, engine, probe):
    name, other = sandbox.spawn("login"), sandbox.spawn("plain")
    typed = engine.exec(probe.container(name).name, "printenv", "SITE_PASSWORD").stdout.strip()
    assert PLACEHOLDER.fullmatch(typed), "the placeholder is in the environment, the secret is nowhere"
    nothing_of_it_is_recorded(sandbox, engine, probe, SITE_SECRET, name)

    header = probe.shell(name, '-H "X-Site-Key: $SITE_PASSWORD" https://httpbin.org/headers')
    assert json.loads(header.stdout)["headers"]["X-Site-Key"] == SITE_SECRET
    form = probe.shell(name, '-d "user=alice&password=$SITE_PASSWORD" https://httpbin.org/post')
    assert json.loads(form.stdout)["form"] == {"user": "alice", "password": SITE_SECRET}
    body = probe.shell(name, '-H "Content-Type: application/json" -d "{\\"password\\": \\"$SITE_PASSWORD\\"}" https://httpbin.org/post')
    assert json.loads(body.stdout)["json"] == {"password": SITE_SECRET}
    query = probe.shell(name, '"https://httpbin.org/get?password=$SITE_PASSWORD"')
    assert json.loads(query.stdout)["args"] == {"password": SITE_SECRET}
    probe.curl(name, "https://httpbin.org/anything/nothing-typed")
    swapped = {e["path"]: e.get("substituted") for e in probe.requests_to("httpbin.org", agent=name)}
    assert swapped["/headers"] == swapped["/post"] == swapped["/get"] == ["login"], "the audit names the service whose placeholder was swapped"
    assert swapped["/anything/nothing-typed"] is None and not any("injected" in e for e in probe.requests_to("httpbin.org", agent=name))

    require_reachable("httpbingo.org")
    elsewhere = probe.shell(name, '-H "X-Site-Key: $SITE_PASSWORD" https://httpbingo.org/headers')
    sent = json.loads(elsewhere.stdout)["headers"]["X-Site-Key"]
    assert PLACEHOLDER.fullmatch(sent if isinstance(sent, str) else sent[0]) and SITE_SECRET not in elsewhere.stdout, "not swapped on any other host"

    lacking = probe.shell(other, f'-H "X-Site-Key: {typed}" https://httpbin.org/headers')
    assert json.loads(lacking.stdout)["headers"]["X-Site-Key"] == typed, "not swapped for an agent whose profile lacks the service"

    # a request that cannot be scanned is refused with the reason, the same way every time, and the refusal never hands the secret back
    post = 'curl -sS -m 25 -o /dev/null -w "%{http_code}" https://httpbin.org/post'
    container = probe.container(name).name
    assert engine.exec(container, "sh", "-c", f"head -c 2097152 /dev/zero | {post} --data-binary @-").stdout.strip() == "413"
    assert engine.exec(container, "sh", "-c", f'{post} -H "Content-Encoding: gzip" -d x').stdout.strip() == "415"
    denied = [e for e in probe.audit(agent=name, host="httpbin.org") if e.get("decision") == "deny"]
    assert len(denied) == 2 and all(e["event"] == "request" and e["reason"]["code"] == "placeholder" for e in denied)
    reasons = [e["reason"]["text"] for e in denied]
    assert any("1 MiB" in r for r in reasons) and any("compressed" in r for r in reasons)
    refused = engine.exec(container, "sh", "-c", 'curl -sS -m 25 -H "Content-Encoding: $SITE_PASSWORD" -d x https://httpbin.org/post')
    assert "compressed" in refused.stdout or "egzo:" in refused.stdout
    assert SITE_SECRET not in refused.stdout + refused.stderr and SITE_SECRET not in json.dumps(probe.audit())


def test_up_refuses_a_missing_secret_and_a_token_bound_to_the_wrong_service_before_creating_anything(live_project, engine, agent_image):
    p = live_project
    echo = document(agent_image, "echo")
    p.write(echo)
    missing = p.sh("project up", env={"DEPLOY_TOKEN": "", "SITE_SECRET": "x"})
    assert missing.returncode != 0 and "main/DEPLOY_TOKEN" in missing.stderr and engine.resources(p.name) == []

    # api.anthropic.com answers 401 to a subscription token sent as x-api-key: say so before starting anything
    p.write(spec(egress={"default": anthropic_profile()}, agents={"coder": custom(agent_image)}))
    oauth = p.sh("project up", env={"ANTHROPIC_API_KEY": "sk-ant-oat01-" + "x" * 60}, timeout=300)
    assert oauth.returncode != 0 and "OAuth" in oauth.stderr and "anthropic-oauth" in oauth.stderr
    assert "oat01" not in oauth.stderr and not engine.containers(p.name)
    wrong = {"allow": ["platform.claude.com"], "services": {"anthropic-oauth": "main/ANTHROPIC_API_KEY"}}
    p.write(spec(egress={"default": wrong}, agents={"coder": custom(agent_image)}))
    api = p.sh("project up", env={"ANTHROPIC_API_KEY": "sk-ant-api03-" + "x" * 60}, timeout=300)
    assert api.returncode != 0 and "API key" in api.stderr and "anthropic" in api.stderr
    assert "api03" not in api.stderr and not engine.containers(p.name)


def test_a_rotated_secret_reaches_the_proxy_without_recreating_anything_and_the_placeholder_stays_and_pass_is_read_for_its_first_line(live_project, engine, agent_image, tmp_path):
    p = live_project
    both = document(agent_image, "echo", "login")
    p.write(both)
    p.ok("project up", env={"DEPLOY_TOKEN": "first-value-0123456789", "SITE_SECRET": "site-first-0123456789"}, timeout=300)
    probe = Probe(engine, p)
    echo, login = p.spawn("echo"), p.spawn("login")
    before = {s: probe.container(s).raw["Id"] for s in ("control", "proxy", echo, login)}
    placeholder = engine.exec(probe.container(login).name, "printenv", "SITE_PASSWORD").stdout.strip()

    p.ok("project up", env={"DEPLOY_TOKEN": "second-value-0123456789", "SITE_SECRET": "site-second-0123456789"}, timeout=300)
    assert {s: probe.container(s).raw["Id"] for s in ("control", "proxy", echo, login)} == before
    assert json.loads(probe.curl(echo, "https://httpbin.org/headers").stdout)["headers"]["X-Egzo-Secret"] == "second-value-0123456789"
    later = p.spawn("login")
    assert engine.exec(probe.container(later).name, "printenv", "SITE_PASSWORD").stdout.strip() == placeholder, "it never changes, even if the secret is rotated"
    sent = json.loads(probe.shell(login, '-H "X-Site-Key: $SITE_PASSWORD" https://httpbin.org/headers').stdout)["headers"]["X-Site-Key"]
    assert sent == "site-second-0123456789", "a running agent gets the new value"

    p.retire()
    fake_pass = FakePass(tmp_path / "pass", **{"deploy/token": "first-line-secret-0123456789\nurl: https://example.com\n"})
    passed = spec(
        vaults={"main": {"backend": "pass"}},
        egress={"default": {"services": {"echo": {"hosts": ["httpbin.org"], "inject": {"header": "X-Egzo-Secret"}, "secret": "main/deploy/token"}}}},
        agents={"coder": custom(agent_image)},
    )
    p.write(passed)
    p.ok("project up", env=fake_pass.env, timeout=300)
    name = p.spawn("coder")
    assert json.loads(probe.curl(name, "https://httpbin.org/headers").stdout)["headers"]["X-Egzo-Secret"] == "first-line-secret-0123456789"
