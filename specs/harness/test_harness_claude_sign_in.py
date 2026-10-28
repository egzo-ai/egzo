# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""Claude Code signed in with a Claude subscription through the built-in `claude` service: the first agent of the project shows Claude
Code's own /login, which the proxy captures (`proxy/test_proxy_oauth.py`); every agent after it starts signed in, with the stand-ins of the
project's sign-in (`claude auth login` with CLAUDE_CODE_OAUTH_REFRESH_TOKEN, which the proxy answers). No agent holds a real token, and
none gets an API key placeholder, which would win over the sign-in.

No model is called. The stub provider of `oauthkit` answers as platform.claude.com and api.anthropic.com on the project's egress network
(Claude Code's URLs are fixed), so no account is needed: the real sign-in needs one (known-issues/specs-depend-on-online-services.md)."""

import json
import re
import time

import pexpect
import pytest

from oauthkit import ANTHROPIC, UPSTREAM_ANTHROPIC, sign_in_claude, start_stub, stop_stub
from support import agent, spec

READY = rb"bypass permissions on|\? for shortcuts"
LOGIN = rb"/login|Select login method|Not logged in"
SCOPE = "user:inference user:profile user:sessions:claude_code user:mcp_servers"  # what the stub grants
CREDENTIAL_VARIABLES = ("ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN")


@pytest.fixture(scope="module")
def sandbox(shared_project, engine, egzo):
    p = shared_project
    image = engine.harness_image("claude-code", egzo.binary)
    start = start_stub(engine, p, engine.agent_image(), ANTHROPIC)
    p.up(
        spec(
            vaults={"main": {"backend": "env"}},
            proxy=UPSTREAM_ANTHROPIC,
            egress={"subscription": {"services": {"claude": {"credential": {"method": "oauth"}}}}},
            workspaces={"scratch": {}},
            agents={"claude": agent(harness="claude-code", image=image, egress="subscription", workspaces=["scratch"])},
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


def environment(engine, project, name):
    """The agent container's environment, as its definition has it (what `inspect` shows)."""
    return dict(entry.split("=", 1) for entry in engine.instance(project.name, name).raw["Config"]["Env"])


def credentials(engine, project, name):
    """Claude Code's stored sign-in, or None."""
    text = in_instance(engine, project, name, "cat $HOME/.claude/.credentials.json 2>/dev/null").stdout
    return json.loads(text).get("claudeAiOauth") if text.strip() else None


def screen_of(project, name, ready):
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


def test_without_a_sign_in_claude_code_offers_its_own_login_holds_no_credential_and_spawn_says_so(sandbox, engine):
    p = sandbox
    name = "claude-first"
    spawned = p.ok("agent spawn claude", name, timeout=900)
    assert "claude" in spawned.stderr and "sign in" in spawned.stderr, spawned.stderr
    env = environment(engine, p, name)
    assert not [variable for variable in CREDENTIAL_VARIABLES if variable in env], "a placeholder would win over the sign-in"
    assert "CLAUDE_CODE_OAUTH_REFRESH_TOKEN" not in env, "the project has no sign-in to give"
    assert credentials(engine, p, name) is None
    assert re.search(LOGIN, screen_of(p, name, LOGIN))


def test_a_new_claude_code_agent_starts_signed_in_with_the_stand_ins_of_the_projects_sign_in(sandbox, engine):
    p = sandbox
    first = p.spawn("claude", "claude-1", timeout=900)
    held = sign_in_claude(engine, p, first)
    assert held["access_token"].startswith("egzo-") and held["refresh_token"].startswith("egzo-"), held
    assert held["scope"] == SCOPE

    second = p.spawn("claude", "claude-2", timeout=900)
    env = environment(engine, p, second)
    assert env.get("CLAUDE_CODE_OAUTH_REFRESH_TOKEN") == held["refresh_token"], "the stand-in of the project's sign-in, not a new one"
    assert env.get("CLAUDE_CODE_OAUTH_SCOPES") == SCOPE
    assert not [variable for variable in CREDENTIAL_VARIABLES if variable in env]

    stored = credentials(engine, p, second)
    assert stored is not None, "egzo agent run signed Claude Code in before its TUI started"
    assert stored["refreshToken"] == held["refresh_token"] and stored["accessToken"].startswith("egzo-")
    assert "user:inference" in stored["scopes"] and stored.get("subscriptionType") == "pro"

    screen = screen_of(p, second, READY)
    assert not re.search(LOGIN, screen), "with the project's sign-in, Claude Code starts signed in"
    stand_ins = {held["access_token"], held["refresh_token"], stored["accessToken"]}
    assert all(token not in json.dumps(r) for r in p.stub.requests() for token in stand_ins), "a stand-in never reaches the provider"
    profiles = [r for r in p.stub.requests() if r["path"] == "/api/oauth/profile"]
    assert profiles and all((r["authorization"] or "").startswith("Bearer sk-ant-oat01-stub-") for r in profiles), "fetched with the real token"
