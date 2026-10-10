# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""The browser every harness image carries: Chromium behind the Playwright MCP server, through the egress proxy.

A browser is a basic tool, so it is built into the stock `claude-code` and `opencode` images and registered with the
harness: no flavor of the image, no recipe. These specs drive it the way a harness does (they start the MCP server the
harness configuration names and call its tools) and let the real Chromium reach real sites through the real proxy,
with nothing configured in the client but the project's egress profile. No model is called.
"""

import json
import re
import tempfile
import time
from pathlib import Path

import pytest

from support import agent, spec

pytestmark = pytest.mark.usefixtures("engine", "internet", "httpbin")

FIXTURES = Path(__file__).parent / "fixtures"
HARNESSES = ["claude-code", "opencode"]
PASSWORD = 's3cr"t p&ss=w0rd+/%'
DEV_PAGE = "require('http').createServer((q, r) => r.end('<title>dev</title><h1>local dev page</h1>')).listen(8123)"


@pytest.fixture(params=HARNESSES)
def harness(request):
    return request.param


@pytest.fixture
def browser(live_project, harness_image):
    """Start: an agent `coder` of a harness, whose profile reaches its model API plus what the spec adds."""
    live_project.env.update(ANTHROPIC_API_KEY="sk-ant-egzo-spec-not-a-real-key-0000000000", GITHUB_TOKEN="unused", DEPLOY_TOKEN=PASSWORD)

    def start(harness, allow=(), services=None, image=None):
        profile = {"allow": ["platform.claude.com", *allow], "services": {"anthropic": "main/ANTHROPIC_API_KEY", **(services or {})}}
        template = agent(harness=harness, image=image or harness_image(harness))
        live_project.up(spec(egress={"default": profile}, agents={"coder-template": template}), timeout=900)
        live_project.spawn("coder-template", "coder", timeout=900)
        return live_project

    return start


def instance(engine, project):
    return engine.instance(project.name, "coder").name


def configured_servers(engine, project, harness):
    path = "$HOME/.claude.json" if harness == "claude-code" else "$HOME/.config/opencode/opencode.json"
    read = engine.exec(instance(engine, project), "sh", "-c", f"cat {path}")
    assert read.returncode == 0, read.stderr
    config = json.loads(read.stdout)
    return config["mcpServers"] if harness == "claude-code" else config["mcp"]


def launch_command(server):
    """What a harness runs for a stdio MCP server: its command (a string and arguments, or one list) and its environment."""
    command = server["command"]
    command = [command, *server.get("args", [])] if isinstance(command, str) else list(command)
    environment = server.get("env") or server.get("environment") or {}
    return ["env", *[f"{key}={value}" for key, value in environment.items()], *command] if environment else command


def call_tools(engine, project, harness, calls):
    """Start the `playwright` server the harness is configured with, run the calls, return what each one answered.
    With no calls, the names of the tools the server offers."""
    name = instance(engine, project)
    server = configured_servers(engine, project, harness)["playwright"]
    copied = engine.run("cp", str(FIXTURES / "mcp_client.mjs"), f"{name}:/tmp/mcp_client.mjs")
    assert copied.returncode == 0, copied.stderr
    ran = engine.exec(name, "node", "/tmp/mcp_client.mjs", json.dumps(launch_command(server)), json.dumps(calls))
    assert ran.returncode == 0, ran.stderr
    return json.loads(ran.stdout)


def navigate(url):
    return {"name": "browser_navigate", "arguments": {"url": url}}


def evaluate(function):
    return {"name": "browser_evaluate", "arguments": {"function": function}}


def audit_events(project):
    return [json.loads(line) for line in project.run("proxy", "log").stdout.splitlines() if line.startswith("{")]


def events_for(project, host):
    return [e for e in audit_events(project) if e.get("host") == host]


# --- the browser is part of the image ----------------------------------------------------------------------------


def test_the_browser_is_built_into_the_harness_image_and_registered_next_to_the_egzo_server(browser, engine, harness):
    project = browser(harness)
    for tool in ("playwright-mcp", "certutil"):
        assert engine.exec(instance(engine, project), "sh", "-c", f"command -v {tool}").returncode == 0, f"{tool} is missing"
    servers = configured_servers(engine, project, harness)
    assert "egzo" in servers and "playwright" in servers
    tools = call_tools(engine, project, harness, [])
    assert {"browser_navigate", "browser_evaluate", "browser_snapshot"} <= set(tools)


def test_an_image_without_a_browser_is_not_given_a_server_that_cannot_start(browser, engine, harness, harness_image):
    base = harness_image(harness)
    has_browser = engine.run("run", "--rm", "--entrypoint", "sh", base, "-c", "command -v playwright-mcp")
    assert has_browser.returncode == 0, "the stock image must carry the browser for this spec to take it away"
    derived = f"{base}-nobrowser"
    dockerfile = f'FROM {base}\nUSER root\nRUN rm -f "$(command -v playwright-mcp)"\n'
    with tempfile.TemporaryDirectory() as context:
        (Path(context) / "Dockerfile").write_text(dockerfile)
        built = engine.run("build", "-t", derived, context)
    assert built.returncode == 0, built.stderr[-2000:]
    project = browser(harness, image=derived)
    servers = configured_servers(engine, project, harness)
    assert "egzo" in servers and "playwright" not in servers


# --- browsing through the proxy -------------------------------------------------------------------------------------


def test_the_browser_loads_what_the_profile_allows_and_nothing_else(browser, engine, harness):
    project = browser(harness, allow=["example.com"])
    name = instance(engine, project)
    engine.exec(name, "node", "-e", DEV_PAGE, detach=True)
    for _ in range(20):  # the dev server of the agent, on the agent's own localhost
        served = engine.exec(name, "curl", "-sS", "--noproxy", "*", "http://localhost:8123/")
        if served.returncode == 0:
            break
        time.sleep(0.5)
    assert "local dev page" in served.stdout, served.stderr

    allowed, local, refused = call_tools(  # the refused one last: a failed page interrupts the next navigation
        engine, project, harness, [navigate("https://example.com/"), navigate("http://localhost:8123/"), navigate("https://example.org/")]
    )
    assert "Page Title: Example Domain" in allowed["text"]
    assert "net::ERR_" in refused["text"]
    assert "Page Title: dev" in local["text"]

    # Chromium sent Encrypted Client Hello and the proxy answered it: nothing was refused for that, nothing was
    # logged per request, and what never went through the proxy (localhost) is not in its log
    events = audit_events(project)
    assert [e for e in events_for(project, "example.com") if e["action"] == "intercept"]
    assert not [e for e in events_for(project, "example.com") if e["action"] in ("deny", "request")]
    assert [e for e in events_for(project, "example.org") if e["action"] == "deny" and "egress profile" in e["reason"]]
    assert not [e for e in events if "Encrypted Client Hello" in e.get("reason", "") and e["action"] == "deny"]
    assert events_for(project, "localhost") == []

    # the browser leaves nothing behind where the agent works: Playwright's own default is a `.playwright-mcp`
    # directory in the working directory of the server
    found = engine.exec(name, "sh", "-c", "find / \\( -path /proc -o -path /sys \\) -prune -o -name .playwright-mcp -print 2>/dev/null")
    assert found.stdout.strip() == "" and ".playwright-mcp" not in json.dumps([allowed, refused, local])


def test_with_everything_allowed_a_wildcard_service_audits_the_pages_the_browser_loads(browser, engine):
    project = browser("claude-code", services={"browse": {"hosts": ["*"], "inspect": True}})
    (page,) = call_tools(engine, project, "claude-code", [navigate("https://example.com/")])
    assert "Page Title: Example Domain" in page["text"]
    requests = [e for e in events_for(project, "example.com") if e["action"] == "request"]
    assert any(r["method"] == "GET" and r["path"] == "/" and r["status"] == 200 for r in requests)
    assert not [e for e in events_for(project, "example.com") if e["action"] in ("deny", "intercept")]


def test_a_password_typed_in_a_browser_form_is_the_placeholder_and_the_site_gets_the_secret(browser, engine):
    login = {"hosts": ["httpbin.org"], "secret": "main/DEPLOY_TOKEN", "placeholder": "SITE_PASSWORD"}
    project = browser("claude-code", services={"login": login})
    name = instance(engine, project)
    placeholder = engine.exec(name, "printenv", "SITE_PASSWORD").stdout.strip()
    assert re.fullmatch(r"egzo-ph-[0-9a-f]{32}", placeholder)
    fill = f"() => {{ document.querySelector('input[name=custname]').value = '{placeholder}'; document.querySelector('form').submit(); }}"
    results = call_tools(
        engine,
        project,
        "claude-code",
        [
            navigate("https://httpbin.org/forms/post"),
            evaluate(fill),
            {"name": "browser_wait_for", "arguments": {"text": "custname"}},  # the echoed form, once the POST has come back
            evaluate("() => JSON.parse(document.body.innerText).form.custname"),
        ],
    )
    assert json.dumps(PASSWORD)[1:-1] in results[-1]["text"], results[-1]
    assert PASSWORD not in json.dumps(engine.instance(project.name, "coder").raw)
    assert PASSWORD not in engine.exec(name, "env").stdout
