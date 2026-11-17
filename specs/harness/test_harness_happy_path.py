# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""The happy path of every harness, end to end: the real TUI, the real proxy, and a scripted model API (fixtures/model_api_stub.py) under the
provider's real name on the egress network (`proxy.upstream`), so the harness needs no setting of its own and calls home as it does in
production. It is pinged and answers, and then it opens a page in the browser it started itself. The page is a real one on the internet.

What each harness gives its model as its system prompt, in full, is pinned in harness/prompts/<harness>.txt. A harness changes it often,
and a change can break how it works with egzo: a version that words it differently fails here until someone has read the diff and pinned
it again with --update-prompts, by hand. A version bump that leaves the prompt as it was passes. The tools are not pinned: their schemas
are too large for anyone to review."""

import json
import re
import time
from pathlib import Path

import pexpect
import pytest

from oauthkit import make_certificates
from support import agent, spec

HOSTS = ("api.anthropic.com", "api.openai.com")
HARNESSES = ["claude-code", "opencode", "codex"]
FIXTURES = Path(__file__).parent.parent / "fixtures"
PROMPTS = Path(__file__).parent / "prompts"
DOCKERFILES = Path(__file__).parent.parent.parent / "harness"
ANTHROPIC_KEY, OPENAI_KEY = "sk-ant-egzo-spec-not-a-real-key-" + "0" * 10, "sk-proj-egzo-spec-not-a-real-key-" + "0" * 40
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
    anthropic, openai = {"anthropic": "env/ANTHROPIC_API_KEY"}, {"openai": "env/OPENAI_API_KEY"}
    p.up(
        spec(
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
    engine.exec(name, "python3", "/tmp/model_api_stub.py", "/tmp/stub.crt", "/tmp/stub.key", STUB_LOG, PAGE, ANTHROPIC_KEY, f"Bearer {OPENAI_KEY}", detach=True)
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
    return [r for r in asked(engine, p) if "real_key" in r]


@pytest.mark.parametrize("harness", HARNESSES)
def test_a_harness_pinged_calls_home_through_the_proxy_and_shows_the_answer(sandbox, engine, harness):
    p = sandbox
    name = start(p, harness)
    p.ok("message send", name, "ping")
    log = lambda: "the model API was asked:\n" + "\n".join(json.dumps(r) for r in asked(engine, p)[-5:])
    wait_until(lambda: model_turns(engine, p), "the harness to call the model", 90, context=log)
    assert model_turns(engine, p)[0]["real_key"], "the proxy puts the real key on what the harness sends"
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


def version(harness):
    """The version of the harness its Dockerfile installs."""
    found = re.search(r"^ARG \w+_VERSION=(\S+)$", (DOCKERFILES / harness / "Dockerfile").read_text(), re.MULTILINE)
    assert found, f"harness/{harness}/Dockerfile names no version"
    return found.group(1)


def normalized(text, p):
    """What a harness sends, with what changes from one run to the next in a fixed form. This list is all that is replaced: anything else
    that changes is a difference."""
    replacements = [
        (re.escape(p.name), "<project>"),  # the project's name is new for every run
        (r"\b\d{4}-\d{2}-\d{2}\b", "<date>"),  # today, as Claude Code and Codex write it
        (r"\b(Mon|Tue|Wed|Thu|Fri|Sat|Sun) (Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec) \d{2} \d{4}\b", "<date>"),  # as OpenCode does
        (r"OS Version: [^\n\\\"]*", "OS Version: <host kernel>"),  # the kernel of the machine the specs run on
        (r"cch=[0-9a-f]+", "cch=<hash>"),  # Claude Code's billing header carries a hash of the request
        (r"cc_prompt_id=[0-9a-f-]+", "cc_prompt_id=<id>"),  # and an id new for every prompt
        (r"cc_version=\d+\.\d+\.\d+\.[0-9a-f]+", "cc_version=<version>.<hash>"),  # and its version, which a bump changes, with a short hash
    ]
    for pattern, fixed in replacements:
        text = re.sub(pattern, fixed, text)
    return text


def rendered_system(system):
    """A system prompt as text: each part under a heading that says where in the request it was."""
    parts = []
    if "system" in system:
        blocks = system["system"] if isinstance(system["system"], list) else [{"type": "text", "text": system["system"]}]
        parts += [(f"system[{i}]", b["text"] if b.get("type") == "text" else json.dumps(b, sort_keys=True)) for i, b in enumerate(blocks)]
    else:
        parts.append(("instructions", system.get("instructions") or ""))
        for i, message in enumerate(system["messages"]):
            content = message.get("content")
            texts = [c.get("text", json.dumps(c, sort_keys=True)) for c in content] if isinstance(content, list) else [str(content)]
            parts += [(f"{message['role']}[{i}][{j}]", text) for j, text in enumerate(texts)]
    return "".join(f"===== {where} =====\n{text}\n" for where, text in parts)


def pinned(request, harness, got):
    """Compare got with this harness's pinned prompt, or pin it with --update-prompts."""
    path = PROMPTS / f"{harness}.txt"
    if request.config.getoption("--update-prompts"):
        PROMPTS.mkdir(exist_ok=True)
        path.write_text(got)
        return
    if not path.exists():
        pytest.fail(f"nothing is pinned for {harness}: run with --update-prompts, read the diff, and commit {path.name}", pytrace=False)
    want = path.read_text()
    if got != want:
        import difflib

        diff = "".join(difflib.unified_diff(want.splitlines(True), got.splitlines(True), f"pinned {path.name}", "sent now", n=2))
        pytest.fail(f"{harness} {version(harness)} gives its model something else than is pinned. If the change is safe for egzo, pin it "
                    f"again with --update-prompts and commit it:\n{diff}", pytrace=False)


@pytest.mark.parametrize("harness", HARNESSES)
def test_a_harness_gives_its_model_exactly_the_pinned_system_prompt(sandbox, engine, harness, request):
    p = sandbox
    name = start(p, harness)
    p.ok("message send", name, "ping")
    # the agent's own turn is the request that offers egzo's tools: a harness also asks its model for a title and the like. Codex defers
    # MCP tools: it sends no schema for them, only the namespace mcp__egzo, listed in its system prompt as a deferred tool namespace.
    offers_egzo = lambda r: any(t.endswith("get_message") for t in r["tools"] or []) or "- mcp__egzo:" in json.dumps(r.get("system"))
    turn = lambda: [r for r in model_turns(engine, p) if r["tools"] and offers_egzo(r)]
    log = lambda: "the model API was asked:\n" + "\n".join(json.dumps({k: r.get(k) for k in ("path", "tools")}) for r in asked(engine, p)[-5:])
    first = wait_until(turn, "the harness to ask its model for the agent's turn", 90, context=log)[0]

    system = rendered_system(first["system"])
    for told in ("# egzo: your sandbox\n\nYou are one of several agents run by egzo", f"You are the agent {name}, an instance of the template {harness},",
                 "You were started by the operator"):
        assert told in system, f"{harness} does not give its model egzo's instructions ({told!r} is missing):\n{system}"
    pinned(request, harness, normalized(system, p))
