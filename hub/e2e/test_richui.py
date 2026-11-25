"""The Rich UI (issue #227): the chat of an agent spawned in AHP mode, next to its terminal.

The project's side is the test server's scripted AHP server (server/ahp.go): it plays turns as opencode-ahp does, behind the link's real
filter (internal/ahp), so the page sees what a link lets through and the server hears only what the filter passed (`ahp-heard`)."""

import json
import re

from playwright.sync_api import expect

from conftest import sign_in
from test_hub import spawn


def heard(hub, agent):
    return [json.loads(line) if isinstance(line, str) else line for line in hub.world("ahp-heard", project="web", agent=agent)]


def dispatched(hub, agent):
    """The actions the page dispatched to the agent's chat, in order."""
    return [m["params"]["action"] for m in heard(hub, agent) if m.get("method") == "dispatchAction"]


def chat(page):
    return page.get_by_test_id("rich-chat")


def open_chat(page, hub, agent):
    page.goto(hub.url + hub.p("web") + f"/a/{agent}")
    expect(chat(page)).to_be_visible()


def test_the_spawn_form_offers_the_rich_ui_only_for_a_template_that_has_it_and_the_agent_opens_on_its_chat(single_hub, page):
    hub = single_hub
    sign_in(page, hub, "admin")
    hub.world("rich-ui", project="web", text="coder")
    page.goto(hub.url + hub.p("web"))
    page.get_by_role("button", name="Spawn agent").click()
    dialog = page.get_by_role("dialog")
    dialog.get_by_label("Template").select_option("reviewer")
    expect(dialog.get_by_test_id("rich-ui")).to_have_count(0)
    dialog.get_by_label("Template").select_option("coder")
    dialog.get_by_test_id("rich-ui").check()
    expect(dialog.get_by_text("Its terminal stays: open either one.")).to_be_visible()
    dialog.get_by_label("Name").fill("rich")
    dialog.get_by_role("button", name="Spawn", exact=True).click()
    expect(page.get_by_role("heading", name="rich")).to_be_visible()
    assert hub.spawns()[-1]["AHP"] is True, hub.spawns()[-1]

    # the agent opens on its chat, not on a terminal
    hub.world("rich-agent", project="web", agent="rich")
    page.reload()
    expect(page.get_by_test_id("chat-tab")).to_have_attribute("aria-selected", "true")
    expect(chat(page).get_by_text("It is a recursive descent parser.")).to_be_visible()
    expect(page.locator(".tpane")).to_have_count(0)
    # and its terminal is still there
    expect(page.get_by_role("button", name="Attach", exact=True)).to_be_visible()


def test_a_spawn_without_the_rich_ui_asks_for_none_and_its_agent_has_no_chat(single_hub, page):
    hub = single_hub
    sign_in(page, hub, "admin")
    hub.world("rich-ui", project="web", text="coder")
    page.goto(hub.url + hub.p("web"))
    spawn(page, "plain", template="coder")
    assert not hub.spawns()[-1].get("AHP"), hub.spawns()[-1]
    page.goto(hub.url + hub.p("web") + "/a/coder-1")
    expect(page.get_by_role("heading", name="coder-1")).to_be_visible()
    expect(page.get_by_test_id("chat-tab")).to_have_count(0)


def test_a_prompt_starts_a_turn_that_streams_its_thinking_a_tool_call_and_its_answer(single_hub, page):
    hub = single_hub
    sign_in(page, hub, "admin")
    hub.world("rich-agent", project="web", agent="chatty")
    open_chat(page, hub, "chatty")
    expect(chat(page).get_by_text("Explain the parser")).to_be_visible()  # the history comes first

    box = chat(page).get_by_label("Prompt")
    box.fill("Read the README")
    box.press("Enter")
    turn = chat(page).locator("article.turn").last
    expect(turn.locator(".prompt")).to_have_text("Read the README")
    expect(turn.locator(".tool")).to_contain_text("Read: README.md")
    expect(turn.locator(".answer")).to_have_text("The README says how to build the project.")
    expect(turn).to_have_attribute("data-state", "complete")
    turn.get_by_text("Thinking").click()
    expect(turn.locator(".reasoning")).to_contain_text("Looking at the repository first.")
    expect(box).to_have_value("")

    started = [a for a in dispatched(hub, "chatty") if a["type"] == "chat/turnStarted"]
    assert started and started[-1]["message"]["text"] == "Read the README", started


def test_stop_cancels_the_turn_that_runs(single_hub, page):
    hub = single_hub
    sign_in(page, hub, "admin")
    hub.world("rich-agent", project="web", agent="slow")
    open_chat(page, hub, "slow")
    box = chat(page).get_by_label("Prompt")
    box.fill("wait for the build")
    box.press("Enter")
    turn = chat(page).locator("article.turn").last
    expect(turn.locator(".tool")).to_contain_text("Read: README.md")
    expect(turn.get_by_text("Working…")).to_be_visible()
    chat(page).get_by_role("button", name="Stop").click()
    expect(turn).to_have_attribute("data-state", "cancelled")
    expect(turn.get_by_text("Stopped.")).to_be_visible()
    expect(turn.locator(".tool")).to_have_attribute("data-status", "cancelled")
    expect(chat(page).get_by_role("button", name="Stop")).to_have_count(0)
    assert [a["type"] for a in dispatched(hub, "slow")][-1] == "chat/turnCancelled"


def test_a_turn_that_fails_says_why(single_hub, page):
    hub = single_hub
    sign_in(page, hub, "admin")
    hub.world("rich-agent", project="web", agent="fails")
    open_chat(page, hub, "fails")
    box = chat(page).get_by_label("Prompt")
    box.fill("please fail")
    box.press("Enter")
    turn = chat(page).locator("article.turn").last
    expect(turn).to_have_attribute("data-state", "error")
    expect(turn.get_by_role("alert")).to_have_text("the model refused the request")


def test_what_is_typed_in_the_terminal_shows_in_the_chat(single_hub, page):
    hub = single_hub
    sign_in(page, hub, "admin")
    hub.world("rich-agent", project="web", agent="both")
    open_chat(page, hub, "both")
    expect(chat(page).get_by_text("Explain the parser")).to_be_visible()
    hub.world("ahp-say", project="web", agent="both", text="tidy up the tests")
    turn = chat(page).locator("article.turn").last
    expect(turn.locator(".prompt")).to_have_text("tidy up the tests")
    expect(turn).to_have_attribute("data-state", "complete")


def test_a_person_who_may_only_watch_sees_the_chat_live_and_cannot_prompt(hub, page, new_person):
    sign_in(page, hub, "admin")
    hub.world("rich-agent", project="web", agent="shared")
    bob = new_person("bob")
    hub.world("grant", project="web", user="bob", role="user")  # a user may watch the operator's agents, not operate them
    open_chat(page, hub, "shared")
    box = chat(page).get_by_label("Prompt")
    box.fill("wait while bob looks")
    box.press("Enter")
    expect(chat(page).get_by_text("Working…")).to_be_visible()

    open_chat(bob, hub, "shared")
    expect(bob.get_by_test_id("chat-readonly")).to_be_visible()
    expect(chat(bob).get_by_label("Prompt")).to_have_count(0)
    expect(chat(bob).get_by_role("button", name="Stop")).to_have_count(0)
    # the turn that runs is in the snapshot bob gets, and its end reaches him live
    turn = chat(bob).locator("article.turn").last
    expect(turn.locator(".prompt")).to_have_text("wait while bob looks")
    chat(page).get_by_role("button", name="Stop").click()
    expect(turn).to_have_attribute("data-state", "cancelled")
    # the link was told bob only watches: it would refuse whatever he dispatched
    assert any(re.fullmatch(r"ahp shared read-only=true", c) for c in hub.calls()), hub.calls()


def test_the_chat_fits_a_phone(single_hub, page):
    hub = single_hub
    page.set_viewport_size({"width": 390, "height": 844})
    sign_in(page, hub, "admin")
    hub.world("rich-agent", project="web", agent="pocket")
    open_chat(page, hub, "pocket")
    box = chat(page).get_by_label("Prompt")
    box.fill("Read the README, and tell me what it says about building the project on a machine that has no network at all")
    box.press("Enter")
    expect(chat(page).locator("article.turn").last).to_have_attribute("data-state", "complete")
    expect(box).to_be_in_viewport()
    expect(chat(page).get_by_role("button", name="Send")).to_be_in_viewport()
    assert page.evaluate("document.documentElement.scrollWidth <= window.innerWidth"), "the page scrolls sideways on a phone"


def test_a_prompt_works_on_a_hub_served_over_plain_http(single_hub, page):
    # A hub on a private network is often plain http at a LAN address, which is not a secure context: crypto.randomUUID is not there.
    # The test server is on 127.0.0.1, which is one, so the page is made to look like the other.
    hub = single_hub
    page.add_init_script("delete Crypto.prototype.randomUUID")
    sign_in(page, hub, "admin")
    hub.world("rich-agent", project="web", agent="lan")
    open_chat(page, hub, "lan")
    assert page.evaluate("typeof crypto.randomUUID") == "undefined"
    box = chat(page).get_by_label("Prompt")
    box.fill("Read the README")
    box.press("Enter")
    turn = chat(page).locator("article.turn").last
    expect(turn.locator(".prompt")).to_have_text("Read the README")
    expect(turn).to_have_attribute("data-state", "complete")
    assert [a["type"] for a in dispatched(hub, "lan")] == ["chat/turnStarted"]


def test_the_chat_waits_for_the_harness_to_know_the_main_session(single_hub, page):
    # opencode-ahp registers a session the TUI made 250 ms after it was made, and egzo may name it the main one before that: a subscribe
    # then is refused, and the chat tries again rather than staying empty
    hub = single_hub
    sign_in(page, hub, "admin")
    hub.world("rich-agent", project="web", agent="early", state="late")
    open_chat(page, hub, "early")
    expect(chat(page).get_by_text("It is a recursive descent parser.")).to_be_visible(timeout=10000)
    expect(chat(page).get_by_role("alert")).to_have_count(0)
    subscribes = [m for m in heard(hub, "early") if m.get("method") == "subscribe"]
    assert len(subscribes) == 3, subscribes
