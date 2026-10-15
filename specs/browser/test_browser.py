# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""The browser every harness image carries: Chromium behind the Playwright MCP server, through the egress proxy.

A browser is a basic tool, so it is built into the stock `claude-code` and `opencode` images and registered with the harness: no flavor of
the image, no recipe. These specs drive it the way a harness does (they start the MCP server the harness configuration names and call its
tools) and let the real Chromium reach real sites through the real proxy, with nothing configured in the client but the project's egress
profile. No model is called. They need example.com and httpbin.org, and fail when those cannot be reached.

One project is up for the whole module, with the harnesses, their copies without a browser, and the profiles these specs browse with."""

import json
import re
import tempfile
import time
from pathlib import Path

import pytest

from support import agent, spec

pytestmark = pytest.mark.usefixtures("internet", "httpbin")

FIXTURES = Path(__file__).parent.parent / "fixtures"
HARNESSES = ["claude-code", "opencode"]
PASSWORD = 's3cr"t p&ss=w0rd+/%'
DEV_PAGE = "require('http').createServer((q, r) => r.end('<title>dev</title><h1>local dev page</h1>')).listen(8123)"
CONFIG_FILE = {"claude-code": "$HOME/.claude.json", "opencode": "$HOME/.config/opencode/opencode.json"}
USERS_OWN = {
    "claude-code": {"type": "stdio", "command": "npx", "args": ["@playwright/mcp@latest", "--headless"], "env": {"A": "1"}},
    "opencode": {"type": "local", "command": ["npx", "@playwright/mcp"], "enabled": False},
}
MODEL_API = {"allow": ["platform.claude.com"], "services": {"anthropic": "main/ANTHROPIC_API_KEY"}}


def without_browser(engine, base):
    """A copy of a stock harness image from which the browser server is taken away."""
    has_browser = engine.run("run", "--rm", "--entrypoint", "sh", base, "-c", "command -v playwright-mcp")
    assert has_browser.returncode == 0, "the stock image must carry the browser for this spec to take it away"
    derived = f"{base}-nobrowser"
    with tempfile.TemporaryDirectory() as context:
        (Path(context) / "Dockerfile").write_text(f'FROM {base}\nUSER root\nRUN rm -f "$(command -v playwright-mcp)"\n')
        built = engine.run("build", "-t", derived, context)
    assert built.returncode == 0, built.stderr[-2000:]
    return derived


@pytest.fixture(scope="module")
def sandbox(shared_project, engine, egzo):
    p = shared_project
    p.env.update(ANTHROPIC_API_KEY="sk-ant-egzo-spec-not-a-real-key-0000000000", DEPLOY_TOKEN=PASSWORD)
    images = {name: engine.harness_image(name, egzo.binary) for name in HARNESSES}
    agents = {name: agent(harness=name, image=image, egress="example") for name, image in images.items()}
    agents |= {f"{name}-bare": agent(harness=name, image=without_browser(engine, image), egress="example") for name, image in images.items()}
    agents["wild"] = agent(harness="claude-code", image=images["claude-code"], egress="wild")
    agents["login"] = agent(harness="claude-code", image=images["claude-code"], egress="login")
    p.up(
        spec(
            vaults={"main": {"backend": "env", "secrets": ["ANTHROPIC_API_KEY", "DEPLOY_TOKEN"]}},
            egress={
                "example": {**MODEL_API, "allow": [*MODEL_API["allow"], "example.com"]},
                "wild": {**MODEL_API, "services": {**MODEL_API["services"], "browse": {"hosts": ["*"], "inspect": True}}},
                "login": {**MODEL_API, "services": {**MODEL_API["services"], "login": {"hosts": ["httpbin.org"], "secret": "main/DEPLOY_TOKEN", "placeholder": "SITE_PASSWORD"}}},
            },
            agents=agents,
        ),
        timeout=900,
    )
    return p


@pytest.fixture(autouse=True)
def start_from_no_instance(sandbox):
    sandbox.retire()
    yield


def servers_of(engine, project, name, harness):
    read = engine.exec(engine.instance(project.name, name).name, "sh", "-c", f"cat {CONFIG_FILE[harness]}")
    assert read.returncode == 0, read.stderr
    config = json.loads(read.stdout)
    return config["mcpServers"] if harness == "claude-code" else config["mcp"]


def launch_command(server):
    """What a harness runs for a stdio MCP server: its command (a string and arguments, or one list) and its environment."""
    command = server["command"]
    command = [command, *server.get("args", [])] if isinstance(command, str) else list(command)
    environment = server.get("env") or server.get("environment") or {}
    return ["env", *[f"{key}={value}" for key, value in environment.items()], *command] if environment else command


def call_tools(engine, project, name, harness, calls):
    """Start the `playwright` server the harness is configured with, run the calls, return what each one answered.
    With no calls, the names of the tools the server offers."""
    container = engine.instance(project.name, name).name
    server = servers_of(engine, project, name, harness)["playwright"]
    copied = engine.run("cp", str(FIXTURES / "mcp_client.mjs"), f"{container}:/tmp/mcp_client.mjs")
    assert copied.returncode == 0, copied.stderr
    ran = engine.exec(container, "node", "/tmp/mcp_client.mjs", json.dumps(launch_command(server)), json.dumps(calls))
    assert ran.returncode == 0, ran.stderr
    return json.loads(ran.stdout)


def navigate(url):
    return {"name": "browser_navigate", "arguments": {"url": url}}


def evaluate(function):
    return {"name": "browser_evaluate", "arguments": {"function": function}}


def audit(project, host, agent_name):
    events = [json.loads(line) for line in project.ok("proxy log --agent", agent_name).stdout.splitlines() if line.startswith("{")]
    return [e for e in events if e.get("host") == host]


def set_server(engine, project, name, harness, entry):
    key = "mcpServers" if harness == "claude-code" else "mcp"
    script = (
        "const fs=require('fs');const p=process.argv[1].replace('$HOME',process.env.HOME);const c=JSON.parse(fs.readFileSync(p));"
        f"c.{key}[process.argv[2]]=JSON.parse(process.argv[3]);fs.writeFileSync(p,JSON.stringify(c))"
    )
    done = engine.exec(engine.instance(project.name, name).name, "node", "-e", script, CONFIG_FILE[harness], "playwright", json.dumps(entry))
    assert done.returncode == 0, done.stderr


def restarted(engine, project, name):
    """Restart the instance and wait until its harness is running again, so its configuration has been written."""
    project.ok("restart", name)
    for _ in range(60):
        if engine.exec(engine.instance(project.name, name).name, "sh", "-c", "pgrep -x claude || pgrep -x opencode").returncode == 0:
            return
        time.sleep(0.5)
    pytest.fail("the harness did not come back")


@pytest.mark.parametrize("harness", HARNESSES)
def test_the_browser_is_part_of_the_image_registered_with_the_harness_and_browses_only_what_the_profile_allows(sandbox, engine, harness):
    p = sandbox
    name = p.spawn(harness, timeout=900)
    container = engine.instance(p.name, name).name
    for tool in ("playwright-mcp", "certutil"):
        assert engine.exec(container, "sh", "-c", f"command -v {tool}").returncode == 0, f"{tool} is missing"
    servers = servers_of(engine, p, name, harness)
    assert "egzo" in servers and "playwright" in servers
    assert {"browser_navigate", "browser_evaluate", "browser_snapshot"} <= set(call_tools(engine, p, name, harness, []))

    # browsing: what the profile allows and nothing else; the agent's own localhost never goes through the proxy
    engine.exec(container, "node", "-e", DEV_PAGE, detach=True)
    for _ in range(20):
        served = engine.exec(container, "curl", "-sS", "--noproxy", "*", "http://localhost:8123/")
        if served.returncode == 0:
            break
        time.sleep(0.5)
    assert "local dev page" in served.stdout, served.stderr
    allowed, local, refused = call_tools(  # the refused one last: a failed page interrupts the next navigation
        engine, p, name, harness, [navigate("https://example.com/"), navigate("http://localhost:8123/"), navigate("https://example.org/")]
    )
    assert "Page Title: Example Domain" in allowed["text"] and "Page Title: dev" in local["text"] and "net::ERR_" in refused["text"]
    # Chromium sent Encrypted Client Hello and the proxy answered it: nothing was refused for that, nothing was logged per request, and what
    # never went through the proxy (localhost) is not in its log
    assert [e for e in audit(p, "example.com", name) if e["action"] == "intercept"]
    assert not [e for e in audit(p, "example.com", name) if e["action"] in ("deny", "request")]
    assert [e for e in audit(p, "example.org", name) if e["action"] == "deny" and "egress profile" in e["reason"]]
    assert audit(p, "localhost", name) == []
    found = engine.exec(container, "sh", "-c", "find / \\( -path /proc -o -path /sys \\) -prune -o -name .playwright-mcp -print 2>/dev/null")
    assert found.stdout.strip() == "" and ".playwright-mcp" not in json.dumps([allowed, refused, local]), "the browser leaves nothing where the agent works"

    # an entry egzo wrote earlier, with arguments that no longer are right: replaced as a whole, not merged into
    registered = servers["playwright"]
    stale = json.loads(json.dumps(registered))
    stale["args" if harness == "claude-code" else "command"] += ["--stale"]
    set_server(engine, p, name, harness, stale)
    restarted(engine, p, name)
    assert servers_of(engine, p, name, harness)["playwright"] == registered
    # a server of the user's with that name is not egzo's to change, and it stays whatever the image carries
    set_server(engine, p, name, harness, USERS_OWN[harness])
    restarted(engine, p, name)
    assert servers_of(engine, p, name, harness)["playwright"] == USERS_OWN[harness] and "egzo" in servers_of(engine, p, name, harness)

    # an image without a browser is not given a server that cannot start
    bare = p.spawn(f"{harness}-bare", timeout=900)
    bare_servers = servers_of(engine, p, bare, harness)
    assert "egzo" in bare_servers and "playwright" not in bare_servers


def test_with_everything_audited_the_pages_the_browser_loads_are_logged_and_a_password_typed_in_a_form_is_the_placeholder(sandbox, engine):
    p = sandbox
    wild = p.spawn("wild", timeout=900)
    (page,) = call_tools(engine, p, wild, "claude-code", [navigate("https://example.com/")])
    assert "Page Title: Example Domain" in page["text"]
    requests = [e for e in audit(p, "example.com", wild) if e["action"] == "request"]
    assert any(r["method"] == "GET" and r["path"] == "/" and r["status"] == 200 for r in requests)
    assert not [e for e in audit(p, "example.com", wild) if e["action"] in ("deny", "intercept")]

    login = p.spawn("login", timeout=900)
    container = engine.instance(p.name, login).name
    placeholder = engine.exec(container, "printenv", "SITE_PASSWORD").stdout.strip()
    assert re.fullmatch(r"egzo-ph-[0-9a-f]{32}", placeholder)
    fill = f"() => {{ document.querySelector('input[name=custname]').value = '{placeholder}'; document.querySelector('form').submit(); }}"
    results = call_tools(
        engine, p, login, "claude-code",
        [
            navigate("https://httpbin.org/forms/post"),
            evaluate(fill),
            {"name": "browser_wait_for", "arguments": {"text": "custname"}},  # the echoed form, once the POST has come back
            evaluate("() => JSON.parse(document.body.innerText).form.custname"),
        ],
    )
    assert json.dumps(PASSWORD)[1:-1] in results[-1]["text"], results[-1]
    assert PASSWORD not in json.dumps(engine.instance(p.name, login).raw) and PASSWORD not in engine.exec(container, "env").stdout
