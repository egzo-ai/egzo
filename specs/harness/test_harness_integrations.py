# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""The harness integrations: Claude Code and OpenCode as images egzo runs, in their native TUI.

Every integration must (design: bypass mode is a requirement): pre-configure the harness's own bypass mode, skip every first-run prompt,
report its life cycle through hooks, reach the control sidecar's MCP tools, run as the invoking user, and never hold a real credential.
The images are the real ones from `harness/<name>/Dockerfile`; no model is called, so no credential is needed.

One project is up for the whole module, with a template for each way these specs set a harness up (a workspace, two, a model, a prompt,
permissions left to the harness, a subscription token); an instance is spawned for what a spec reads, and removed after it."""

import json
import os
import re
import subprocess
import time

import pexpect
import pytest

from support import agent, spec

HARNESSES = ["claude-code", "opencode"]
ANTHROPIC = {"allow": ["platform.claude.com"], "services": {"anthropic": "main/ANTHROPIC_API_KEY"}}
OAUTH = {"allow": ["platform.claude.com"], "services": {"anthropic-oauth": "main/CLAUDE_CODE_OAUTH_TOKEN"}}
OAUTH_TOKEN = "sk-ant-oat01-egzo-spec-not-a-real-token-" + "0" * 40
API_KEY = "sk-ant-egzo-spec-not-a-real-key-0000000000"
# what each harness prints when its prompt is ready, and what must never appear before that
PROMPT_READY = {"claude-code": rb"bypass permissions on|\? for shortcuts", "opencode": rb"Ask anything"}
BINARY = {"claude-code": "claude", "opencode": "opencode"}
FIRST_RUN = [
    rb"Choose the text style", rb"Let's get started", rb"Do you trust", rb"trust the files", rb"Yes, I trust",
    rb"Bypass Permissions mode", rb"WARNING: Claude Code running in Bypass", rb"Detected a custom API key",
    rb"Select login method", rb"Paste code here", rb"Sign in", rb"Welcome to Claude Code",
]


@pytest.fixture(scope="module")
def sandbox(shared_project, engine, egzo):
    p = shared_project
    p.env.update({"ANTHROPIC_API_KEY": API_KEY, "CLAUDE_CODE_OAUTH_TOKEN": OAUTH_TOKEN})
    (p.root / "coder.md").write_text("Always answer in rhyme.\n")
    cc, oc = engine.harness_image("claude-code", egzo.binary), engine.harness_image("opencode", egzo.binary)
    quick = {"inject": {"human_quiet": "1s"}}
    p.up(
        spec(
            vaults={"main": {"backend": "env", "secrets": ["ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"]}},
            egress={"default": ANTHROPIC, "oauth": OAUTH},
            workspaces={"scratch": {}, "one": {}, "two": {}},
            agents={
                "claude-code": agent(harness="claude-code", image=cc, workspaces=["scratch"], **quick),
                "opencode": agent(harness="opencode", image=oc, workspaces=["scratch"], **quick),
                "cc-two": agent(harness="claude-code", image=cc, workspaces=["one", "two"]),
                "cc-model": agent(harness="claude-code", image=cc, model="claude-sonnet-5-5", prompt="./coder.md"),
                "cc-asks": agent(harness="claude-code", image=cc, permissions="default"),
                "cc-oauth": agent(harness="claude-code", image=cc, egress="oauth"),
                "oc-model": agent(harness="opencode", image=oc, model="anthropic/claude-sonnet-5-5", prompt="./coder.md"),
                "oc-asks": agent(harness="opencode", image=oc, permissions="default"),
                "emit": agent(harness="custom", image=engine.session_image(egzo.binary), env={"FAKE_TUI": "emit"}),
            },
        ),
        timeout=900,
    )
    return p


@pytest.fixture(autouse=True)
def start_from_no_instance(sandbox):
    sandbox.retire()
    yield


def in_instance(engine, project, name, command):
    return engine.exec(engine.instance(project.name, name).name, "sh", "-c", command)


def read(engine, project, name, path):
    result = in_instance(engine, project, name, f"cat {path}")
    assert result.returncode == 0, f"{path}: {result.stderr}"
    return result.stdout


def read_json(engine, project, name, path):
    return json.loads(read(engine, project, name, path))


def cmdline(engine, project, name, process):
    return in_instance(engine, project, name, f"tr '\\0' ' ' < /proc/$(pgrep -n -x {process})/cmdline").stdout


def env_of(engine, project, name):
    return dict(e.split("=", 1) for e in engine.instance(project.name, name).raw["Config"]["Env"])


def events(project):
    return [json.loads(line) for line in project.ok("events").stdout.splitlines() if line.startswith("{")]


def wait_until(check, what, timeout, context=None):
    deadline = time.time() + timeout
    while time.time() < deadline:
        found = check()
        if found:
            return found
        time.sleep(1)
    raise AssertionError(f"timed out waiting for {what}" + (f"\n{context()}" if context else ""))


@pytest.mark.parametrize("harness", HARNESSES)
def test_the_harness_runs_as_the_invoking_user_in_its_workspace_with_no_real_credential_and_keeps_its_home(sandbox, engine, egzo, harness):
    p = sandbox
    name = p.spawn(harness, f"{harness}-1", timeout=900)
    assert in_instance(engine, p, name, "id -u").stdout.strip() == str(os.getuid())
    version = in_instance(engine, p, name, f"{BINARY[harness]} --version")
    assert version.returncode == 0 and re.search(r"\d+\.\d+", version.stdout), version.stderr
    for tool in ("egzo", "git", "curl", "rg", "node"):
        assert in_instance(engine, p, name, f"command -v {tool}").returncode == 0, f"{tool} is missing from the harness image"

    find = (
        "for p in /proc/[0-9]*; do c=$(tr '\\0' ' ' < $p/cmdline 2>/dev/null); "
        "case \"$c\" in sh*|/usr/local/bin/egzo*) ;; *claude*|*opencode*) readlink $p/cwd; break;; esac; done"
    )
    assert in_instance(engine, p, name, find).stdout.strip() == "/workspace/scratch", "it starts in its workspace and stays up"
    assert engine.instance(p.name, name).raw["State"]["Running"]

    assert API_KEY not in json.dumps(engine.instance(p.name, name).raw["Config"]["Env"])
    leaked = in_instance(engine, p, name, "grep -rl 'sk-ant-egzo-spec' / --exclude-dir=proc --exclude-dir=sys 2>/dev/null || true")
    assert leaked.stdout.strip() == "", "no real credential is inside the agent"

    # first run: nothing to answer
    environment = {**os.environ, **p.env}
    environment.pop("EGZO_PROJECT_NAME", None)
    client = pexpect.spawn(egzo.binary, ["agent", "attach", name], cwd=p.root, env=environment, dimensions=(40, 120), encoding=None, timeout=90)
    client.expect(PROMPT_READY[harness], timeout=90)
    time.sleep(1)
    screen = client.before + client.after
    try:
        while True:
            screen += client.read_nonblocking(65536, timeout=1)
    except (pexpect.TIMEOUT, pexpect.EOF):
        pass
    for question in FIRST_RUN:
        assert not re.search(question, screen), f"first-run prompt on screen: {question!r}"
    client.send(b"\x1d")
    client.expect(pexpect.EOF)

    # its conversation home survives a stop and start, and goes with the instance
    in_instance(engine, p, name, "echo kept > $HOME/marker")
    p.ok("stop", name)
    p.ok("start", name, timeout=300)
    assert read(engine, p, name, "$HOME/marker").strip() == "kept"
    p.ok("agent rm --force", name)
    assert not [r for r in engine.resources(p.name) if r.kind == "volume" and r.name.endswith(f"{name}-home")]


def test_claude_code_is_configured_to_bypass_permissions_trust_its_workspaces_report_to_control_and_reach_its_tools(sandbox, engine):
    p = sandbox
    scratch, two, model, asks = (p.spawn(t, name, timeout=900) for t, name in (("claude-code", "cc-a"), ("cc-two", "cc-b"), ("cc-model", "cc-c"), ("cc-asks", "cc-d")))

    settings = read_json(engine, p, scratch, "$HOME/.claude/settings.json")
    assert settings["skipDangerousModePermissionPrompt"] is True
    # bypass comes from the flag: permissions.defaultMode in the settings makes Claude Code ask on its first start whether to make auto
    # mode the default
    assert "defaultMode" not in settings.get("permissions", {})
    assert "--permission-mode bypassPermissions" in cmdline(engine, p, scratch, "claude")
    state = read_json(engine, p, scratch, "$HOME/.claude.json")
    assert state["hasCompletedOnboarding"] is True and "IS_SANDBOX=1" in engine.instance(p.name, scratch).raw["Config"]["Env"]
    assert state["projects"]["/workspace/scratch"]["hasTrustDialogAccepted"] is True
    assert state["customApiKeyResponses"]["approved"], "the placeholder API key must be pre-approved"
    env = env_of(engine, p, scratch)
    assert env["ANTHROPIC_API_KEY"] and env["ANTHROPIC_API_KEY"] != API_KEY

    for hook in ("SessionStart", "UserPromptSubmit", "Stop", "Notification"):
        commands = [h["command"] for group in settings["hooks"][hook] for h in group["hooks"]]
        assert f"egzo hook {hook}" in commands, hook
    server = state["mcpServers"]["egzo"]
    assert server["type"] == "http" and server["url"] == "http://control:7777/mcp" and server["headers"]["Authorization"].startswith("Basic ")

    assert "--append-system-prompt" in cmdline(engine, p, scratch, "claude")
    instructions = read(engine, p, scratch, "$HOME/.egzo/instructions.md")
    for needed in ("get_message", "list_messages", "resolve", "outcome", "update", "ask", "message(", "agents"):
        assert needed in instructions, f"the platform instructions do not mention {needed}"

    several_state = read_json(engine, p, two, "$HOME/.claude.json")
    several_settings = read_json(engine, p, two, "$HOME/.claude/settings.json")
    for workspace in ("one", "two"):
        assert several_state["projects"][f"/workspace/{workspace}"]["hasTrustDialogAccepted"] is True
        assert f"/workspace/{workspace}" in several_settings["permissions"]["additionalDirectories"]

    assert "--model claude-sonnet-5-5" in cmdline(engine, p, model, "claude")
    text = read(engine, p, model, "$HOME/.egzo/instructions.md")
    assert "Always answer in rhyme." in text and "get_message" in text, "the prompt file is added to the instructions"

    asked = read_json(engine, p, asks, "$HOME/.claude/settings.json")
    assert asked["permissions"].get("defaultMode", "default") == "default" and not asked.get("skipDangerousModePermissionPrompt")
    assert "--permission-mode" not in cmdline(engine, p, asks, "claude")
    assert "IS_SANDBOX=1" not in engine.instance(p.name, asks).raw["Config"]["Env"], "permissions: default keeps the harness prompts"


def test_opencode_is_configured_to_allow_every_tool_report_through_a_plugin_and_reach_its_tools(sandbox, engine):
    p = sandbox
    default, model, asks = (p.spawn(t, name, timeout=900) for t, name in (("opencode", "oc-a"), ("oc-model", "oc-b"), ("oc-asks", "oc-c")))
    config = read_json(engine, p, default, "$HOME/.config/opencode/opencode.json")
    assert config["permission"] in ("allow", {"*": "allow"}) or all(v == "allow" for v in config["permission"].values())
    server = config["mcp"]["egzo"]
    assert server["type"] == "remote" and server["url"] == "http://control:7777/mcp" and server["headers"]["Authorization"].startswith("Basic ")
    plugin = in_instance(engine, p, default, "cat $HOME/.config/opencode/plugin/egzo.js $HOME/.config/opencode/plugins/egzo.js 2>/dev/null").stdout
    assert "session.idle" in plugin
    assert "UserPromptSubmit" in plugin and "Stop" in plugin and "SessionStart" in plugin

    configured = read_json(engine, p, model, "$HOME/.config/opencode/opencode.json")
    assert configured["model"] == "anthropic/claude-sonnet-5-5" and any("instructions.md" in path for path in configured["instructions"])
    assert "Always answer in rhyme." in read(engine, p, model, "$HOME/.egzo/instructions.md")

    asked = read_json(engine, p, asks, "$HOME/.config/opencode/opencode.json")
    assert asked.get("permission") not in ("allow", {"*": "allow"}), "permissions: default keeps the prompts"


def test_claude_code_gets_an_oauth_placeholder_when_the_profile_injects_a_bearer_token(sandbox, engine):
    name = sandbox.spawn("cc-oauth", timeout=900)
    env = env_of(engine, sandbox, name)
    assert env.get("CLAUDE_CODE_OAUTH_TOKEN"), "Claude Code reads a subscription token from CLAUDE_CODE_OAUTH_TOKEN"
    assert env["CLAUDE_CODE_OAUTH_TOKEN"] != OAUTH_TOKEN
    assert "ANTHROPIC_API_KEY" not in env, "an API key placeholder would make Claude Code send x-api-key"
    assert OAUTH_TOKEN not in json.dumps(engine.instance(sandbox.name, name).raw["Config"])


def test_the_hook_command_posts_its_payload_as_a_hook_event_and_never_blocks_or_fails_the_harness(sandbox, engine):
    p = sandbox
    name = p.spawn("emit", timeout=300)
    container = engine.instance(p.name, name).name
    ran = subprocess.run(
        [engine.cli, "exec", "-i", container, "egzo", "hook", "PreToolUse"],
        input='{"tool_name":"Bash"}', text=True, capture_output=True, env={**os.environ, **engine.env},
    )
    assert ran.returncode == 0, ran.stderr
    found = [e for e in events(p) if e["type"] == "hook" and e["text"] == "PreToolUse" and e["agent"] == name]
    assert found

    start = time.time()
    broken = engine.exec(container, "sh", "-c", "EGZO_CONTROL_URL=http://127.0.0.1:9 egzo hook Stop </dev/null; echo exit=$?")
    assert "exit=0" in broken.stdout and time.time() - start < 6


@pytest.mark.parametrize("harness", HARNESSES)
def test_the_real_tui_reports_idle_and_is_told_a_message_is_waiting(sandbox, harness):
    """No model is called: the harness reports the typed line through its hook as soon as it is submitted. The line must reach the real TUI,
    be submitted, and name the message by its id and nothing else of it."""
    p = sandbox
    name = p.spawn(harness, f"{harness}-2", timeout=900)

    def mine(kind):
        return [e for e in events(p) if e["type"] == kind and e.get("agent") == name]

    wait_until(lambda: (rows := mine("activity")) and rows[-1]["text"] == "idle", "the harness to report that its session started", 45)
    sent = p.ok("message send", name, "hello from the operator, TOP-SECRET-PAYLOAD").stdout.split()[1]

    def context():
        return "\n".join(f'{e["seq"]} {e["type"]} {e.get("text", "")}' for e in events(p))

    wait_until(lambda: mine("announced"), "the message to be announced", 40, context=context)
    typed = wait_until(lambda: [e["data"]["prompt"] for e in mine("hook") if e["text"] == "UserPromptSubmit"], "the TUI to submit the typed line", 40, context=context)
    assert typed[0] == f"check egzo message {sent} and handle the request for me."
    assert "TOP-SECRET-PAYLOAD" not in json.dumps(typed)
