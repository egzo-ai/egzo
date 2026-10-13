import re

from playwright.sync_api import expect

from conftest import sign_in


def test_the_first_person_to_sign_in_is_the_operator_and_sees_every_project(hub, page):
    sign_in(page, hub, "root")
    expect(page.get_by_test_id("signed-in-as")).to_contain_text("operator")
    expect(page.get_by_role("heading", name="Projects")).to_be_visible()
    rows = page.locator("table.list tbody tr")
    expect(rows).to_have_count(4)  # web and data on local, web and ml on lab
    expect(page.get_by_role("link", name="data")).to_be_visible()
    expect(page.get_by_role("link", name="ml")).to_be_visible()
    # two engines: each project says where it lives
    expect(page.locator(".engine-badge")).to_have_count(4)


def test_with_one_engine_there_is_no_engine_badge_and_no_filter(single_hub, page):
    sign_in(page, single_hub, "root")
    expect(page.locator("table.list tbody tr")).to_have_count(2)
    expect(page.locator(".engine-badge")).to_have_count(0)
    expect(page.locator("nav.engines a.engine")).to_have_count(0)


def test_the_engine_chips_filter_the_list(hub, page):
    sign_in(page, hub, "root")
    page.locator("nav.engines a.engine", has_text="lab").click()
    expect(page.locator("table.list tbody tr")).to_have_count(2)
    expect(page.get_by_role("link", name="ml")).to_be_visible()
    expect(page.get_by_role("link", name="data")).to_have_count(0)
    page.get_by_role("link", name="Show all engines").click()
    expect(page.locator("table.list tbody tr")).to_have_count(4)


def test_an_engine_that_is_down_is_reported_without_losing_the_others(hub, page):
    sign_in(page, hub, "root")
    hub.world("down", engine="lab")
    page.wait_for_timeout(1500)  # the hub rescans every second
    page.reload()
    expect(page.get_by_role("alert")).to_contain_text("lab is unreachable")
    expect(page.get_by_role("link", name="data")).to_be_visible()
    expect(page.locator("nav.engines a.engine.down")).to_have_count(1)
    hub.world("up", engine="lab")
    page.wait_for_timeout(6000)  # the registry retries a failed engine after its cooldown
    page.reload()
    expect(page.get_by_role("alert")).to_have_count(0)


def test_a_newcomer_sees_nothing_until_an_owner_adds_them(hub, page, new_person):
    sign_in(page, hub, "root")
    alice = new_person("alice")
    expect(alice.get_by_text("no role on any project")).to_be_visible()
    alice.goto(hub.url + "/e/local/p/web")
    expect(alice.get_by_role("heading", name="Not Found")).to_be_visible()

    page.goto(hub.url + "/e/local/p/web/members")
    page.get_by_label("Person").select_option(label="alice (alice@example.com)")
    page.locator("section select[name=role]").select_option("user")
    page.get_by_role("button", name="Add").click()
    expect(page.get_by_role("status")).to_contain_text("Roles updated")
    expect(page.locator("tr[data-user]")).to_have_count(2)

    alice.goto(hub.url + "/")
    expect(alice.get_by_role("link", name="web")).to_be_visible()
    expect(alice.locator("table.list tbody tr")).to_have_count(1)
    # a user does not see the other projects, nor the owner's tools
    alice.get_by_role("link", name="web").click()
    expect(alice.get_by_role("link", name="Members")).to_have_count(0)
    expect(alice.get_by_role("link", name="Audit log")).to_have_count(0)


def test_an_owner_cannot_remove_themselves(hub, page):
    sign_in(page, hub, "root")
    page.goto(hub.url + "/e/local/p/web/members")
    row = page.locator("tr[data-user]").first
    expect(row.get_by_role("button", name="Remove")).to_have_count(0)
    expect(row.get_by_role("combobox")).to_be_disabled()


def test_spawn_stop_start_and_remove_an_agent(single_hub, page):
    sign_in(page, single_hub, "root")
    page.get_by_role("link", name="web").click()
    page.get_by_label("Template").select_option("reviewer")
    page.get_by_label("Name").fill("fix-bug")
    page.get_by_role("button", name="Spawn").click()
    expect(page.get_by_role("status")).to_contain_text("Spawned fix-bug")
    expect(page.get_by_role("heading", name="fix-bug")).to_be_visible()
    expect(page.get_by_test_id("activity")).to_have_text("idle")

    page.get_by_role("button", name="Stop").click()
    expect(page.get_by_test_id("activity")).to_have_text("stopped")
    expect(page.get_by_role("button", name="Open terminal")).to_have_count(0)
    page.get_by_role("button", name="Start").click()
    expect(page.get_by_test_id("activity")).to_have_text("idle")

    page.once("dialog", lambda dialog: dialog.dismiss())
    page.get_by_role("button", name="Remove").click()
    expect(page.get_by_role("heading", name="fix-bug")).to_be_visible()  # still here: the confirmation was refused
    page.once("dialog", lambda dialog: dialog.accept())
    page.get_by_role("button", name="Remove").click()
    expect(page.get_by_role("status")).to_contain_text("Removed fix-bug")
    expect(page.locator("tr[data-agent=fix-bug]")).to_have_count(0)
    expect(page.locator("tr[data-agent=coder-1]")).to_be_visible()
    assert single_hub.calls()[:1] and single_hub.calls()[0].startswith("spawn web/fix-bug user:")


def test_the_page_follows_the_agent_without_reloading(single_hub, page):
    sign_in(page, single_hub, "root")
    page.get_by_role("link", name="web").click()
    row = page.locator("tr[data-agent=coder-1]")
    expect(row).to_contain_text("refactoring the parser on local")
    single_hub.world("set-agent", engine="local", project="web", agent="coder-1", state="working", text="writing the tests", waiting=True)
    expect(row).to_contain_text("writing the tests", timeout=8000)
    expect(row).to_contain_text("waiting for an answer")
    row.get_by_role("link", name="coder-1").click()
    expect(page.get_by_test_id("waiting")).to_be_visible()
    expect(page.get_by_test_id("status")).to_have_text("writing the tests")
    single_hub.world("set-agent", engine="local", project="web", agent="coder-1", state="idle", text="done")
    expect(page.get_by_test_id("status")).to_have_text("done", timeout=8000)
    expect(page.get_by_test_id("waiting")).to_have_count(0)


def test_there_is_no_messaging_in_the_web_ui(single_hub, page):
    sign_in(page, single_hub, "root")
    page.goto(single_hub.url + "/e/local/p/web/a/coder-1")
    expect(page.locator("textarea")).to_have_count(0)
    expect(page.get_by_role("button", name=re.compile("send", re.I))).to_have_count(0)
    expect(page.get_by_role("button", name="Open terminal")).to_be_visible()


def test_the_terminal_types_into_the_agent_and_a_second_person_only_watches(hub, page, new_person):
    sign_in(page, hub, "root")
    page.goto(hub.url + "/e/local/p/web/a/coder-1")
    page.get_by_role("button", name="Open terminal").click()
    expect(page.locator("#terminal-mode")).to_have_text("you are typing")
    terminal = page.locator("#terminal")
    expect(terminal).to_contain_text("attached to web/coder-1")
    terminal.click()
    page.keyboard.type("hello egzo")
    page.keyboard.press("Enter")
    expect(terminal).to_contain_text("you said: hello egzo")
    expect(page.get_by_test_id("presence")).to_contain_text("root (typing)", timeout=8000)

    alice = new_person("alice")
    hub.world("grant", engine="local", project="web", user="alice", role="administrator")
    alice.goto(hub.url + "/e/local/p/web/a/coder-1")
    alice.get_by_role("button", name="Open terminal").click()
    expect(alice.locator("#terminal-mode")).to_contain_text("watching (read only)")
    alice.locator("#terminal").click()
    alice.keyboard.type("intruder")
    alice.keyboard.press("Enter")
    page.wait_for_timeout(500)
    expect(page.locator("#terminal")).not_to_contain_text("intruder")
    expect(page.get_by_test_id("presence")).to_contain_text("alice (watching)", timeout=8000)

    page.get_by_role("button", name="Disconnect").click()
    expect(page.locator("#terminal-mode")).to_have_text("disconnected")


def test_a_user_cannot_touch_an_agent_the_operator_spawned(hub, page, new_person):
    sign_in(page, hub, "root")
    alice = new_person("alice")
    hub.world("grant", engine="local", project="web", user="alice", role="user")
    alice.goto(hub.url + "/e/local/p/web")
    expect(alice.locator("tr[data-agent=coder-1]")).to_be_visible()
    expect(alice.get_by_role("button", name="Stop")).to_have_count(0)
    expect(alice.get_by_role("button", name="Remove")).to_have_count(0)
    # but a user's own agent is theirs
    alice.get_by_label("Name").fill("alices")
    alice.get_by_role("button", name="Spawn").click()
    expect(alice.get_by_role("button", name="Stop")).to_be_visible()
    expect(alice.get_by_role("button", name="Remove")).to_be_visible()
    alice.goto(hub.url + "/e/local/p/web")
    expect(alice.locator("tr[data-agent=alices]").get_by_role("button", name="Stop")).to_be_visible()
    expect(alice.locator("tr[data-agent=coder-1]").get_by_role("button", name="Stop")).to_have_count(0)


def test_a_project_nobody_owns_goes_to_the_operator_when_it_appears(single_hub, page):
    sign_in(page, single_hub, "root")
    single_hub.world("add-project", engine="local", project="fresh")
    expect(page.get_by_role("link", name="fresh")).to_have_count(0)
    page.wait_for_timeout(1500)
    page.reload()
    expect(page.get_by_role("link", name="fresh")).to_be_visible()


def test_the_operator_adds_an_ssh_engine_and_confirms_its_host_key(single_hub, page):
    sign_in(page, single_hub, "root")
    page.get_by_role("link", name="Engines", exact=True).click()
    expect(page.get_by_test_id("engines")).to_contain_text("local")
    page.get_by_label("Name").fill("far")
    page.get_by_label("Address", exact=True).fill("ssh://me@far.example")
    page.locator("details", has_text="ssh (ssh://)").locator("summary").click()
    page.get_by_label("Private key").fill("-----BEGIN OPENSSH PRIVATE KEY-----\nsecret\n-----END OPENSSH PRIVATE KEY-----")
    page.get_by_role("button", name="Test and save").click()
    expect(page.get_by_test_id("fingerprint")).to_have_text("SHA256:e2e-host-key")
    expect(page.locator("body")).not_to_contain_text("BEGIN OPENSSH PRIVATE KEY")
    page.get_by_role("button", name="Pin this key and save").click()
    expect(page.get_by_role("status")).to_contain_text("Saved engine far")
    expect(page.get_by_test_id("engines")).to_contain_text("far")
    expect(page.get_by_test_id("engines")).to_contain_text("ssh://me@far.example")

    page.once("dialog", lambda dialog: dialog.accept())
    page.get_by_test_id("engines").locator("tr", has_text="far").get_by_role("button", name="Remove").click()
    expect(page.get_by_role("status")).to_contain_text("Removed engine far")


def test_only_the_operator_sees_the_engines_page(hub, page, new_person):
    sign_in(page, hub, "root")
    alice = new_person("alice")
    expect(alice.get_by_role("link", name="Engines", exact=True)).to_have_count(0)
    alice.goto(hub.url + "/engines")
    expect(alice.get_by_role("heading", name="Forbidden")).to_be_visible()


def test_the_audit_log_shows_who_did_what_and_what_was_refused(hub, page, new_person):
    sign_in(page, hub, "root")
    alice = new_person("alice")
    hub.world("grant", engine="local", project="web", user="alice", role="user")
    alice.goto(hub.url + "/e/local/p/web")
    alice.get_by_label("Name").fill("alices")
    alice.get_by_role("button", name="Spawn").click()
    expect(alice.get_by_role("status")).to_contain_text("Spawned")
    alice.request.post(hub.url + "/e/local/p/web/a/coder-1/rm", headers={"Origin": hub.url})  # refused: not alice's
    page.goto(hub.url + "/e/local/p/web/audit")
    audit = page.get_by_test_id("audit")
    expect(audit).to_contain_text("agent.spawn")
    expect(audit.locator("tr.refused")).to_contain_text("agent.rm")
    expect(audit.locator("tr.refused")).to_contain_text("alice")


def test_the_spec_and_the_logs_are_read_only_text(single_hub, page):
    sign_in(page, single_hub, "root")
    page.goto(single_hub.url + "/e/local/p/web")
    page.get_by_role("link", name="Spec").click()
    expect(page.get_by_test_id("text")).to_contain_text("name: web")
    page.goto(single_hub.url + "/e/local/p/web/a/coder-1")
    page.get_by_role("link", name="Logs").click()
    expect(page.get_by_test_id("text")).to_contain_text("log of coder-1")


def test_signing_out_ends_the_session(single_hub, page):
    sign_in(page, single_hub, "root")
    page.get_by_role("button", name="Sign out").click()
    expect(page.get_by_role("heading", name="Sign in")).to_be_visible()
    page.goto(single_hub.url + "/")
    expect(page).to_have_url(re.compile(r"/login\?next="))


def test_the_pages_fit_a_phone(single_hub, browser):
    context = browser.new_context(viewport={"width": 390, "height": 800})
    page = context.new_page()
    sign_in(page, single_hub, "root")
    page.get_by_role("link", name="web").click()
    assert page.evaluate("document.documentElement.scrollWidth") <= 392, "the page scrolls sideways on a phone"
    context.close()
