import re

from playwright.sync_api import expect

from conftest import sign_in


def projects_table(page):
    return page.get_by_test_id("projects")


def spawn(page, name, template=None):
    page.get_by_role("button", name="Spawn agent").click()
    dialog = page.get_by_role("dialog")
    if template:
        dialog.get_by_label("Template").select_option(template)
    dialog.get_by_label("Name").fill(name)
    dialog.get_by_role("button", name="Spawn", exact=True).click()


def terminal_text(page, agent="coder-1"):
    return page.locator(f".tpane[aria-label='Terminal of {agent}'] .xterm-rows")


def terminal_state(page, agent="coder-1"):
    return page.locator(f".tpane[aria-label='Terminal of {agent}']").get_by_test_id("terminal-state")


def test_the_first_person_to_sign_in_is_the_operator_and_sees_every_project(hub, page):
    sign_in(page, hub, "root")
    expect(page.get_by_test_id("signed-in-as")).to_contain_text("operator")
    expect(page.get_by_role("heading", name="Projects")).to_be_visible()
    expect(projects_table(page).locator("tbody tr")).to_have_count(4)  # web and data on local, web and ml on lab
    expect(projects_table(page).get_by_role("link", name="data")).to_be_visible()
    expect(projects_table(page).get_by_role("link", name="ml")).to_be_visible()
    expect(projects_table(page).locator("tbody tr").first).to_contain_text("operator")  # held as an operator: no role
    # two engines: each project says where it lives
    expect(projects_table(page).locator(".engine")).to_have_count(4)


def test_with_one_engine_there_is_no_engine_badge_and_no_filter(single_hub, page):
    sign_in(page, single_hub, "root")
    expect(projects_table(page).locator("tbody tr")).to_have_count(2)
    expect(page.locator(".engine")).to_have_count(0)
    expect(page.locator(".chips")).to_have_count(0)


def test_the_engine_chips_filter_the_list(hub, page):
    sign_in(page, hub, "root")
    page.locator(".chips .chip", has_text="lab").click()
    expect(projects_table(page).locator("tbody tr")).to_have_count(2)
    expect(projects_table(page).get_by_role("link", name="ml")).to_be_visible()
    expect(projects_table(page).get_by_role("link", name="data")).to_have_count(0)
    expect(page).to_have_url(re.compile(r"\?engine=lab"))
    page.get_by_role("link", name="Show all engines").click()
    expect(projects_table(page).locator("tbody tr")).to_have_count(4)


def test_an_engine_that_is_down_is_reported_without_losing_the_others(hub, page):
    sign_in(page, hub, "root")
    hub.world("down", engine="lab")
    expect(page.get_by_role("alert")).to_contain_text("lab is unreachable", timeout=8000)  # the hub rescans every second, the page asks every three
    expect(projects_table(page).get_by_role("link", name="data")).to_be_visible()
    expect(page.locator(".chips .chip[data-down]")).to_have_count(1)
    hub.world("up", engine="lab")
    expect(page.get_by_role("alert")).to_have_count(0, timeout=15000)  # the registry retries a failed engine after its cooldown


def test_a_newcomer_sees_nothing_until_an_owner_adds_them(hub, page, new_person):
    sign_in(page, hub, "root")
    alice = new_person("alice")
    expect(alice.get_by_text("have a role on any project")).to_be_visible()
    alice.goto(hub.url + "/e/local/p/web")
    expect(alice.get_by_role("heading", name="Not found")).to_be_visible()

    page.goto(hub.url + "/e/local/p/web/members")
    page.get_by_label("Person", exact=True).select_option(label="alice (alice@example.com)")
    page.get_by_label("Role", exact=True).select_option("user")
    page.get_by_role("button", name="Add", exact=True).click()
    expect(page.get_by_role("status")).to_contain_text("Roles updated")
    expect(page.locator("tr[data-user]")).to_have_count(2)

    alice.goto(hub.url + "/")
    expect(projects_table(alice).locator("tbody tr")).to_have_count(1)
    projects_table(alice).get_by_role("link", name="web").click()
    # a user does not see the other projects, nor the owner's tools
    expect(alice.get_by_role("tab", name="Members")).to_have_count(0)
    expect(alice.get_by_role("tab", name="Audit log")).to_have_count(0)


def test_an_owner_cannot_remove_themselves_and_an_operator_is_a_locked_row(hub, page, new_person):
    sign_in(page, hub, "root")
    alice = new_person("alice")
    hub.world("grant", engine="local", project="web", user="alice", role="owner")
    alice.goto(hub.url + "/e/local/p/web/members")
    mine = alice.locator("tr[data-user]").filter(has_text="alice@example.com")
    expect(mine.get_by_role("button", name="Remove")).to_have_count(0)
    expect(mine.get_by_role("combobox")).to_be_disabled()
    # the operator holds the project without a role: listed, shaded, and nothing to change
    locked = alice.locator("tr[data-operator]")
    expect(locked).to_have_count(1)
    expect(locked).to_contain_text("root")
    expect(locked).to_contain_text("operator")
    expect(locked.get_by_role("button")).to_have_count(0)
    expect(locked.get_by_role("combobox")).to_have_count(0)


def test_removing_a_member_asks_first(hub, page, new_person):
    sign_in(page, hub, "root")
    new_person("alice")
    hub.world("grant", engine="local", project="web", user="alice", role="user")
    page.goto(hub.url + "/e/local/p/web/members")
    expect(page.locator("tr[data-user]")).to_have_count(2)
    page.get_by_role("button", name="Remove alice").click()
    page.get_by_role("dialog").get_by_role("button", name="Cancel").click()
    expect(page.locator("tr[data-user]")).to_have_count(2)
    page.get_by_role("button", name="Remove alice").click()
    page.get_by_role("dialog").get_by_role("button", name="Remove from the project").click()
    expect(page.locator("tr[data-user]")).to_have_count(1)


def test_spawn_stop_start_and_remove_an_agent(single_hub, page):
    sign_in(page, single_hub, "root")
    projects_table(page).get_by_role("link", name="web").click()
    spawn(page, "fix-bug", "reviewer")
    expect(page.get_by_role("status")).to_contain_text("Spawned fix-bug")
    expect(page.get_by_role("heading", name="fix-bug")).to_be_visible()
    expect(page.get_by_test_id("activity")).to_have_text("idle")

    page.get_by_role("button", name="Stop", exact=True).click()
    expect(page.get_by_test_id("activity")).to_have_text("stopped")
    expect(page.get_by_role("button", name=re.compile("^(Attach|Go to terminal)$"))).to_have_count(0)
    page.get_by_role("button", name="Start", exact=True).click()
    expect(page.get_by_test_id("activity")).to_have_text("idle")

    page.get_by_role("button", name="Remove", exact=True).click()
    page.get_by_role("dialog").get_by_role("button", name="Cancel").click()
    expect(page.get_by_role("heading", name="fix-bug")).to_be_visible()  # still here: the confirmation was refused
    page.get_by_role("button", name="Remove", exact=True).click()
    page.get_by_role("dialog").get_by_role("button", name="Remove", exact=True).click()
    expect(page.get_by_role("status").last).to_contain_text("Removed fix-bug")
    expect(page.locator("tr[data-agent=fix-bug]")).to_have_count(0)
    expect(page.locator("tr[data-agent=coder-1]")).to_be_visible()
    assert single_hub.calls()[:1] and single_hub.calls()[0].startswith("spawn web/fix-bug user:")


def test_the_page_follows_the_agent_without_reloading(single_hub, page):
    sign_in(page, single_hub, "root")
    projects_table(page).get_by_role("link", name="web").click()
    row = page.locator("tr[data-agent=coder-1]")
    expect(row).to_contain_text("refactoring the parser on local")
    single_hub.world("set-agent", engine="local", project="web", agent="coder-1", state="working", text="writing the tests", waiting=True)
    expect(row).to_contain_text("writing the tests", timeout=8000)
    expect(row).to_contain_text("waiting for you")
    # the sidebar counts the questions of every project
    expect(page.locator(".side .qcount").first).to_be_visible()
    row.get_by_role("link", name="coder-1").click()
    expect(page.get_by_test_id("waiting")).to_be_visible()
    expect(page.get_by_test_id("status")).to_have_text("writing the tests")
    single_hub.world("set-agent", engine="local", project="web", agent="coder-1", state="idle", text="done")
    expect(page.get_by_test_id("status")).to_have_text("done", timeout=8000)
    expect(page.get_by_test_id("waiting")).to_have_count(0)


def test_there_is_no_messaging_in_the_web_ui(single_hub, page):
    sign_in(page, single_hub, "root")
    page.goto(single_hub.url + "/e/local/p/web/a/coder-1")
    expect(page.get_by_role("heading", name="coder-1")).to_be_visible()
    expect(page.locator("textarea")).to_have_count(0)
    expect(page.get_by_role("button", name=re.compile("send", re.I))).to_have_count(0)
    expect(page.get_by_role("button", name="Attach", exact=True)).to_be_visible()


def test_the_terminal_types_into_the_agent_and_a_second_person_can_take_it_over(hub, page, new_person):
    sign_in(page, hub, "root")
    page.goto(hub.url + "/e/local/p/web/a/coder-1")
    page.get_by_role("button", name="Attach", exact=True).click()
    expect(terminal_state(page)).to_have_text("connected")
    terminal = terminal_text(page)
    expect(terminal).to_contain_text("attached to web/coder-1")
    page.locator(".tpane .xterm").click()
    page.keyboard.type("hello egzo")
    page.keyboard.press("Enter")
    expect(terminal).to_contain_text("you said: hello egzo")
    # the terminal is in the sidebar and the agent page says who holds it
    expect(page.get_by_role("option", name=re.compile("^coder-1, web"))).to_be_visible()
    page.get_by_role("link", name="Agent page").click()
    expect(page.get_by_test_id("presence")).to_contain_text("you", timeout=8000)

    alice = new_person("alice")
    hub.world("grant", engine="local", project="web", user="alice", role="administrator")
    alice.goto(hub.url + "/e/local/p/web/a/coder-1")
    alice.get_by_role("button", name="Attach", exact=True).click()
    expect(terminal_state(alice)).to_have_text("in use by someone else")
    expect(alice.get_by_role("status")).to_contain_text("root is at this terminal")
    expect(terminal_text(alice)).not_to_contain_text("attached to")
    alice.get_by_role("button", name="Take over").click()
    expect(terminal_text(alice)).to_contain_text("attached to web/coder-1")
    expect(terminal_state(page)).to_have_text("taken over")
    expect(page.get_by_role("option", name=re.compile("taken over"))).to_be_visible()
    alice.locator(".tpane .xterm").click()
    alice.keyboard.type("mine")
    alice.keyboard.press("Enter")
    expect(terminal_text(alice)).to_contain_text("you said: mine")
    expect(terminal_text(page)).not_to_contain_text("you said: mine")

    alice.get_by_role("button", name="Close terminal").click()
    expect(alice.locator(".tpane")).to_have_count(0)
    expect(alice.get_by_role("status")).to_contain_text("Detached from coder-1")


def test_several_terminals_stay_open_while_you_browse_and_the_leader_key_switches_them(single_hub, page):
    sign_in(page, single_hub, "root")
    single_hub.world("set-agent", engine="local", project="web", agent="coder-2", state="idle", text="second")
    page.goto(single_hub.url + "/e/local/p/web")
    page.locator("tr[data-agent=coder-1]").get_by_role("button", name="Attach coder-1").click()
    expect(terminal_text(page, "coder-1")).to_contain_text("attached to web/coder-1")
    page.get_by_role("link", name="All projects", exact=True).click()  # a page is shown; the terminal stays open
    expect(page.get_by_role("heading", name="Projects")).to_be_visible()
    page.goto(single_hub.url + "/e/local/p/web")  # even a reload brings it back
    expect(page.get_by_role("option", name=re.compile("^coder-1"))).to_be_visible()
    page.locator("tr[data-agent=coder-2]").get_by_role("button", name="Attach coder-2").click(timeout=15000)
    expect(terminal_text(page, "coder-2")).to_contain_text("attached to web/coder-2")
    expect(page.locator(".side [role=option]")).to_have_count(2)

    # Ctrl+] then 1 goes to the first terminal; the keys go to the agent otherwise
    page.keyboard.press("Control+]")
    expect(page.locator(".leader")).to_be_visible()
    page.keyboard.press("1")
    expect(page.locator(".tpane[aria-label='Terminal of coder-1']")).to_be_visible()
    expect(page.locator(".tpane[aria-label='Terminal of coder-2']")).to_be_hidden()
    page.locator(".tpane[aria-label='Terminal of coder-1'] .xterm").click()
    page.keyboard.type("one")
    page.keyboard.press("Enter")
    expect(terminal_text(page, "coder-1")).to_contain_text("you said: one")
    # split: both are shown
    page.keyboard.press("Control+]")
    page.keyboard.press("s")
    expect(page.locator(".tpane:visible")).to_have_count(2)
    # Ctrl+] then d closes the focused one
    page.keyboard.press("Control+]")
    page.keyboard.press("d")
    expect(page.locator(".side [role=option]")).to_have_count(1)
    # and the page comes back with the leader and h
    page.keyboard.press("Control+]")
    page.keyboard.press("h")
    expect(page.get_by_role("heading", name="coder-1").or_(page.get_by_role("tab", name="Agents"))).to_be_visible()


def test_a_user_who_may_not_operate_the_agent_has_no_terminal(hub, page, new_person):
    sign_in(page, hub, "root")
    bob = new_person("bob")
    hub.world("grant", engine="local", project="web", user="bob", role="user")
    bob.goto(hub.url + "/e/local/p/web/a/coder-1")
    expect(bob.get_by_role("heading", name="coder-1")).to_be_visible()
    expect(bob.get_by_role("button", name="Attach", exact=True)).to_have_count(0)
    expect(bob.get_by_text("Only the people who may operate")).to_be_visible()


def test_a_user_cannot_touch_an_agent_the_operator_spawned(hub, page, new_person):
    sign_in(page, hub, "root")
    alice = new_person("alice")
    hub.world("grant", engine="local", project="web", user="alice", role="user")
    alice.goto(hub.url + "/e/local/p/web")
    expect(alice.locator("tr[data-agent=coder-1]")).to_be_visible()
    expect(alice.get_by_role("button", name="Stop coder-1")).to_have_count(0)
    expect(alice.get_by_role("button", name="Remove coder-1")).to_have_count(0)
    # but a user's own agent is theirs
    spawn(alice, "alices")
    expect(alice.get_by_role("button", name="Stop", exact=True)).to_be_visible()
    expect(alice.get_by_role("button", name="Remove", exact=True)).to_be_visible()
    alice.goto(hub.url + "/e/local/p/web")
    expect(alice.locator("tr[data-agent=alices]").get_by_role("button", name="Stop alices")).to_be_visible()
    expect(alice.locator("tr[data-agent=coder-1]").get_by_role("button", name="Stop coder-1")).to_have_count(0)


def test_an_operator_holds_a_project_as_soon_as_the_scan_finds_it(single_hub, page):
    sign_in(page, single_hub, "root")
    single_hub.world("add-project", engine="local", project="fresh")
    expect(projects_table(page).get_by_role("link", name="fresh")).to_be_visible(timeout=10000)  # found by the next scan


def test_a_project_whose_sidecar_logs_no_status_says_so_and_shows_its_agents_as_running(single_hub, page):
    sign_in(page, single_hub, "root")
    single_hub.world("add-project", engine="local", project="mute", state="silent")
    single_hub.world("set-agent", engine="local", project="mute", agent="coder-1", state="working")
    page.goto(single_hub.url + "/e/local/p/mute")
    agent = page.get_by_test_id("agents").locator("tr[data-agent=coder-1]")
    expect(agent).to_be_visible()
    # nothing has been heard yet: the agent is not called "starting", and there is nothing to warn about yet
    expect(agent).not_to_contain_text("starting")
    # after a while of silence the page says why it cannot tell what the agent is doing
    expect(page.get_by_test_id("feed-banner")).to_contain_text("writes no status", timeout=15000)
    expect(agent).to_contain_text("running")
    expect(agent).not_to_contain_text("starting")


def test_a_project_with_a_status_log_shows_what_its_agents_do_and_no_banner(single_hub, page):
    sign_in(page, single_hub, "root")
    page.goto(single_hub.url + "/e/local/p/web")
    expect(page.locator("tr[data-agent=coder-1]")).to_contain_text("working")
    expect(page.get_by_test_id("feed-banner")).to_have_count(0)


def test_the_operator_adds_an_ssh_engine_and_confirms_its_host_key(single_hub, page):
    sign_in(page, single_hub, "root")
    page.get_by_role("link", name=re.compile("^Settings")).click()
    expect(page.get_by_role("heading", name="Settings")).to_be_visible()
    expect(page.get_by_role("tab", name="Engines")).to_have_attribute("aria-selected", "true")
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
    expect(page.get_by_test_id("engines")).to_contain_text("ssh://me@far.example")

    page.get_by_role("button", name="Remove far").click()
    dialog = page.get_by_role("dialog")
    expect(dialog.get_by_role("button", name="Forget engine")).to_be_disabled()
    dialog.get_by_label("Type").fill("far")
    dialog.get_by_role("button", name="Forget engine").click()
    expect(page.get_by_role("status").last).to_contain_text("Removed engine far")


def test_only_an_operator_sees_the_settings(hub, page, new_person):
    sign_in(page, hub, "root")
    alice = new_person("alice")
    expect(alice.get_by_role("link", name=re.compile("^Settings"))).to_have_count(0)
    for path in ("/settings/engines", "/settings/users", "/engines"):
        alice.goto(hub.url + path)
        expect(alice.get_by_role("heading", name="Forbidden")).to_be_visible()
    # the old address lands on the engines tab
    page.goto(hub.url + "/engines")
    expect(page).to_have_url(hub.url + "/settings/engines")
    expect(page.get_by_test_id("engines")).to_be_visible()


def test_an_operator_makes_another_person_an_operator_and_the_last_one_stays(hub, page, new_person):
    sign_in(page, hub, "root")
    alice = new_person("alice")
    expect(alice.get_by_role("link", name=re.compile("^Settings"))).to_have_count(0)

    page.get_by_role("link", name=re.compile("^Settings")).click()
    page.get_by_role("tab", name="Users").click()
    users = page.get_by_test_id("users")
    expect(users.locator("tr[data-user]")).to_have_count(2)
    root_row = users.locator("tr[data-user]").filter(has_text="root@example.com")
    alice_row = users.locator("tr[data-user]").filter(has_text="alice@example.com")
    # the only operator cannot be removed
    expect(root_row.get_by_role("button", name="Remove operator from root")).to_be_disabled()
    expect(alice_row.get_by_test_id("operator-badge")).to_have_count(0)

    alice_row.get_by_role("button", name="Make alice an operator").click()
    page.get_by_role("dialog").get_by_role("button", name="Cancel").click()
    expect(alice_row.get_by_test_id("operator-badge")).to_have_count(0)
    alice_row.get_by_role("button", name="Make alice an operator").click()
    page.get_by_role("dialog").get_by_role("button", name="Make operator").click()
    expect(page.get_by_role("status")).to_contain_text("alice is an operator")
    expect(alice_row.get_by_test_id("operator-badge")).to_be_visible()

    # alice now has the settings, and holds every project
    alice.goto(hub.url + "/")
    expect(alice.get_by_role("link", name=re.compile("^Settings"))).to_be_visible()
    expect(projects_table(alice).locator("tbody tr")).to_have_count(4)  # every project of both engines, none of them granted
    expect(projects_table(alice).locator("tbody tr").first).to_contain_text("operator")

    # with two operators, root may step down, and is then no operator
    expect(root_row.get_by_role("button", name="Remove operator from root")).to_be_enabled()
    root_row.get_by_role("button", name="Remove operator from root").click()
    page.get_by_role("dialog").get_by_role("button", name="Remove operator").click()
    expect(page.get_by_role("heading", name="Forbidden")).to_be_visible()
    expect(page.get_by_role("link", name=re.compile("^Settings"))).to_have_count(0)

    # alice is the last one
    alice.goto(hub.url + "/settings/users")
    last = alice.get_by_test_id("users").locator("tr[data-user]").filter(has_text="alice@example.com")
    expect(last.get_by_role("button", name="Remove operator from alice")).to_be_disabled()


def test_the_audit_log_shows_who_did_what_and_what_was_refused(hub, page, new_person):
    sign_in(page, hub, "root")
    alice = new_person("alice")
    hub.world("grant", engine="local", project="web", user="alice", role="user")
    alice.goto(hub.url + "/e/local/p/web")
    spawn(alice, "alices")
    expect(alice.get_by_role("status")).to_contain_text("Spawned")
    alice.request.post(hub.url + "/api/v1/e/local/p/web/a/coder-1/rm", data={}, headers={"Origin": hub.url})  # refused: not alice's
    page.goto(hub.url + "/e/local/p/web/audit")
    audit = page.get_by_test_id("audit")
    expect(audit).to_contain_text("agent.spawn")
    expect(audit.locator("tr.refused")).to_contain_text("agent.rm")
    expect(audit.locator("tr.refused")).to_contain_text("alice")


def test_the_spec_and_the_logs_are_read_only_text(single_hub, page):
    sign_in(page, single_hub, "root")
    page.goto(single_hub.url + "/e/local/p/web")
    page.get_by_role("tab", name="Spec").click()
    expect(page.get_by_test_id("text")).to_contain_text("name: web")
    page.goto(single_hub.url + "/e/local/p/web/a/coder-1")
    page.get_by_role("tab", name="Logs").click()
    expect(page.get_by_test_id("text")).to_contain_text("log of coder-1")


def test_the_timeline_of_a_project(single_hub, page):
    sign_in(page, single_hub, "root")
    page.goto(single_hub.url + "/e/local/p/web/timeline")
    expect(page.get_by_test_id("timeline")).to_contain_text("refactoring the parser on local")


def test_the_command_menu_opens_an_agent_terminal(single_hub, page):
    sign_in(page, single_hub, "root")
    page.keyboard.press("Control+k")
    page.get_by_role("combobox", name="Command").fill("coder-1")
    page.keyboard.press("Enter")
    expect(terminal_text(page, "coder-1")).to_contain_text("attached to web/coder-1")


def test_signing_out_ends_the_session(single_hub, page):
    sign_in(page, single_hub, "root")
    page.get_by_role("button", name="Sign out").click()
    expect(page.get_by_role("link", name="Sign in")).to_be_visible()
    page.goto(single_hub.url + "/")
    expect(page).to_have_url(re.compile(r"/login\?next="))


def test_the_pages_fit_a_phone(single_hub, browser):
    context = browser.new_context(viewport={"width": 390, "height": 800})
    page = context.new_page()
    sign_in_phone(page, single_hub)
    assert page.evaluate("document.documentElement.scrollWidth") <= 392, "the page scrolls sideways on a phone"
    page.get_by_role("button", name="Open menu").click()
    page.locator(".side").get_by_role("link", name="web").first.click()
    expect(page.get_by_role("heading", name="web")).to_be_visible()
    assert page.evaluate("document.documentElement.scrollWidth") <= 392, "the project page scrolls sideways on a phone"
    context.close()


def sign_in_phone(page, server):
    page.goto(server.url + "/login")
    page.get_by_role("link", name="Sign in").click()
    page.get_by_label("Who are you?").fill("root")
    page.get_by_role("button", name="Sign in").click()
    expect(page.get_by_role("heading", name="Projects")).to_be_visible()
