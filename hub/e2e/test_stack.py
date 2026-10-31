"""The whole stack, for real: the hub image, Dex as its single sign-on, and a project made by the egzo CLI that links itself to the hub.

Run by `make hub-stack` (e2e/stack.sh), never by the plain `pytest`: it needs HUB_STACK_URL.
"""

import os

import pytest
from playwright.sync_api import expect

URL = os.environ.get("HUB_STACK_URL")
PROJECT = os.environ.get("HUB_STACK_PROJECT")
CODE = os.environ.get("HUB_STACK_CODE")
ADMIN_PASSWORD = os.environ.get("HUB_STACK_ADMIN_PASSWORD")

pytestmark = pytest.mark.skipif(not URL, reason="needs the stack of e2e/stack.sh (make hub-stack)")


def sign_in(page):
    """The starter account, with the password the stack set."""
    page.goto(URL)
    page.get_by_label("Username").fill("admin")
    page.get_by_label("Password").fill(ADMIN_PASSWORD)
    page.get_by_role("button", name="Sign in", exact=True).click()
    expect(page.get_by_test_id("signed-in-as")).to_contain_text("admin · operator")


def dex_sign_in(page, failed):
    page.wait_for_load_state()
    assert not page.url.startswith("chrome-error:"), f"the browser could not load the page the hub sent it to; failed requests: {failed}"
    # Dex: the local account of dex.yaml
    if page.get_by_role("link", name="Log in with Email").count():  # Dex shows a chooser when it has several connectors
        page.get_by_role("link", name="Log in with Email").click()
    page.get_by_placeholder("email address").fill("admin@example.com")
    page.get_by_placeholder("password").fill("password")
    page.get_by_role("button", name="Login").click()


def test_the_admin_approves_a_real_project_links_dex_and_works_with_the_project(page):
    failed = []
    page.on("requestfailed", lambda request: failed.append(f"{request.url}: {request.failure}"))
    sign_in(page)
    expect(page.get_by_text("No project has linked to this hub yet")).to_be_visible()

    # the address the project's first `up` printed: what it says of itself, and the operator approves it
    page.goto(URL + "/link/" + CODE)
    details = page.get_by_test_id("registration")
    expect(details).to_contain_text(PROJECT)
    expect(details).to_contain_text("coder")
    page.get_by_role("button", name="Approve").click()
    expect(page.get_by_role("heading", name=PROJECT)).to_be_visible()
    page.goto(URL)

    # link the account at Dex, sign out, and sign in with Dex: the same user
    page.get_by_test_id("signed-in-as").click()
    page.get_by_role("button", name="Link Dex").click()
    dex_sign_in(page, failed)
    expect(page.get_by_test_id("linked-as")).to_contain_text("admin@example.com")
    page.get_by_role("button", name="Sign out").click()
    page.get_by_role("link", name="Sign in with Dex").click()
    dex_sign_in(page, failed)
    expect(page.get_by_test_id("signed-in-as")).to_contain_text("admin · operator")

    # the operator holds the project that linked
    page.get_by_test_id("projects").get_by_role("link", name=PROJECT).click()
    expect(page.get_by_role("heading", name=PROJECT)).to_be_visible()
    expect(page.get_by_role("heading", name="No agent yet")).to_be_visible()

    page.get_by_role("button", name="Spawn agent").click()
    agent = page.get_by_role("dialog").get_by_label("Name").input_value()  # the name it suggests
    page.get_by_role("dialog").get_by_role("button", name="Spawn", exact=True).click()
    # the hub answers at once and runs the spawn: the agent's page shows it while the link makes it on the real engine (how fast is the engine's)
    expect(page.get_by_role("heading", name=agent)).to_be_visible()

    # once made, the terminal of the real session holder opens by itself
    expect(page.get_by_test_id("terminal-state")).to_have_text("connected", timeout=120000)
    terminal = page.locator(".tpane .xterm-rows")
    expect(terminal).to_contain_text("READY", timeout=20000)
    page.locator(".tpane .xterm").click()
    page.keyboard.type("hello from the browser")
    expect(terminal).to_contain_text("hello from the browser", timeout=10000)
    page.get_by_role("button", name="Close terminal").click()
    expect(page.get_by_test_id("activity")).not_to_have_text("stopped")

    page.get_by_role("button", name="Stop", exact=True).click()
    expect(page.get_by_test_id("activity")).to_have_text("stopped", timeout=20000)
    page.get_by_role("button", name="Remove", exact=True).click()
    page.get_by_role("dialog").get_by_role("button", name="Remove", exact=True).click()
    expect(page.get_by_role("heading", name="No agent yet")).to_be_visible(timeout=60000)
    page.get_by_role("tab", name="Activity").click()
    for verb in ("remove", "stop", "spawn"):
        expect(page.get_by_test_id("activity")).to_contain_text(verb)

    page.get_by_role("tab", name="Audit log").click()
    audit = page.get_by_test_id("audit")
    for action in ("agent.spawn", "agent.attach", "agent.stop", "agent.rm"):
        expect(audit).to_contain_text(action)


def test_a_project_whose_link_stopped_is_offline_since_and_can_only_be_looked_at(page):
    sign_in(page)
    row = page.get_by_test_id("projects").locator(f"tr[data-project={PROJECT}]")
    expect(row).to_contain_text("offline", timeout=60000)  # the hub notices at the next keep-alive at the latest
    row.get_by_role("link", name=PROJECT).click()
    expect(page.get_by_test_id("offline")).to_contain_text("Offline since")
    expect(page.get_by_role("button", name="Spawn agent")).to_have_count(0)


def test_a_project_that_came_back_is_the_same_project(page):
    sign_in(page)
    row = page.get_by_test_id("projects").locator(f"tr[data-project={PROJECT}]")
    expect(row).to_have_count(1)
    expect(row).not_to_contain_text("offline", timeout=30000)
    row.get_by_role("link", name=PROJECT).click()
    expect(page.get_by_role("button", name="Spawn agent")).to_be_enabled(timeout=30000)
    page.get_by_role("tab", name="Activity").click()
    expect(page.get_by_test_id("activity")).to_contain_text("spawn")  # what it did before is still its history


def test_a_project_that_unlinked_is_forgotten(page):
    sign_in(page)
    expect(page.get_by_text("No project has linked to this hub yet")).to_be_visible(timeout=10000)
    page.goto(URL + "/settings/links")
    expect(page.get_by_test_id("links")).to_contain_text("No project has dialled this hub yet")
