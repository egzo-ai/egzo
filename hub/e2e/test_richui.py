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


def test_a_prompt_starts_a_turn_that_streams_its_thinking_its_tools_and_its_answer_in_markdown(single_hub, page):
    hub = single_hub
    sign_in(page, hub, "admin")
    hub.world("rich-agent", project="web", agent="chatty")
    open_chat(page, hub, "chatty")
    # the history comes first, with the session's title, the model and what each turn cost
    expect(chat(page).get_by_test_id("chat-title")).to_have_text("Explain the parser")
    first = chat(page).locator("article.turn").first
    expect(first.locator("strong")).to_have_text("recursive descent")
    expect(first.get_by_test_id("turn-meta")).to_contain_text("glm-5.3-flash")
    expect(first.get_by_test_id("turn-meta")).to_contain_text("5.4k in · 120 out")
    expect(chat(page).get_by_test_id("chat-status")).to_have_text("ready")

    box = chat(page).get_by_label("Prompt")
    box.fill("Read the README")
    box.press("Enter")
    expect(box).to_have_value("")
    turn = chat(page).locator("article.turn").last
    expect(turn.locator(".prompt")).to_have_text("Read the README")
    expect(chat(page).get_by_test_id("chat-status")).to_have_text("working")
    expect(turn).to_have_attribute("data-state", "complete")
    # the answer is Markdown: bold, and a code block that can be copied
    answer = turn.locator(".answer")
    expect(answer.locator("strong")).to_have_text("build")
    expect(answer.locator("pre code")).to_have_text("make\n")
    expect(answer.get_by_role("button", name="Copy the code")).to_have_count(1)
    # the tool call says what it did, and opens on what it was called with and what it gave back
    tool = turn.get_by_test_id("tool")
    expect(tool).to_have_attribute("data-status", "done")
    expect(tool.locator("summary")).to_contain_text("Read file")
    expect(tool.locator("summary")).to_contain_text("README.md")
    expect(tool.get_by_test_id("tool-output")).not_to_be_visible()
    tool.locator("summary").click()
    expect(tool.get_by_test_id("tool-input")).to_contain_text('"filePath": "README.md"')
    expect(tool.get_by_test_id("tool-output")).to_contain_text("Build with `make`.")
    # thinking folds away once the turn is done
    turn.get_by_text("Thought").click()
    expect(turn.locator(".reasoning")).to_contain_text("Looking at the repository first.")
    expect(turn.get_by_test_id("turn-meta")).to_contain_text("2s")
    expect(turn.get_by_test_id("turn-meta")).to_contain_text("12k in · 310 out")
    # the session got a title from its first turn
    expect(chat(page).get_by_test_id("chat-title")).to_have_text("Build the project")

    started = [a for a in dispatched(hub, "chatty") if a["type"] == "chat/turnStarted"]
    assert started and started[-1]["message"]["text"] == "Read the README", started


def test_a_shell_command_shows_what_it_ran_its_output_and_how_it_failed(single_hub, page):
    hub = single_hub
    sign_in(page, hub, "admin")
    hub.world("rich-agent", project="web", agent="tester")
    open_chat(page, hub, "tester")
    box = chat(page).get_by_label("Prompt")
    box.fill("run the tests")
    box.press("Enter")
    turn = chat(page).locator("article.turn").last
    expect(turn).to_have_attribute("data-state", "complete")
    shell = turn.get_by_test_id("tool").first
    expect(shell).to_have_attribute("data-status", "failed")
    expect(shell.locator("summary code")).to_have_text("go test ./...")
    shell.locator("summary").click()
    expect(shell.get_by_test_id("tool-command")).to_have_text("$ go test ./...")
    expect(shell.get_by_test_id("tool-output")).to_contain_text("FAIL\tegzo/lexer")
    expect(shell.get_by_test_id("tool-error")).to_have_text("exit status 1")


def test_stop_cancels_the_turn_that_runs(single_hub, page):
    hub = single_hub
    sign_in(page, hub, "admin")
    hub.world("rich-agent", project="web", agent="slow")
    open_chat(page, hub, "slow")
    box = chat(page).get_by_label("Prompt")
    box.fill("wait for the build")
    box.press("Enter")
    turn = chat(page).locator("article.turn").last
    expect(turn.get_by_test_id("tool")).to_contain_text("README.md")
    expect(turn.get_by_text("Working…")).to_be_visible()
    chat(page).get_by_role("button", name="Stop").click()
    expect(turn).to_have_attribute("data-state", "cancelled")
    expect(turn.get_by_test_id("turn-meta")).to_contain_text("Stopped")
    expect(turn.get_by_test_id("tool")).to_have_attribute("data-status", "cancelled")
    expect(chat(page).get_by_role("button", name="Stop")).to_have_count(0)
    assert [a["type"] for a in dispatched(hub, "slow")][-1] == "chat/turnCancelled"


def test_while_a_turn_runs_a_message_steers_it_or_waits_for_the_next_one(single_hub, page):
    hub = single_hub
    sign_in(page, hub, "admin")
    hub.world("rich-agent", project="web", agent="steer")
    open_chat(page, hub, "steer")
    box = chat(page).get_by_label("Prompt")
    box.fill("wait for the build")
    box.press("Enter")
    expect(chat(page).get_by_role("button", name="Steer")).to_be_visible()
    # Enter steers the running turn
    box.fill("use the v2 API")
    box.press("Enter")
    waiting = chat(page).get_by_test_id("chat-pending")
    expect(waiting.locator("li[data-kind=steering]:not([data-pending])")).to_contain_text("use the v2 API")
    # Queue waits for the turn to end
    box.fill("then read the README")
    chat(page).get_by_role("button", name="Queue").click()
    expect(waiting.locator("li[data-kind=queued]:not([data-pending])")).to_contain_text("then read the README")
    # when the turn ends, the steering message is gone, and the queued one starts the next turn
    chat(page).get_by_role("button", name="Stop").click()
    expect(chat(page).locator("article.turn")).to_have_count(3)
    nxt = chat(page).locator("article.turn").last
    expect(nxt.locator(".prompt")).to_have_text("then read the README")
    expect(nxt).to_have_attribute("data-state", "complete")
    expect(waiting).to_have_count(0)
    kinds = [(a["type"], a.get("kind")) for a in dispatched(hub, "steer")]
    assert kinds == [("chat/turnStarted", None), ("chat/pendingMessageSet", "steering"), ("chat/pendingMessageSet", "queued"), ("chat/turnCancelled", None)], kinds


def test_a_prompt_the_agent_does_not_take_says_why(single_hub, page):
    hub = single_hub
    sign_in(page, hub, "admin")
    hub.world("rich-agent", project="web", agent="picky")
    open_chat(page, hub, "picky")
    expect(chat(page).get_by_test_id("chat-status")).to_have_text("ready")
    hub.world("ahp-reject", project="web", agent="picky", text="a turn is already active")
    box = chat(page).get_by_label("Prompt")
    box.fill("a prompt")
    box.press("Enter")
    expect(chat(page).get_by_role("status").filter(has_text="The agent did not take it: a turn is already active")).to_be_visible()
    expect(chat(page).locator("article.turn")).to_have_count(1)  # nothing of it stays


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
    expect(turn.get_by_test_id("turn-meta")).to_contain_text("Failed")


def test_what_is_typed_in_the_terminal_shows_in_the_chat(single_hub, page):
    hub = single_hub
    sign_in(page, hub, "admin")
    hub.world("rich-agent", project="web", agent="both")
    open_chat(page, hub, "both")
    expect(chat(page).get_by_text("Explain the parser", exact=True).first).to_be_visible()
    hub.world("ahp-say", project="web", agent="both", text="tidy up the tests")
    turn = chat(page).locator("article.turn").last
    expect(turn.locator(".prompt")).to_have_text("tidy up the tests")
    expect(turn).to_have_attribute("data-state", "complete")


def test_a_message_egzo_announces_shows_as_what_the_person_sent(single_hub, page):
    hub = single_hub
    sign_in(page, hub, "admin")
    hub.world("rich-agent", project="web", agent="inbox")
    open_chat(page, hub, "inbox")
    expect(chat(page).get_by_test_id("chat-status")).to_have_text("ready")
    hub.world("ahp-message", project="web", agent="inbox", user="admin", code="m0a1b2c3d4", text="Please **fix** the login page")
    turn = chat(page).locator("article.turn").last
    message = turn.get_by_test_id("prompt-message")
    expect(message).to_contain_text("admin")
    expect(message).to_contain_text("asks, through egzo")
    expect(message.locator("strong")).to_have_text("fix")
    expect(turn).not_to_contain_text("check egzo message")
    expect(turn).to_have_attribute("data-state", "complete")
    # a message the hub does not know (from the operator, at the CLI) shows as egzo's line
    hub.world("ahp-say", project="web", agent="inbox", text="check egzo message m99999999 and handle the request for me.")
    expect(chat(page).locator("article.turn").last.get_by_test_id("prompt-egzo")).to_contain_text("check egzo message m99999999")


def test_the_chat_comes_back_on_its_own_after_the_connection_drops(single_hub, page):
    hub = single_hub
    sign_in(page, hub, "admin")
    hub.world("rich-agent", project="web", agent="flaky")
    open_chat(page, hub, "flaky")
    expect(chat(page).get_by_test_id("chat-status")).to_have_text("ready")
    hub.world("ahp-drop", project="web", agent="flaky")
    # what happens while it is away is replayed when it is back, without a reload
    hub.world("ahp-say", project="web", agent="flaky", text="tidy up the tests")
    turn = chat(page).locator("article.turn").last
    expect(turn.locator(".prompt")).to_have_text("tidy up the tests", timeout=10000)
    expect(turn).to_have_attribute("data-state", "complete")
    expect(turn.locator(".answer strong")).to_have_text("build")
    expect(chat(page).get_by_test_id("chat-status")).to_have_text("ready")
    reconnects = [m for m in heard(hub, "flaky") if m.get("method") == "reconnect"]
    assert reconnects, " ".join(m.get("method") for m in heard(hub, "flaky"))
    assert reconnects[-1]["params"]["subscriptions"] == ["ahp-root://", "ahp-session:/ses_flaky", "ahp-chat:/ses_flaky"], reconnects
    initializes = [m for m in heard(hub, "flaky") if m.get("method") == "initialize"]
    assert len(initializes) == 1, initializes


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
    subscribes = [m for m in heard(hub, "early") if m.get("method") == "subscribe" and m["params"]["channel"].startswith("ahp-chat:")]
    assert len(subscribes) == 3, subscribes
