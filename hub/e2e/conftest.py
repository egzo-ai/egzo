"""Browser tests of the hub.

Each test gets its own hub (server/main.go: the real hub code, fake projects linked to it and a test login provider), started on free
ports, and a browser from pytest-playwright. `world(op, ...)` changes what the projects hold while the page is open.
"""

import json
import os
import subprocess
from pathlib import Path

import pytest
import requests
from playwright.sync_api import expect

HERE = Path(__file__).parent
SERVER = HERE / "server" / "egzo-hub-e2e"


class Hub:
    def __init__(self, process, addresses):
        self.process = process
        self.url = addresses["hub"]
        self.control_url = addresses["control"]

    def world(self, op, **fields):
        response = requests.post(f"{self.control_url}/control", json={"op": op, **fields}, timeout=10)
        response.raise_for_status()
        return response.json() if response.content else None

    def calls(self, project="web", machine="local"):
        return self.world("calls", project=project, machine=machine)

    def p(self, project, machine="local"):
        """The address of a project: it goes by the id the hub gave it."""
        return "/p/" + self.world("id", project=project, machine=machine)


def start(scenario):
    if not SERVER.exists():
        raise RuntimeError(f"build the test server first: cd hub && go build -o e2e/server/egzo-hub-e2e ./e2e/server ({SERVER})")
    process = subprocess.Popen([str(SERVER), "-scenario", scenario], stdout=subprocess.PIPE, text=True)
    line = process.stdout.readline()
    if not line:
        process.kill()
        raise RuntimeError("the test server did not start")
    return Hub(process, json.loads(line))


@pytest.fixture
def hub():
    server = start("multi")
    yield server
    server.process.kill()
    server.process.wait()


@pytest.fixture
def single_hub():
    server = start("single")
    yield server
    server.process.kill()
    server.process.wait()


@pytest.fixture
def browser_context_args(browser_context_args):
    return {**browser_context_args, "viewport": {"width": 1100, "height": 800}}


# The WebGL renderer draws to a canvas; the tests read the terminal's text from the DOM renderer's rows.
DOM_RENDERER = 'try { localStorage.setItem("egzo.renderer", "dom") } catch (e) {}'


@pytest.fixture(autouse=True)
def dom_terminal_renderer(context):
    context.add_init_script(DOM_RENDERER)


ADMIN_PASSWORD = "e2e-admin-password"  # what the test server sets for the starter account


def sign_in(page, server, name):
    """Sign in as `name`: "admin" with the starter account's password, anybody else through the test provider, with an invite of the operator's:
    nobody gets an account without one, and one who has an account just accepts it."""
    if name == "admin":
        page.goto(server.url + "/login")
        page.get_by_label("Username").fill("admin")
        page.get_by_label("Password").fill(ADMIN_PASSWORD)
        page.get_by_role("button", name="Sign in", exact=True).click()
    else:
        page.goto(server.url + "/invite#" + server.world("invite")["code"])
        page.get_by_role("button", name="Continue with Test login").click()
        page.get_by_label("Who are you?").fill(name)
        page.get_by_role("button", name="Sign in").click()
    expect(page.get_by_test_id("signed-in-as")).to_contain_text(name)


@pytest.fixture
def new_person(browser, hub):
    """Another browser, with its own cookies, signed in as `name`."""
    contexts = []

    def make(name, server=None):
        context = browser.new_context(viewport={"width": 1100, "height": 800})
        contexts.append(context)
        context.add_init_script(DOM_RENDERER)
        page = context.new_page()
        sign_in(page, server or hub, name)
        return page

    yield make
    for context in contexts:
        context.close()
