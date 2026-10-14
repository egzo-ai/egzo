"""The whole stack, for real: the hub image, Dex for the login, the local Docker engine and a project made by the egzo CLI.

Run by `make hub-stack` (e2e/stack.sh), never by the plain `pytest`: it needs HUB_STACK_URL.
"""

import os

import pytest
from playwright.sync_api import expect

URL = os.environ.get("HUB_STACK_URL")
PROJECT = os.environ.get("HUB_STACK_PROJECT")

pytestmark = pytest.mark.skipif(not URL, reason="needs the stack of e2e/stack.sh (make hub-stack)")


def test_a_person_signs_in_through_dex_and_works_with_a_real_project(page):
    page.goto(URL)
    page.get_by_role("link", name="Sign in").click()
    # Dex: the local account of dex.yaml
    if page.get_by_role("link", name="Log in with Email").count():  # Dex shows a chooser when it has several connectors
        page.get_by_role("link", name="Log in with Email").click()
    page.get_by_placeholder("email address").fill("admin@example.com")
    page.get_by_placeholder("password").fill("password")
    page.get_by_role("button", name="Login").click()
    expect(page.get_by_test_id("signed-in-as")).to_contain_text("operator")

    # the first person is the operator and owns the project the CLI made
    page.get_by_test_id("projects").get_by_role("link", name=PROJECT).click()
    expect(page.get_by_role("heading", name=PROJECT)).to_be_visible()
    expect(page.get_by_role("heading", name="No agent yet")).to_be_visible()

    page.get_by_role("button", name="Spawn agent").click()
    page.get_by_role("dialog").get_by_role("button", name="Spawn", exact=True).click()
    expect(page.get_by_role("status")).to_contain_text("Spawned coder-1")
    expect(page.get_by_test_id("activity")).not_to_have_text("stopped", timeout=15000)

    # the terminal of the real session holder
    page.get_by_role("button", name="Attach", exact=True).click()
    expect(page.get_by_test_id("terminal-state")).to_have_text("connected", timeout=15000)
    terminal = page.locator(".tpane .xterm-rows")
    expect(terminal).to_contain_text("READY", timeout=20000)
    page.locator(".tpane .xterm").click()
    page.keyboard.type("hello from the browser")
    expect(terminal).to_contain_text("hello from the browser", timeout=10000)
    page.get_by_role("button", name="Close terminal").click()

    page.get_by_role("button", name="Stop", exact=True).click()
    expect(page.get_by_test_id("activity")).to_have_text("stopped", timeout=20000)
    page.get_by_role("button", name="Remove", exact=True).click()
    page.get_by_role("dialog").get_by_role("button", name="Remove", exact=True).click()
    expect(page.get_by_role("status").last).to_contain_text("Removed coder-1")
    expect(page.get_by_role("heading", name="No agent yet")).to_be_visible()

    page.get_by_role("tab", name="Audit log").click()
    audit = page.get_by_test_id("audit")
    for action in ("agent.spawn", "agent.attach", "agent.stop", "agent.rm"):
        expect(audit).to_contain_text(action)
