# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""The happy path of every harness, end to end: the real TUI, the real proxy, and a scripted model API (fixtures/model_api_stub.py) under the
provider's real name on the egress network (`proxy.upstream`), so the harness needs no setting of its own and calls home as it does in
production. It is pinged and answers, and then it opens a page in the browser it started itself. The page is a real one on the internet."""

import json
import time
from pathlib import Path

import pexpect
import pytest

from oauthkit import make_certificates
from support import agent, spec

HOSTS = ("api.anthropic.com", "api.openai.com")
HARNESSES = ["claude-code", "opencode", "codex"]
FIXTURES = Path(__file__).parent.parent / "fixtures"
ANTHROPIC_KEY, OPENAI_KEY = "sk-ant-egzo-spec-not-a-real-key-" + "0" * 10, "sk-proj-egzo-spec-not-a-real-key-" + "0" * 40
REAL_KEY = {"claude-code": ANTHROPIC_KEY, "opencode": ANTHROPIC_KEY, "codex": f"Bearer {OPENAI_KEY}"}
PAGE, LANDED = "https://egzo.ai", "github.com/egzo-ai/egzo"
STUB, STUB_LOG = "model-api-stub", "/tmp/model-api.log"
BROWSE = ["egzo.ai", "github.com", "*.github.com", "*.githubassets.com", "*.githubusercontent.com"]


@pytest.fixture(scope="module")
def sandbox(shared_project, engine, egzo):
    p = shared_project
    p.env.update(ANTHROPIC_API_KEY=ANTHROPIC_KEY, OPENAI_API_KEY=OPENAI_KEY)
    make_certificates(p.root, HOSTS)
    images = {name: engine.harness_image(name, egzo.binary) for name in HARNESSES}
    quick = {"inject": {"human_quiet": "1s"}}
    anthropic, openai = {"anthropic": "main/ANTHROPIC_API_KEY"}, {"openai": "main/OPENAI_API_KEY"}
    p.up(
        spec(
            vaults={"main": {"backend": "env"}},
            proxy={"upstream": {"ca": "./stub-ca.pem", "private_hosts": list(HOSTS)}},
            egress={"anthropic": {"allow": BROWSE, "services": anthropic}, "openai": {"allow": BROWSE, "services": openai}},
            workspaces={"scratch": {}},
            agents={
                "claude-code": agent(harness="claude-code", image=images["claude-code"], egress="anthropic", workspaces=["scratch"], **quick),
                "opencode": agent(harness="opencode", image=images["opencode"], egress="anthropic", model="anthropic/claude-sonnet-5-5", workspaces=["scratch"], **quick),
                "codex": agent(harness="codex", image=images["codex"], egress="openai", workspaces=["scratch"], **quick),
            },
        ),
        timeout=1800,
    )
    name = f"{p.name}-{STUB}"
    aliases = [flag for host in HOSTS for flag in ("--network-alias", host)]
    started = engine.run("run", "-d", "--name", name, "--network", f"{p.name}_egress", *aliases, engine.agent_image(), "sleep", "infinity")
    assert started.returncode == 0, started.stderr
    for source in ("stub.crt", "stub.key"):
        engine.put_file(name, p.root / source, f"/tmp/{source}")
    engine.put_file(name, FIXTURES / "model_api_stub.py", "/tmp/model_api_stub.py")
    engine.exec(name, "python3", "/tmp/model_api_stub.py", "/tmp/stub.crt", "/tmp/stub.key", STUB_LOG, PAGE, detach=True)
    p.stub = name
    yield p
    engine.run("rm", "-f", name)


@pytest.fixture(autouse=True)
def start_from_no_instance(sandbox, engine):
    sandbox.retire()
    engine.exec(sandbox.stub, "sh", "-c", f"rm -f /tmp/browse; : > {STUB_LOG}")
    yield


def asked(engine, p):
    """What the model API was asked, in order."""
    return [json.loads(line) for line in engine.exec(p.stub, "cat", STUB_LOG).stdout.splitlines() if line.strip()]


def wait_until(check, what, timeout, context=None):
    deadline = time.time() + timeout
    while time.time() < deadline:
        found = check()
        if found:
            return found
        time.sleep(1)
    raise AssertionError(f"timed out waiting for {what}" + (f"\n{context()}" if context else ""))


def idle(p, name):
    rows = [json.loads(line) for line in p.ok("events").stdout.splitlines() if line.startswith("{")]
    rows = [e for e in rows if e["type"] == "activity" and e.get("agent") == name]
    return rows and rows[-1]["text"] == "idle"


def start(p, harness):
    name = p.spawn(harness, f"{harness}-1", timeout=900)
    wait_until(lambda: idle(p, name), "the harness to report that its session started", 90)
    return name


def model_turns(engine, p):
    return [r for r in asked(engine, p) if r.get("key")]


@pytest.mark.parametrize("harness", HARNESSES)
def test_a_harness_pinged_calls_home_through_the_proxy_and_shows_the_answer(sandbox, engine, harness):
    p = sandbox
    name = start(p, harness)
    p.ok("message send", name, "ping")
    log = lambda: "the model API was asked:\n" + "\n".join(json.dumps(r) for r in asked(engine, p)[-5:])
    wait_until(lambda: model_turns(engine, p), "the harness to call the model", 90, context=log)
    assert model_turns(engine, p)[0]["key"] == REAL_KEY[harness], "the proxy puts the real key on what the harness sends"
    client = p.pty("agent attach", name, dimensions=(40, 140), timeout=90)
    client.expect(b"pong", timeout=60)
    client.send(b"\x1d")
    client.expect(pexpect.EOF)


@pytest.mark.parametrize("harness", HARNESSES)
def test_a_harness_opens_a_page_in_its_browser_and_follows_the_redirect(sandbox, engine, harness, internet):
    p = sandbox
    engine.exec(p.stub, "touch", "/tmp/browse")
    name = start(p, harness)
    p.ok("message send", name, "open the page")
    log = lambda: "the model API was asked:\n" + "\n".join(json.dumps(r) for r in asked(engine, p)[-5:])
    answers = wait_until(lambda: [text for r in asked(engine, p) for text in r.get("results", []) if LANDED in text], f"the browser to land on {LANDED}", 120, context=log)
    assert answers
