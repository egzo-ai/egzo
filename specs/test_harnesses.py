# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""The harness integrations: Claude Code and OpenCode as images egzo runs, in their native TUI.

Every integration must (design: bypass mode is a requirement): pre-configure the harness's own
bypass mode, skip every first-run prompt, report its life cycle through hooks, reach the control
sidecar's MCP tools, run as the invoking user, and never hold a real credential. The images are the
real ones from `harness/<name>/Dockerfile`; no model is called, so no credential is needed.
"""

import json
import os
import re
import time

import pexpect
import pytest

from conftest import LABEL_PREFIX
from support import agent, spec

pytestmark = pytest.mark.usefixtures("engine")

HARNESSES = ["claude-code", "opencode"]
ANTHROPIC = {"allow": ["platform.claude.com"], "services": {"anthropic": "main/ANTHROPIC_API_KEY"}}
# what each harness prints when its prompt is ready, and what must never appear before that
PROMPT_READY = {"claude-code": rb"bypass permissions on|\? for shortcuts", "opencode": rb"Ask anything"}
BINARY = {"claude-code": "claude", "opencode": "opencode"}
FIRST_RUN = [
    rb"Choose the text style", rb"Let's get started", rb"Do you trust", rb"trust the files", rb"Yes, I trust",
    rb"Bypass Permissions mode", rb"WARNING: Claude Code running in Bypass", rb"Detected a custom API key",
    rb"Select login method", rb"Paste code here", rb"Sign in", rb"Welcome to Claude Code",
]


@pytest.fixture(params=HARNESSES)
def harness(request):
    return request.param


def bring_up(project, document, timeout=900):
    """Write the document with each agent as a template named `<name>-template`, `up`, and spawn one instance
    of each named `<name>` (the addresses and container names the specs below use are the instance names)."""
    names = list(document["agents"])
    project.write({**document, "agents": {f"{name}-template": fields for name, fields in document["agents"].items()}})
    result = project.run("up", timeout=timeout)
    assert result.returncode == 0, result.stderr
    for name in names:
        project.spawn(f"{name}-template", name, timeout=timeout)
    return project


@pytest.fixture
def environment_secrets(live_project):
    live_project.env["ANTHROPIC_API_KEY"] = "sk-ant-egzo-spec-not-a-real-key-0000000000"
    live_project.env["GITHUB_TOKEN"] = "unused"
    live_project.env["DEPLOY_TOKEN"] = "unused"
    return live_project


@pytest.fixture
def launched(environment_secrets, harness_image, harness):
    def start(agent_fields=None, workspaces=None, **extra):
        image = harness_image(harness)
        fields = {"harness": harness, "image": image, **(agent_fields or {})}
        document = spec(egress={"default": ANTHROPIC}, agents={"coder": agent(**fields)}, **extra)
        if workspaces:
            document["workspaces"] = workspaces
        return bring_up(environment_secrets, document)

    return start


def container(engine, project, name="coder"):
    return engine.instance(project.name, name)


def read_in_agent(engine, project, path):
    result = engine.exec(container(engine, project).name, "sh", "-c", f"cat {path}")
    assert result.returncode == 0, f"{path}: {result.stderr}"
    return result.stdout


def spawn_attach(project, egzo, *args, dimensions=(40, 120)):
    environment = dict(os.environ)
    environment.pop("EGZO_PROJECT_NAME", None)
    environment.update(project.env)
    return pexpect.spawn(egzo.binary, ["attach", *args, "coder"], cwd=project.root, env=environment, dimensions=dimensions, encoding=None, timeout=90)


# --- the images --------------------------------------------------------------------------------------


def test_the_harness_runs_as_the_invoking_user_and_its_cli_works(launched, engine, harness):
    project = launched()
    who = engine.exec(container(engine, project).name, "id", "-u")
    assert who.stdout.strip() == str(os.getuid())
    version = engine.exec(container(engine, project).name, BINARY[harness], "--version")
    assert version.returncode == 0 and re.search(r"\d+\.\d+", version.stdout), version.stderr


def test_the_harness_image_has_the_session_holder_and_the_tools_an_agent_needs(launched, engine):
    project = launched()
    for tool in ("egzo", "git", "curl", "rg", "node"):
        found = engine.exec(container(engine, project).name, "sh", "-c", f"command -v {tool}")
        assert found.returncode == 0, f"{tool} is missing from the harness image"


def test_the_harness_starts_in_its_workspace_and_stays_up(launched, engine):
    project = launched(workspaces={"scratch": {}}, agent_fields={"workspaces": ["scratch"]})
    find = (
        "for p in /proc/[0-9]*; do c=$(tr '\\0' ' ' < $p/cmdline 2>/dev/null); "
        "case \"$c\" in sh*|/usr/local/bin/egzo*) ;; *claude*|*opencode*) readlink $p/cwd; break;; esac; done"
    )
    cwd = engine.exec(container(engine, project).name, "sh", "-c", find)
    assert cwd.stdout.strip() == "/workspace/scratch", cwd.stderr
    assert container(engine, project).raw["State"]["Running"]


def test_no_real_credential_is_inside_the_agent(launched, engine, harness):
    project = launched()
    inspect = json.dumps(container(engine, project).raw["Config"]["Env"])
    assert project.env["ANTHROPIC_API_KEY"] not in inspect
    files = engine.exec(container(engine, project).name, "sh", "-c", "grep -rl 'sk-ant-egzo-spec' / --exclude-dir=proc --exclude-dir=sys 2>/dev/null || true")
    assert files.stdout.strip() == ""


# --- first run: nothing to answer -----------------------------------------------------------------


def test_the_tui_reaches_its_prompt_with_no_first_run_question(launched, engine, egzo, harness):
    project = launched()
    client = spawn_attach(project, egzo)
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


def test_the_tui_keeps_its_conversation_home_across_a_stop_and_start(launched, engine):
    project = launched()
    engine.exec(container(engine, project).name, "sh", "-c", "echo kept > $HOME/marker")
    assert project.run("stop", "coder").returncode == 0
    assert project.run("start", "coder", timeout=300).returncode == 0
    assert read_in_agent(engine, project, "$HOME/marker").strip() == "kept"


def test_removing_an_instance_removes_its_conversation_home(launched, engine):
    project = launched()
    assert project.run("rm", "--force", "coder").returncode == 0
    assert not [r for r in engine.resources(project.name) if r.kind == "volume" and r.name.endswith("coder-home")]


# --- bypass mode ------------------------------------------------------------------------------------


@pytest.fixture
def claude(environment_secrets, harness_image):
    def start(agent_fields=None, workspaces=None):
        document = spec(egress={"default": ANTHROPIC}, agents={"coder": agent(harness="claude-code", image=harness_image("claude-code"), **(agent_fields or {}))})
        if workspaces:
            document["workspaces"] = workspaces
        return bring_up(environment_secrets, document)

    return start


@pytest.fixture
def opencode(environment_secrets, harness_image):
    def start(agent_fields=None, workspaces=None):
        document = spec(egress={"default": ANTHROPIC}, agents={"coder": agent(harness="opencode", image=harness_image("opencode"), **(agent_fields or {}))})
        if workspaces:
            document["workspaces"] = workspaces
        return bring_up(environment_secrets, document)

    return start


def test_claude_code_bypasses_permissions_and_has_accepted_everything(claude, engine):
    project = claude()
    settings = json.loads(read_in_agent(engine, project, "$HOME/.claude/settings.json"))
    assert settings["skipDangerousModePermissionPrompt"] is True
    # bypass comes from the flag: permissions.defaultMode in the settings makes Claude Code ask on its
    # first start whether to make auto mode the default
    assert "defaultMode" not in settings.get("permissions", {})
    cmdline = engine.exec(container(engine, project).name, "sh", "-c", "tr '\\0' ' ' < /proc/$(pgrep -n -x claude)/cmdline").stdout
    assert "--permission-mode bypassPermissions" in cmdline
    state = json.loads(read_in_agent(engine, project, "$HOME/.claude.json"))
    assert state["hasCompletedOnboarding"] is True
    env = container(engine, project).raw["Config"]["Env"]
    assert "IS_SANDBOX=1" in env


def test_claude_code_has_the_workspace_trusted_and_a_placeholder_key_approved(claude, engine):
    project = claude(workspaces={"scratch": {}}, agent_fields={"workspaces": ["scratch"]})
    state = json.loads(read_in_agent(engine, project, "$HOME/.claude.json"))
    assert state["projects"]["/workspace/scratch"]["hasTrustDialogAccepted"] is True
    assert state["customApiKeyResponses"]["approved"], "the placeholder API key must be pre-approved"
    env = dict(e.split("=", 1) for e in container(engine, project).raw["Config"]["Env"])
    assert env["ANTHROPIC_API_KEY"] and env["ANTHROPIC_API_KEY"] != project.env["ANTHROPIC_API_KEY"]


def test_claude_code_trusts_every_workspace_of_an_agent_with_several(claude, engine):
    project = claude(workspaces={"one": {}, "two": {}}, agent_fields={"workspaces": ["one", "two"]})
    state = json.loads(read_in_agent(engine, project, "$HOME/.claude.json"))
    settings = json.loads(read_in_agent(engine, project, "$HOME/.claude/settings.json"))
    for name in ("one", "two"):
        assert state["projects"][f"/workspace/{name}"]["hasTrustDialogAccepted"] is True
        assert f"/workspace/{name}" in settings["permissions"]["additionalDirectories"]


def test_claude_code_reports_its_life_cycle_to_the_control_sidecar(claude, engine):
    project = claude()
    settings = json.loads(read_in_agent(engine, project, "$HOME/.claude/settings.json"))
    for hook in ("SessionStart", "UserPromptSubmit", "Stop", "Notification"):
        commands = [h["command"] for group in settings["hooks"][hook] for h in group["hooks"]]
        assert f"egzo hook {hook}" in commands, hook


def test_claude_code_gets_the_control_tools_over_mcp(claude, engine):
    project = claude()
    state = json.loads(read_in_agent(engine, project, "$HOME/.claude.json"))
    server = state["mcpServers"]["egzo"]
    assert server["type"] == "http" and server["url"] == "http://control:7777/mcp"
    assert server["headers"]["Authorization"].startswith("Basic ")


def test_claude_code_is_told_how_to_read_an_egzo_message_header(claude, engine):
    project = claude()
    process = engine.exec(container(engine, project).name, "sh", "-c", "tr '\\0' ' ' < /proc/$(pgrep -n -x claude)/cmdline")
    assert "--append-system-prompt" in process.stdout
    instructions = read_in_agent(engine, project, "$HOME/.egzo/instructions.md")
    for needed in ("get_message", "list_messages", "resolve", "outcome", "update", "ask", "message(", "agents"):
        assert needed in instructions, f"the platform instructions do not mention {needed}"
    assert "reply" not in instructions.lower() or "tool" in instructions.lower()


def test_claude_code_model_and_prompt_come_from_the_agent_definition(claude, engine):
    project = claude(agent_fields={"model": "claude-sonnet-5-5"})
    cmdline = engine.exec(container(engine, project).name, "sh", "-c", "tr '\\0' ' ' < /proc/$(pgrep -n -x claude)/cmdline").stdout
    assert "--model claude-sonnet-5-5" in cmdline


def test_the_prompt_file_is_added_to_the_instructions(environment_secrets, harness_image, engine):
    (environment_secrets.root / "coder.md").write_text("Always answer in rhyme.\n")
    bring_up(environment_secrets, spec(egress={"default": ANTHROPIC}, agents={"coder": agent(harness="claude-code", image=harness_image("claude-code"), prompt="./coder.md")}))
    text = read_in_agent(engine, environment_secrets, "$HOME/.egzo/instructions.md")
    assert "Always answer in rhyme." in text and "get_message" in text


def test_permissions_default_keeps_the_harness_prompts(claude, engine):
    project = claude(agent_fields={"permissions": "default"})
    settings = json.loads(read_in_agent(engine, project, "$HOME/.claude/settings.json"))
    assert settings["permissions"].get("defaultMode", "default") == "default"
    assert not settings.get("skipDangerousModePermissionPrompt")
    cmdline = engine.exec(container(engine, project).name, "sh", "-c", "tr '\\0' ' ' < /proc/$(pgrep -n -x claude)/cmdline").stdout
    assert "--permission-mode" not in cmdline
    assert "IS_SANDBOX=1" not in container(engine, project).raw["Config"]["Env"]


def test_opencode_allows_every_tool_without_asking(opencode, engine):
    project = opencode()
    config = json.loads(read_in_agent(engine, project, "$HOME/.config/opencode/opencode.json"))
    assert config["permission"] in ("allow", {"*": "allow"}) or all(v == "allow" for v in config["permission"].values())


def test_opencode_permissions_default_keeps_the_prompts(opencode, engine):
    project = opencode(agent_fields={"permissions": "default"})
    config = json.loads(read_in_agent(engine, project, "$HOME/.config/opencode/opencode.json"))
    assert config.get("permission") not in ("allow", {"*": "allow"})


def test_opencode_gets_the_control_tools_over_mcp(opencode, engine):
    project = opencode()
    config = json.loads(read_in_agent(engine, project, "$HOME/.config/opencode/opencode.json"))
    server = config["mcp"]["egzo"]
    assert server["type"] == "remote" and server["url"] == "http://control:7777/mcp"
    assert server["headers"]["Authorization"].startswith("Basic ")


def test_opencode_reports_its_life_cycle_through_a_plugin(opencode, engine):
    project = opencode()
    plugin = engine.exec(container(engine, project).name, "sh", "-c", "cat $HOME/.config/opencode/plugin/egzo.js $HOME/.config/opencode/plugins/egzo.js 2>/dev/null").stdout
    assert "session.idle" in plugin
    assert "UserPromptSubmit" in plugin and "Stop" in plugin and "SessionStart" in plugin


def test_opencode_model_and_instructions_come_from_the_agent_definition(environment_secrets, harness_image, engine):
    (environment_secrets.root / "coder.md").write_text("Always answer in rhyme.\n")
    fields = {"model": "anthropic/claude-sonnet-5-5", "prompt": "./coder.md"}
    bring_up(environment_secrets, spec(egress={"default": ANTHROPIC}, agents={"coder": agent(harness="opencode", image=harness_image("opencode"), **fields)}))
    config = json.loads(read_in_agent(engine, environment_secrets, "$HOME/.config/opencode/opencode.json"))
    assert config["model"] == "anthropic/claude-sonnet-5-5"
    assert any("instructions.md" in path for path in config["instructions"])
    assert "Always answer in rhyme." in read_in_agent(engine, environment_secrets, "$HOME/.egzo/instructions.md")


# --- hooks in both directions ---------------------------------------------------------------------------


def test_the_hook_command_posts_its_payload_as_a_hook_event(live_project, engine, session_image):
    bring_up(live_project, spec(agents={"coder": agent(harness="custom", image=session_image, env={"FAKE_TUI": "emit"})}), timeout=300)
    name = container(engine, live_project).name
    import subprocess

    ran = subprocess.run(
        [engine.cli, "exec", "-i", name, "egzo", "hook", "PreToolUse"],
        input='{"tool_name":"Bash"}', text=True, capture_output=True, env={**os.environ, **engine.env},
    )
    assert ran.returncode == 0, ran.stderr
    hooks = [json.loads(l) for l in live_project.run("events").stdout.splitlines() if l.startswith("{")]
    found = [e for e in hooks if e["type"] == "hook" and e["text"] == "PreToolUse"]
    assert found and found[0]["agent"] == "coder"


def test_the_hook_command_never_blocks_or_fails_the_harness(live_project, engine, session_image):
    bring_up(live_project, spec(agents={"coder": agent(harness="custom", image=session_image, env={"FAKE_TUI": "emit"})}), timeout=300)
    name = container(engine, live_project).name
    start = time.time()
    broken = engine.exec(name, "sh", "-c", "EGZO_CONTROL_URL=http://127.0.0.1:9 egzo hook Stop </dev/null; echo exit=$?")
    assert "exit=0" in broken.stdout
    assert time.time() - start < 6


# --- the real TUIs take what egzo types -----------------------------------------------------------------


def events_of(project):
    out = project.run("events").stdout
    return [json.loads(line) for line in out.splitlines() if line.startswith("{")]


def wait_until(check, what, timeout, context=None):
    deadline = time.time() + timeout
    while time.time() < deadline:
        found = check()
        if found:
            return found
        time.sleep(1)
    raise AssertionError(f"timed out waiting for {what}" + (f"\n{context()}" if context else ""))


def test_the_real_tui_reports_idle_and_is_told_a_message_is_waiting(launched, harness):
    """No model is called: the harness reports the typed line through its hook as soon as it is submitted.
    The line must reach the real TUI, be submitted, and name the message by its id and nothing else of it."""
    project = launched(agent_fields={"inject": {"human_quiet": "1s"}})

    def activity():
        rows = [e for e in events_of(project) if e["type"] == "activity" and e["agent"] == "coder"]
        return rows[-1]["text"] if rows else None

    wait_until(lambda: activity() == "idle", "the harness to report that its session started", 45)
    sent = project.run("send", "coder", "hello from the operator, TOP-SECRET-PAYLOAD").stdout.split()[1]
    context = lambda: "\n".join(f'{e["seq"]} {e["type"]} {e.get("text", "")}' for e in events_of(project))
    wait_until(lambda: [e for e in events_of(project) if e["type"] == "announced"], "the message to be announced", 40, context=context)
    typed = wait_until(
        lambda: [e["data"]["prompt"] for e in events_of(project) if e["type"] == "hook" and e["text"] == "UserPromptSubmit"],
        "the TUI to submit the typed line", 40, context=context,
    )
    assert typed[0] == f"check egzo message {sent} and handle the request for me."
    assert "TOP-SECRET-PAYLOAD" not in json.dumps(typed)


# --- a Claude subscription token (claude setup-token) instead of an API key --------------------------------

OAUTH_TOKEN = "sk-ant-oat01-egzo-spec-not-a-real-token-" + "0" * 40


@pytest.fixture
def subscription(environment_secrets, harness_image):
    environment_secrets.env["CLAUDE_CODE_OAUTH_TOKEN"] = OAUTH_TOKEN
    vaults = {"main": {"backend": "env", "secrets": {"CLAUDE_CODE_OAUTH_TOKEN": {"from": "env:CLAUDE_CODE_OAUTH_TOKEN"}}}}
    egress = {"default": {"allow": ["platform.claude.com"], "services": {"anthropic-oauth": "main/CLAUDE_CODE_OAUTH_TOKEN"}}}
    return bring_up(environment_secrets, spec(vaults=vaults, egress=egress, agents={"coder": agent(harness="claude-code", image=harness_image("claude-code"))}))


def test_claude_code_gets_an_oauth_placeholder_when_the_profile_injects_a_bearer_token(subscription, engine):
    env = dict(e.split("=", 1) for e in container(engine, subscription).raw["Config"]["Env"])
    assert env.get("CLAUDE_CODE_OAUTH_TOKEN"), "Claude Code reads a subscription token from CLAUDE_CODE_OAUTH_TOKEN"
    assert env["CLAUDE_CODE_OAUTH_TOKEN"] != OAUTH_TOKEN
    assert "ANTHROPIC_API_KEY" not in env, "an API key placeholder would make Claude Code send x-api-key"
    assert OAUTH_TOKEN not in json.dumps(container(engine, subscription).raw["Config"])
