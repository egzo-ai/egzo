"""The whole stack, for real: the hub image, Dex as its single sign-on, the local Docker engine and a project made by the egzo CLI.

Run by `make hub-stack` (e2e/stack.sh), never by the plain `pytest`: it needs HUB_STACK_URL.
"""

import os

import pytest
from playwright.sync_api import expect

URL = os.environ.get("HUB_STACK_URL")
PROJECT = os.environ.get("HUB_STACK_PROJECT")
ADMIN_PASSWORD = os.environ.get("HUB_STACK_ADMIN_PASSWORD")

pytestmark = pytest.mark.skipif(not URL, reason="needs the stack of e2e/stack.sh (make hub-stack)")


def dex_sign_in(page, failed):
    page.wait_for_load_state()
    assert not page.url.startswith("chrome-error:"), f"the browser could not load the page the hub sent it to; failed requests: {failed}"
    # Dex: the local account of dex.yaml
    if page.get_by_role("link", name="Log in with Email").count():  # Dex shows a chooser when it has several connectors
        page.get_by_role("link", name="Log in with Email").click()
    page.get_by_placeholder("email address").fill("admin@example.com")
    page.get_by_placeholder("password").fill("password")
    page.get_by_role("button", name="Login").click()


def test_the_admin_links_dex_signs_in_with_it_and_works_with_a_real_project(page):
    failed = []
    page.on("requestfailed", lambda request: failed.append(f"{request.url}: {request.failure}"))
    # the starter account, with the password the stack set
    page.goto(URL)
    page.get_by_label("Username").fill("admin")
    page.get_by_label("Password").fill(ADMIN_PASSWORD)
    page.get_by_role("button", name="Sign in", exact=True).click()
    expect(page.get_by_test_id("signed-in-as")).to_contain_text("admin · operator")

    # link the account at Dex, sign out, and sign in with Dex: the same user
    page.get_by_test_id("signed-in-as").click()
    page.get_by_role("button", name="Link Dex").click()
    dex_sign_in(page, failed)
    expect(page.get_by_test_id("linked-as")).to_contain_text("admin@example.com")
    page.get_by_role("button", name="Sign out").click()
    page.get_by_role("link", name="Sign in with Dex").click()
    dex_sign_in(page, failed)
    expect(page.get_by_test_id("signed-in-as")).to_contain_text("admin · operator")

    # the operator holds the project the CLI made
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
