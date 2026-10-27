# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""Codex as an egzo harness (harness/codex): the real `codex` TUI, started with egzo's settings on its command line, with no first-run
question, reporting its life cycle through hooks, and never holding a real credential: a placeholder API key for the `openai` service, or
the stand-ins of the project's ChatGPT sign-in, which the proxy keeps (`proxy/test_proxy_oauth.py`).

No model is called. The sign-in is made against the stub provider of `oauthkit`, under a service of the project's own: what the built-in
`chatgpt` service does with auth.openai.com, which needs an account (known-issues/specs-depend-on-online-services.md)."""

import json
import re
import time

import pexpect
import pytest

from oauthkit import IDP, SERVICE, UPSTREAM, start_stub, stop_stub, sign_in
from support import agent, spec

API_KEY = "sk-proj-egzo-spec-not-a-real-key-" + "0" * 40
READY = rb"describe a task|Ask Codex to do anything"
FIRST_RUN = [rb"Sign in with ChatGPT", rb"Use an OpenAI API key", rb"Do you trust", rb"Update available", rb"Welcome to Codex"]


@pytest.fixture(scope="module")
def sandbox(shared_project, engine, egzo):
    p = shared_project
    p.env["OPENAI_API_KEY"] = API_KEY
    image = engine.harness_image("codex", egzo.binary)
    start = start_stub(engine, p, engine.agent_image())
    quick = {"inject": {"human_quiet": "1s"}}
    p.up(
        spec(
            vaults={"main": {"backend": "env"}},
            proxy=UPSTREAM,
            egress={
                "key": {"services": {"openai": "main/OPENAI_API_KEY"}},
                "chatgpt": {"services": {"chatgpt": {"credential": {"method": "oauth"}}}},
                "stub": {"services": {"idp": SERVICE}},
            },
            workspaces={"scratch": {}},
            agents={
                "codex": agent(harness="codex", image=image, egress="key", workspaces=["scratch"], **quick),
                "signs-in": agent(harness="codex", image=image, egress="chatgpt", workspaces=["scratch"]),
                "stubbed": agent(harness="codex", image=image, egress="stub", workspaces=["scratch"]),
            },
        ),
        timeout=1200,
    )
    p.stub = start()
    yield p
    stop_stub(engine, p)


@pytest.fixture(autouse=True)
def start_from_no_instance(sandbox):
    sandbox.retire()
    yield


def in_instance(engine, project, name, command):
    return engine.exec(engine.instance(project.name, name).name, "sh", "-c", command)


def codex_cmdline(engine, project, name):
    """The arguments of the native codex process (`codex` on the PATH is a node wrapper that starts it)."""
    return in_instance(engine, project, name, "tr '\\0' '\\n' < /proc/$(pgrep -n -x codex)/cmdline").stdout.splitlines()


def screen_of(project, name, ready=READY):
    """What the TUI shows once it is drawn, read through `agent attach`."""
    client = project.pty("agent attach", name, dimensions=(40, 140), timeout=90)
    client.expect(ready, timeout=90)
    time.sleep(1)
    screen = client.before + client.after
    try:
        while True:
            screen += client.read_nonblocking(65536, timeout=1)
    except (pexpect.TIMEOUT, pexpect.EOF):
        pass
    client.send(b"\x1d")
    client.expect(pexpect.EOF)
    return screen


def events(project):
    return [json.loads(line) for line in project.ok("events").stdout.splitlines() if line.startswith("{")]


def wait_until(check, what, timeout):
    deadline = time.time() + timeout
    while time.time() < deadline:
        found = check()
        if found:
            return found
        time.sleep(1)
    raise AssertionError(f"timed out waiting for {what}")


def test_codex_with_an_api_key_starts_ready_with_egzos_settings_and_a_placeholder_key(sandbox, engine):
    p = sandbox
    name = p.spawn("codex", "codex-1", timeout=900)
    assert in_instance(engine, p, name, "codex --version").returncode == 0
    assert in_instance(engine, p, name, "id -u").stdout.strip() == str(engine.uid)

    auth = json.loads(in_instance(engine, p, name, "cat $HOME/.codex/auth.json").stdout)
    assert auth["auth_mode"] == "apikey" and auth["OPENAI_API_KEY"] != API_KEY and "placeholder" in auth["OPENAI_API_KEY"]
    assert API_KEY not in json.dumps(engine.instance(p.name, name).raw["Config"]["Env"])
    assert in_instance(engine, p, name, f"grep -rl '{API_KEY}' / --exclude-dir=proc --exclude-dir=sys 2>/dev/null || true").stdout.strip() == ""

    command = codex_cmdline(engine, p, name)
    assert "--dangerously-bypass-hook-trust" in command
    for setting in ('approval_policy="never"', "features.use_agent_identity=false", "check_for_update_on_startup=false",
                    'projects."/workspace/scratch".trust_level="trusted"'):
        assert setting in command, f"{setting} not in {command}"
    assert any(s.startswith("mcp_servers.egzo.url=") for s in command)
    config = in_instance(engine, p, name, "cat $HOME/.codex/config.toml 2>/dev/null || true").stdout
    assert "egzo" not in config and "control:7777" not in config, f"egzo writes nothing into the user's config.toml:\n{config}"

    screen = screen_of(p, name)
    for question in FIRST_RUN:
        assert not re.search(question, screen), f"first-run screen: {question!r}"


def test_codex_reports_idle_once_ready_and_is_told_a_message_is_waiting(sandbox):
    """No model is called: Codex's UserPromptSubmit hook reports the typed line as soon as it is submitted."""
    p = sandbox
    name = p.spawn("codex", "codex-2", timeout=900)

    def mine(kind):
        return [e for e in events(p) if e["type"] == kind and e.get("agent") == name]

    wait_until(lambda: (rows := mine("activity")) and rows[-1]["text"] == "idle", "codex to be reported idle", 60)
    sent = p.ok("message send", name, "hello from the operator").stdout.split()[1]
    typed = wait_until(lambda: [e["data"]["prompt"] for e in mine("state") if e.get("data", {}).get("prompt")], "the hook to report the prompt", 60)
    assert typed[0] == f"check egzo message {sent} and handle the request for me."


def test_without_a_sign_in_codex_offers_its_own_sign_in_and_spawn_says_so(sandbox, engine):
    p = sandbox
    name = "signs-in-1"
    spawned = p.ok("agent spawn signs-in", name, timeout=900)
    assert "chatgpt" in spawned.stderr and "sign in" in spawned.stderr, spawned.stderr
    assert in_instance(engine, p, name, "test -e $HOME/.codex/auth.json && echo yes || echo no").stdout.strip() == "no"
    screen = screen_of(p, name, ready=rb"Sign in with Device Code")
    assert re.search(rb"one-time code", screen)


def test_a_new_codex_agent_gets_the_stand_ins_of_the_projects_sign_in(sandbox, engine):
    p = sandbox
    first = p.spawn("stubbed", "stubbed-1", timeout=900)
    held = sign_in(engine, p, first)

    second = p.spawn("stubbed", "stubbed-2", timeout=900)
    auth = json.loads(in_instance(engine, p, second, "cat $HOME/.codex/auth.json").stdout)
    assert auth["auth_mode"] == "chatgpt" and auth["OPENAI_API_KEY"] is None
    tokens = auth["tokens"]
    assert tokens["refresh_token"] == held["refresh_token"], "the stand-in of the project's sign-in, not a new one"
    assert tokens["access_token"].split(".")[2].startswith("egzo-") and tokens["id_token"].split(".")[2].startswith("egzo-")
    assert all(token not in json.dumps(r) for r in p.stub.requests() for token in tokens.values()), "a stand-in never reaches the provider"
    screen = screen_of(p, second)
    assert not re.search(rb"Sign in with", screen), "with the project's sign-in, Codex starts signed in"
    assert IDP not in in_instance(engine, p, second, "cat $HOME/.codex/auth.json").stdout
