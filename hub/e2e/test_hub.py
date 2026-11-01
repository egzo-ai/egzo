import re

from playwright.sync_api import expect

from conftest import ADMIN_PASSWORD, sign_in


def projects_table(page):
    return page.get_by_test_id("projects")


def spawn(page, name, template=None):
    """Spawns from the project page: the dialog closes at once and the agent's page shows it being made."""
    page.get_by_role("button", name="Spawn agent").click()
    dialog = page.get_by_role("dialog")
    if template:
        dialog.get_by_label("Template").select_option(template)
    dialog.get_by_label("Name").fill(name)
    dialog.get_by_role("button", name="Spawn", exact=True).click()
    expect(page.get_by_role("dialog")).to_have_count(0)
    expect(page.get_by_role("heading", name=name)).to_be_visible()


def close_terminal(page, agent):
    """Closes the terminal that opened on a new agent once it was ready, back to its page."""
    expect(terminal_state(page, agent)).to_have_text("connected")
    page.locator(f".tpane[aria-label='Terminal of {agent}']").get_by_role("button", name="Close terminal").click()


def terminal_text(page, agent="coder-1"):
    return page.locator(f".tpane[aria-label='Terminal of {agent}'] .xterm-rows")


def terminal_state(page, agent="coder-1"):
    return page.locator(f".tpane[aria-label='Terminal of {agent}']").get_by_test_id("terminal-state")


def test_the_starter_account_is_the_operator_and_sees_every_project(hub, page):
    sign_in(page, hub, "admin")
    expect(page.get_by_test_id("signed-in-as")).to_contain_text("operator")
    expect(page.get_by_role("heading", name="Projects")).to_be_visible()
    expect(projects_table(page).locator("tbody tr")).to_have_count(4)  # web and data on local, web and ml on lab
    expect(projects_table(page).get_by_role("link", name="data")).to_be_visible()
    expect(projects_table(page).get_by_role("link", name="ml")).to_be_visible()
    expect(projects_table(page).locator("tbody tr").first).to_contain_text("owner")  # it made the codes, so it owns them, like anybody
    # two machines: each project says where it runs, and the two called web are two projects
    expect(projects_table(page).locator("tr[data-project=web]")).to_have_count(2)
    expect(projects_table(page).locator("tr[data-project=web]").filter(has_text="lab")).to_have_count(1)


def test_the_filter_finds_a_project_by_its_name_or_its_machine(hub, page):
    sign_in(page, hub, "admin")
    page.get_by_label("Filter projects").fill("lab")
    expect(projects_table(page).locator("tbody tr")).to_have_count(2)
    expect(projects_table(page).get_by_role("link", name="ml")).to_be_visible()
    expect(projects_table(page).get_by_role("link", name="data")).to_have_count(0)


def test_two_projects_of_one_name_are_two_projects(hub, page):
    sign_in(page, hub, "admin")
    page.goto(hub.url + hub.p("web", machine="lab"))
    expect(page.get_by_test_id("host")).to_have_text("lab")
    expect(page.locator("tr[data-agent=coder-1]")).to_contain_text("refactoring the parser on lab")
    page.goto(hub.url + hub.p("web"))
    expect(page.locator("tr[data-agent=coder-1]")).to_contain_text("refactoring the parser on local")


def test_a_project_whose_link_went_away_is_offline_with_what_was_last_known_and_comes_back(single_hub, page):
    sign_in(page, single_hub, "admin")
    page.goto(single_hub.url + single_hub.p("web"))
    expect(page.locator("tr[data-agent=coder-1]")).to_be_visible()
    single_hub.world("down", project="web")
    banner = page.get_by_test_id("offline")
    expect(banner).to_contain_text("Offline since", timeout=8000)
    expect(page.get_by_role("button", name="Spawn agent")).to_have_count(0)
    expect(page.locator("tr[data-agent=coder-1]")).to_be_visible()
    expect(page.get_by_role("button", name="Stop coder-1")).to_have_count(0)
    page.goto(single_hub.url)
    expect(projects_table(page).locator("tr[data-project=web]")).to_contain_text("offline")
    expect(projects_table(page).get_by_role("link", name="data")).to_be_visible()
    single_hub.world("up", project="web")
    expect(projects_table(page).locator("tr[data-project=web]")).not_to_contain_text("offline", timeout=8000)


def test_a_task_that_ran_when_the_link_dropped_is_interrupted(single_hub, page):
    sign_in(page, single_hub, "admin")
    page.goto(single_hub.url + single_hub.p("web"))
    single_hub.world("hold", project="web")
    page.get_by_role("button", name="Stop coder-1").click()
    row = page.locator("tr[data-agent=coder-1]")
    expect(row.get_by_test_id("task")).to_contain_text("stopping")
    single_hub.world("down", project="web")
    expect(row.get_by_test_id("task-error")).to_contain_text("link dropped", timeout=8000)
    single_hub.world("release", project="web")


def test_a_terminal_says_when_the_link_drops(single_hub, page):
    sign_in(page, single_hub, "admin")
    page.goto(single_hub.url + single_hub.p("web"))
    page.get_by_role("button", name="Attach coder-1").click()
    expect(terminal_state(page)).to_have_text("connected")
    single_hub.world("down", project="web")
    expect(page.locator(".tpane[aria-label='Terminal of coder-1'] .tcard")).to_contain_text("link", timeout=8000)


def make_code(page, hub):
    page.goto(hub.url + "/settings/links")
    page.get_by_role("button", name="Link a project").click()
    return page.get_by_test_id("link-code").inner_text()


def test_an_operator_makes_a_code_and_a_project_registers_with_it(single_hub, page):
    sign_in(page, single_hub, "admin")
    code = make_code(page, single_hub)
    assert code.startswith("egzo-link-")
    shown = page.get_by_test_id("new-code")
    expect(shown).to_contain_text("shown once")
    expect(shown).to_contain_text("hub: ")
    expect(page.get_by_test_id("link-command")).to_contain_text("egzo project register")
    expect(page.get_by_test_id("link-codes").locator("tr[data-code]")).to_have_count(1)

    single_hub.world("register", project="fresh", machine="laptop", code=code)
    verdict = single_hub.world("verdict", project="fresh", machine="laptop")
    assert verdict["State"] == "approved" and verdict["Owner"] == "admin"
    expect(page.get_by_test_id("links").locator("tr[data-link=fresh]")).to_contain_text("approved", timeout=8000)
    expect(page.get_by_test_id("link-codes")).to_have_count(0)  # the code is spent

    page.reload()
    expect(page.get_by_test_id("new-code")).to_have_count(0)  # and it is never shown again


def test_a_code_that_is_cancelled_registers_nobody(single_hub, page):
    sign_in(page, single_hub, "admin")
    code = make_code(page, single_hub)
    page.get_by_test_id("link-codes").get_by_role("button", name=re.compile("^Cancel code")).click()
    expect(page.get_by_test_id("link-codes")).to_have_count(0)
    expect(page.get_by_test_id("new-code")).to_have_count(0)
    single_hub.world("register", project="fresh", machine="laptop", code=code)
    assert single_hub.world("verdict", project="fresh", machine="laptop")["State"] == "unregistered"
    expect(page.get_by_test_id("links").locator("tr[data-link=fresh]")).to_have_count(0)


def test_a_person_with_no_quota_makes_no_code(single_hub, page):
    sign_in(page, single_hub, "alice")
    answer = page.request.post(single_hub.url + "/api/v1/links/codes", data={}, headers={"Origin": single_hub.url})
    assert answer.status == 409


def test_the_address_a_link_used_to_show_is_gone(single_hub, page):
    sign_in(page, single_hub, "admin")
    page.goto(single_hub.url + "/link/QXRB-7KPM")
    expect(page.get_by_test_id("signed-in-as")).to_be_visible()
    expect(page.get_by_role("button", name="Approve")).to_have_count(0)


def test_a_revoked_project_can_be_forgotten(single_hub, page):
    sign_in(page, single_hub, "admin")
    single_hub.world("add-project", project="fresh")
    page.goto(single_hub.url + "/settings/links")
    row = page.get_by_test_id("links").locator("tr[data-link=fresh]")
    row.get_by_role("button", name="Revoke fresh").click()
    dialog = page.get_by_role("dialog")
    dialog.get_by_label("Type").fill("fresh")
    dialog.get_by_role("button", name="Revoke").click()
    expect(row).to_contain_text("revoked")
    row.get_by_role("button", name="Forget fresh").click()
    page.get_by_role("dialog").get_by_role("button", name="Forget").click()
    expect(row).to_have_count(0)


def test_the_timeline_of_a_project_that_is_offline_says_so(single_hub, page):
    sign_in(page, single_hub, "admin")
    single_hub.world("down", project="web")
    page.goto(single_hub.url + single_hub.p("web") + "/timeline")
    expect(page.get_by_test_id("timeline-offline")).to_be_visible()
    page.goto(single_hub.url + single_hub.p("web") + "/a/coder-1")
    expect(page.get_by_test_id("timeline-offline")).to_be_visible()
    expect(page.get_by_text("Loading…")).to_have_count(0)
    single_hub.world("up", project="web")


def test_an_operator_revokes_a_project_and_it_leaves_the_hub(single_hub, page):
    sign_in(page, single_hub, "admin")
    page.get_by_role("link", name=re.compile("^Settings")).click()
    expect(page.get_by_role("tab", name="Links")).to_have_attribute("aria-selected", "true")
    links = page.get_by_test_id("links")
    expect(links.locator("tr[data-link=web]")).to_contain_text("approved")
    links.get_by_role("button", name="Revoke web").click()
    dialog = page.get_by_role("dialog")
    expect(dialog.get_by_role("button", name="Revoke")).to_be_disabled()
    dialog.get_by_label("Type").fill("web")
    dialog.get_by_role("button", name="Revoke").click()
    expect(links.locator("tr[data-link=web]")).to_contain_text("revoked")
    assert single_hub.world("verdict", project="web")["State"] == "revoked"
    page.goto(single_hub.url)
    expect(projects_table(page).get_by_role("link", name="web")).to_have_count(0)


def test_a_project_that_unlinks_leaves_the_hub(single_hub, page):
    sign_in(page, single_hub, "admin")
    single_hub.world("unlink", project="data")
    expect(projects_table(page).get_by_role("link", name="data")).to_have_count(0, timeout=8000)


def test_a_newcomer_sees_nothing_until_an_owner_adds_them(hub, page, new_person):
    sign_in(page, hub, "admin")
    alice = new_person("alice")
    expect(alice.get_by_text("have a role on any project")).to_be_visible()
    alice.goto(hub.url + hub.p("web"))
    expect(alice.get_by_role("heading", name="Not found")).to_be_visible()

    page.goto(hub.url + hub.p("web") + "/members")
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


def test_an_owner_cannot_remove_themselves(hub, page, new_person):
    sign_in(page, hub, "admin")
    alice = new_person("alice")
    hub.world("grant", project="web", user="alice", role="owner")
    alice.goto(hub.url + hub.p("web") + "/members")
    mine = alice.locator("tr[data-user]").filter(has_text="alice@example.com")
    expect(mine.get_by_role("button", name="Remove")).to_have_count(0)
    expect(mine.get_by_role("combobox")).to_be_disabled()
    # the operator who connected it is an owner like any other, and a co-owner can change that
    other = alice.locator("tr[data-user]").filter(has_text="admin")
    expect(other.get_by_role("button", name="Remove admin")).to_be_visible()


def test_removing_a_member_asks_first(hub, page, new_person):
    sign_in(page, hub, "admin")
    new_person("alice")
    hub.world("grant", project="web", user="alice", role="user")
    page.goto(hub.url + hub.p("web") + "/members")
    expect(page.locator("tr[data-user]")).to_have_count(2)
    page.get_by_role("button", name="Remove alice").click()
    page.get_by_role("dialog").get_by_role("button", name="Cancel").click()
    expect(page.locator("tr[data-user]")).to_have_count(2)
    page.get_by_role("button", name="Remove alice").click()
    page.get_by_role("dialog").get_by_role("button", name="Remove from the project").click()
    expect(page.locator("tr[data-user]")).to_have_count(1)


def test_spawn_stop_start_and_remove_an_agent(single_hub, page):
    sign_in(page, single_hub, "admin")
    projects_table(page).get_by_role("link", name="web").click()
    spawn(page, "fix-bug", "reviewer")
    # once made, its terminal opens: the user asked for an agent to work with
    close_terminal(page, "fix-bug")
    expect(page.get_by_test_id("activity")).to_have_text("idle")
    expect(page.get_by_test_id("task")).to_have_count(0)

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
    # the project's list, where the agent is until it is gone; that was seen, so no toast says it
    expect(page.locator("tr[data-agent=fix-bug]")).to_have_count(0)
    expect(page.locator("tr[data-agent=coder-1]")).to_be_visible()
    expect(page.get_by_role("status").filter(has_text="Removed fix-bug")).to_have_count(0)
    assert single_hub.calls()[:1] and single_hub.calls()[0].startswith("spawn fix-bug user:")


def test_the_spawn_dialog_suggests_a_name_that_is_not_a_number(single_hub, page):
    sign_in(page, single_hub, "admin")
    page.goto(single_hub.url + single_hub.p("web"))
    page.get_by_role("button", name="Spawn agent").click()
    name = page.get_by_role("dialog").get_by_label("Name")
    expect(name).to_have_value(re.compile(r"^coder-[a-z]+[0-9]{4}$"))
    first = name.input_value()
    page.get_by_role("dialog").get_by_label("Template").select_option("reviewer")
    expect(name).to_have_value(re.compile(r"^reviewer-[a-z]+[0-9]{4}$"))  # a suggestion follows the template
    page.get_by_role("dialog").get_by_role("button", name="Another suggestion").click()
    expect(name).not_to_have_value(first)
    name.fill("mine")
    page.get_by_role("dialog").get_by_label("Template").select_option("coder")
    expect(name).to_have_value("mine")  # a name the user typed stays
    name.fill("coder-1")
    page.get_by_role("dialog").get_by_role("button", name="Spawn", exact=True).click()
    expect(page.get_by_role("dialog").get_by_role("alert")).to_contain_text("There is already an agent named coder-1")


def test_a_slow_spawn_is_one_agent_from_the_start_and_its_page_shows_the_steps(single_hub, page):
    sign_in(page, single_hub, "admin")
    projects_table(page).get_by_role("link", name="web").click()
    single_hub.world("hold", project="web")
    spawn(page, "slow-one")
    expect(page.get_by_test_id("task")).to_have_text("spawning")
    expect(page.get_by_test_id("task-step")).to_have_text("create network web-slow-one")
    # the project lists it once, as the agent it will be, with who asked
    page.get_by_role("navigation", name="Breadcrumb").get_by_role("link", name="web").click()
    row = page.locator("tr[data-agent=slow-one]")
    expect(row.get_by_test_id("task")).to_have_text("spawning")
    expect(row).to_contain_text("admin")
    expect(row).to_have_attribute("aria-busy", "true")

    single_hub.world("release", project="web")
    expect(row.get_by_test_id("task")).to_have_count(0)
    expect(row).to_contain_text("idle")
    expect(page.locator("tr[data-agent=slow-one]")).to_have_count(1)


def test_a_task_away_from_its_agent_has_a_toast_and_a_failure_stays_on_the_agent(single_hub, page):
    sign_in(page, single_hub, "admin")
    page.goto(single_hub.url + single_hub.p("web"))
    single_hub.world("hold", project="web")
    page.locator("tr[data-agent=coder-1]").get_by_role("button", name="Stop coder-1").click()
    row = page.locator("tr[data-agent=coder-1]")
    expect(row.get_by_test_id("task")).to_have_text("stopping")
    expect(row.get_by_role("button", name="Remove coder-1")).to_be_disabled()  # one task at a time
    # away from the agent, a toast says what runs, then how it ended
    page.get_by_role("link", name="Projects").first.click()
    expect(page.get_by_role("status").filter(has_text="Stopping coder-1")).to_be_visible()
    single_hub.world("release", project="web")
    expect(page.get_by_role("status").filter(has_text="Stopped coder-1")).to_be_visible()

    # a failure is on the agent, with why, until somebody who may dismisses it; the activity keeps it
    page.goto(single_hub.url + single_hub.p("web"))
    single_hub.world("fail-next", project="web", state="no room on the machine")
    page.locator("tr[data-agent=coder-1]").get_by_role("button", name="Start coder-1").click()
    expect(row.get_by_test_id("task")).to_have_text("start failed")
    expect(row.get_by_test_id("task-error")).to_have_text("no room on the machine")
    expect(page.get_by_role("alert").filter(has_text="Could not start coder-1: no room on the machine")).to_be_visible()
    row.get_by_role("button", name="Dismiss the failure on coder-1").click()
    expect(row.get_by_test_id("task")).to_have_count(0)
    page.get_by_role("tab", name="Activity").click()
    activity = page.get_by_test_id("activity")
    expect(activity.locator("tr").nth(1)).to_contain_text("start failed")
    expect(activity.locator("tr").nth(2)).to_contain_text("done")


def test_the_page_follows_the_agent_without_reloading(single_hub, page):
    sign_in(page, single_hub, "admin")
    projects_table(page).get_by_role("link", name="web").click()
    row = page.locator("tr[data-agent=coder-1]")
    expect(row).to_contain_text("refactoring the parser on local")
    single_hub.world("set-agent", project="web", agent="coder-1", state="working", text="writing the tests", waiting=True)
    expect(row).to_contain_text("writing the tests", timeout=8000)
    expect(row).to_contain_text("waiting for you")
    # the sidebar counts the questions of every project
    expect(page.locator(".side .qcount").first).to_be_visible()
    row.get_by_role("link", name="coder-1").click()
    expect(page.get_by_test_id("waiting")).to_be_visible()
    expect(page.get_by_test_id("status")).to_have_text("writing the tests")
    single_hub.world("set-agent", project="web", agent="coder-1", state="idle", text="done")
    expect(page.get_by_test_id("status")).to_have_text("done", timeout=8000)
    expect(page.get_by_test_id("waiting")).to_have_count(0)


def test_there_is_no_messaging_in_the_web_ui(single_hub, page):
    sign_in(page, single_hub, "admin")
    page.goto(single_hub.url + single_hub.p("web") + "/a/coder-1")
    expect(page.get_by_role("heading", name="coder-1")).to_be_visible()
    expect(page.locator("textarea")).to_have_count(0)
    expect(page.get_by_role("button", name=re.compile("send", re.I))).to_have_count(0)
    expect(page.get_by_role("button", name="Attach", exact=True)).to_be_visible()


def test_the_terminal_types_into_the_agent_and_a_second_person_can_take_it_over(hub, page, new_person):
    sign_in(page, hub, "admin")
    page.goto(hub.url + hub.p("web") + "/a/coder-1")
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
    hub.world("grant", project="web", user="alice", role="admin")
    alice.goto(hub.url + hub.p("web") + "/a/coder-1")
    alice.get_by_role("button", name="Attach", exact=True).click()
    expect(terminal_state(alice)).to_have_text("in use by someone else")
    expect(alice.get_by_role("status")).to_contain_text("admin is at this terminal")
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
    sign_in(page, single_hub, "admin")
    single_hub.world("set-agent", project="web", agent="coder-2", state="idle", text="second")
    page.goto(single_hub.url + single_hub.p("web"))
    page.locator("tr[data-agent=coder-1]").get_by_role("button", name="Attach coder-1").click()
    expect(terminal_text(page, "coder-1")).to_contain_text("attached to web/coder-1")
    page.get_by_role("link", name="All projects", exact=True).click()  # a page is shown; the terminal stays open
    expect(page.get_by_role("heading", name="Projects")).to_be_visible()
    page.goto(single_hub.url + single_hub.p("web"))  # even a reload brings it back
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
    sign_in(page, hub, "admin")
    bob = new_person("bob")
    hub.world("grant", project="web", user="bob", role="user")
    bob.goto(hub.url + hub.p("web") + "/a/coder-1")
    expect(bob.get_by_role("heading", name="coder-1")).to_be_visible()
    expect(bob.get_by_role("button", name="Attach", exact=True)).to_have_count(0)
    expect(bob.get_by_text("Only the people who may operate")).to_be_visible()


def test_a_user_cannot_touch_an_agent_the_operator_spawned(hub, page, new_person):
    sign_in(page, hub, "admin")
    alice = new_person("alice")
    hub.world("grant", project="web", user="alice", role="user")
    alice.goto(hub.url + hub.p("web"))
    expect(alice.locator("tr[data-agent=coder-1]")).to_be_visible()
    expect(alice.get_by_role("button", name="Stop coder-1")).to_have_count(0)
    expect(alice.get_by_role("button", name="Remove coder-1")).to_have_count(0)
    # but a user's own agent is theirs
    spawn(alice, "alices")
    alice.goto(hub.url + hub.p("web"))
    mine = alice.locator("tr[data-agent=alices]")
    expect(mine.get_by_role("button", name="Stop alices")).to_be_visible()
    expect(mine.get_by_role("button", name="Remove alices")).to_be_visible()
    alice.goto(hub.url + hub.p("web"))
    expect(alice.locator("tr[data-agent=alices]").get_by_role("button", name="Stop alices")).to_be_visible()
    expect(alice.locator("tr[data-agent=coder-1]").get_by_role("button", name="Stop coder-1")).to_have_count(0)


def test_an_operator_holds_a_project_as_soon_as_it_is_registered(single_hub, page):
    sign_in(page, single_hub, "admin")
    single_hub.world("add-project", project="fresh")
    expect(projects_table(page).get_by_role("link", name="fresh")).to_be_visible(timeout=10000)  # the page asks every three seconds


def test_a_project_whose_sidecar_logs_no_status_says_so_and_shows_its_agents_as_running(single_hub, page):
    sign_in(page, single_hub, "admin")
    single_hub.world("add-project", project="mute", state="silent")
    single_hub.world("set-agent", project="mute", agent="coder-1", state="working")
    page.goto(single_hub.url + single_hub.p("mute"))
    agent = page.get_by_test_id("agents").locator("tr[data-agent=coder-1]")
    expect(agent).to_be_visible()
    # nothing has been heard yet: the agent is not called "starting", and there is nothing to warn about yet
    expect(agent).not_to_contain_text("starting")
    # after a while of silence the page says why it cannot tell what the agent is doing
    expect(page.get_by_test_id("feed-banner")).to_contain_text("writes no status", timeout=15000)
    expect(agent).to_contain_text("running")
    expect(agent).not_to_contain_text("starting")


def test_a_project_with_a_status_log_shows_what_its_agents_do_and_no_banner(single_hub, page):
    sign_in(page, single_hub, "admin")
    page.goto(single_hub.url + single_hub.p("web"))
    expect(page.locator("tr[data-agent=coder-1]")).to_contain_text("working")
    expect(page.get_by_test_id("feed-banner")).to_have_count(0)


def test_only_an_operator_sees_the_settings(hub, page, new_person):
    sign_in(page, hub, "admin")
    alice = new_person("alice")
    expect(alice.get_by_role("link", name=re.compile("^Settings"))).to_have_count(0)
    for path in ("/settings/links", "/settings/users"):
        alice.goto(hub.url + path)
        expect(alice.get_by_role("heading", name="Forbidden")).to_be_visible()
    # settings alone lands on the first tab
    page.goto(hub.url + "/settings")
    expect(page).to_have_url(hub.url + "/settings/links")
    expect(page.get_by_test_id("links")).to_be_visible()


def test_an_operator_makes_another_person_an_operator_and_the_last_one_stays(hub, page, new_person):
    sign_in(page, hub, "admin")
    alice = new_person("alice")
    expect(alice.get_by_role("link", name=re.compile("^Settings"))).to_have_count(0)

    page.get_by_role("link", name=re.compile("^Settings")).click()
    page.get_by_role("tab", name="Users").click()
    users = page.get_by_test_id("users")
    expect(users.locator("tr[data-user]")).to_have_count(2)
    root_row = users.locator("tr[data-user]").filter(has=page.locator("b", has_text=re.compile("^admin$")))
    alice_row = users.locator("tr[data-user]").filter(has_text="alice@example.com")
    # the only operator cannot be removed
    expect(root_row.get_by_role("button", name="Remove operator from admin")).to_be_disabled()
    expect(alice_row.get_by_test_id("operator-badge")).to_have_count(0)

    alice_row.get_by_role("button", name="Make alice an operator").click()
    page.get_by_role("dialog").get_by_role("button", name="Cancel").click()
    expect(alice_row.get_by_test_id("operator-badge")).to_have_count(0)
    alice_row.get_by_role("button", name="Make alice an operator").click()
    page.get_by_role("dialog").get_by_role("button", name="Make operator").click()
    expect(page.get_by_role("status")).to_contain_text("alice is an operator")
    expect(alice_row.get_by_test_id("operator-badge")).to_be_visible()

    # alice now has the settings, and oversees every project
    alice.goto(hub.url + "/")
    expect(alice.get_by_role("link", name=re.compile("^Settings"))).to_be_visible()
    expect(projects_table(alice).locator("tbody tr")).to_have_count(4)  # every project of both machines, none of them granted
    expect(projects_table(alice).locator("tbody tr").first).to_contain_text("operator")

    # with two operators, admin may step down, and is then no operator
    expect(root_row.get_by_role("button", name="Remove operator from admin")).to_be_enabled()
    root_row.get_by_role("button", name="Remove operator from admin").click()
    page.get_by_role("dialog").get_by_role("button", name="Remove operator").click()
    expect(page.get_by_role("heading", name="Forbidden")).to_be_visible()
    expect(page.get_by_role("link", name=re.compile("^Settings"))).to_have_count(0)

    # alice is the last one
    alice.goto(hub.url + "/settings/users")
    last = alice.get_by_test_id("users").locator("tr[data-user]").filter(has_text="alice@example.com")
    expect(last.get_by_role("button", name="Remove operator from alice")).to_be_disabled()


def test_the_audit_log_shows_who_did_what_and_what_was_refused(hub, page, new_person):
    sign_in(page, hub, "admin")
    alice = new_person("alice")
    hub.world("grant", project="web", user="alice", role="user")
    alice.goto(hub.url + hub.p("web"))
    spawn(alice, "alices")
    expect(alice.get_by_test_id("activity")).to_have_text("idle")  # made
    alice.request.post(hub.url + "/api/v1" + hub.p("web") + "/a/coder-1/rm", data={}, headers={"Origin": hub.url})  # refused: not alice's
    page.goto(hub.url + hub.p("web") + "/audit")
    audit = page.get_by_test_id("audit")
    expect(audit).to_contain_text("agent.spawn")
    expect(audit.locator("tr.refused")).to_contain_text("agent.rm")
    expect(audit.locator("tr.refused")).to_contain_text("alice")


def test_an_operator_reads_what_was_done_on_the_whole_hub(hub, page, new_person):
    sign_in(page, hub, "admin")
    alice = new_person("alice")
    alice.goto(hub.url + "/settings/audit")
    expect(alice.get_by_role("heading", name="Forbidden")).to_be_visible()

    page.goto(hub.url + "/settings/audit")
    audit = page.get_by_test_id("audit")
    expect(audit).to_contain_text("user.signin")
    expect(audit.locator("tbody tr").first).to_be_visible()


def test_the_spec_and_the_logs_are_read_only_text(single_hub, page):
    sign_in(page, single_hub, "admin")
    page.goto(single_hub.url + single_hub.p("web"))
    page.get_by_role("tab", name="Spec").click()
    expect(page.get_by_test_id("text")).to_contain_text("name: web")
    page.goto(single_hub.url + single_hub.p("web") + "/a/coder-1")
    page.get_by_role("tab", name="Logs").click()
    expect(page.get_by_test_id("text")).to_contain_text("log of coder-1")


def test_the_timeline_of_a_project(single_hub, page):
    sign_in(page, single_hub, "admin")
    page.goto(single_hub.url + single_hub.p("web") + "/timeline")
    expect(page.get_by_test_id("timeline")).to_contain_text("refactoring the parser on local")


def test_the_command_menu_opens_an_agent_terminal(single_hub, page):
    sign_in(page, single_hub, "admin")
    page.keyboard.press("Control+k")
    page.get_by_role("combobox", name="Command").fill("coder-1")
    # an agent is listed once its link has pushed it, which can be after the sign-in, and Enter on an empty list only closes the menu
    expect(page.get_by_role("dialog", name="Command menu")).to_contain_text("web/coder-1")
    page.keyboard.press("Enter")
    expect(terminal_text(page, "coder-1")).to_contain_text("attached to web/coder-1")


def test_signing_out_ends_the_session(single_hub, page):
    sign_in(page, single_hub, "admin")
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
    page.get_by_label("Username").fill("admin")
    page.get_by_label("Password").fill(ADMIN_PASSWORD)
    page.get_by_role("button", name="Sign in", exact=True).click()
    expect(page.get_by_role("heading", name="Projects")).to_be_visible()


def test_a_wrong_password_is_refused_and_the_right_one_signs_in(hub, page):
    page.goto(hub.url + "/login")
    page.get_by_label("Password").fill("not the password")
    page.get_by_role("button", name="Sign in", exact=True).click()
    expect(page.get_by_role("alert")).to_contain_text("Wrong username or password")
    page.get_by_label("Password").fill(ADMIN_PASSWORD)
    page.get_by_role("button", name="Sign in", exact=True).click()
    expect(page.get_by_test_id("signed-in-as")).to_contain_text("admin")
    expect(page.get_by_role("heading", name="Projects")).to_be_visible()


def test_the_starter_account_links_single_sign_on_and_then_signs_in_with_it(hub, page, browser):
    sign_in(page, hub, "admin")
    page.get_by_test_id("signed-in-as").click()
    account = page.get_by_test_id("account")
    account.get_by_role("button", name="Link Test login").click()
    page.get_by_label("Who are you?").fill("boss")
    page.get_by_role("button", name="Sign in").click()
    expect(page.get_by_test_id("linked-as")).to_contain_text("boss@example.com")
    # unlinking is allowed: the password is still a way in
    expect(page.get_by_test_id("linked-as").get_by_role("button", name="Unlink")).to_be_enabled()

    context = browser.new_context()
    other = context.new_page()
    other.goto(hub.url + "/login")
    other.get_by_role("link", name="Sign in with Test login").click()
    other.get_by_label("Who are you?").fill("boss")
    other.get_by_role("button", name="Sign in").click()
    expect(other.get_by_test_id("signed-in-as")).to_contain_text("admin · operator")
    context.close()


def test_a_disabled_person_is_signed_out_and_cannot_sign_in_again(hub, page, new_person):
    sign_in(page, hub, "admin")
    alice = new_person("alice")
    page.goto(hub.url + "/settings/users")
    row = page.get_by_test_id("users").locator("tr[data-user]").filter(has_text="alice@example.com")
    row.get_by_role("button", name="Disable alice").click()
    page.get_by_role("dialog").get_by_role("button", name="Disable").click()
    expect(page.get_by_role("status")).to_contain_text("alice is disabled")
    # disabled users are hidden behind a filter
    expect(row).to_have_count(0)
    page.get_by_label(re.compile("Show disabled users")).check()
    expect(row.get_by_test_id("disabled-badge")).to_be_visible()

    alice.reload()
    expect(alice.get_by_role("link", name="Sign in with Test login")).to_be_visible()
    alice.get_by_role("link", name="Sign in with Test login").click()
    alice.get_by_label("Who are you?").fill("alice")
    alice.get_by_role("button", name="Sign in").click()
    expect(alice.get_by_role("alert")).to_contain_text("Sign-in refused")


def test_the_network_tab_says_why_a_host_was_denied_how_to_fix_it_and_when_it_is_fixed(single_hub, page):
    for _ in range(2):
        single_hub.world("audit", project="web", agent="coder-1", host="registry.npmjs.org", decision="deny", code="not-in-profile",
                         text="not allowed by the agent's egress profile")
    single_hub.world("audit", project="web", agent="coder-1", host="example.com", decision="allow", mode="tunnel")
    sign_in(page, single_hub, "admin")
    page.goto(single_hub.url + single_hub.p("web"))
    denied = page.locator('tr[data-agent="coder-1"]').get_by_test_id("denied")
    expect(denied).to_have_text("1 denied")
    denied.click()
    expect(page).to_have_url(single_hub.url + single_hub.p("web") + "/a/coder-1/network")

    row = page.get_by_test_id("denials").locator('tr[data-host="registry.npmjs.org"]')
    expect(row).to_contain_text("not in the profile")
    expect(row.locator("td").nth(2)).to_have_text("2")
    row.get_by_role("button", name="How to fix").click()
    expect(page.get_by_test_id("fix-yaml")).to_have_text("egress:\n  default:\n    allow: [registry.npmjs.org]")
    expect(page.get_by_test_id("fix")).to_contain_text("egzo project up")
    connections = page.get_by_test_id("connections")
    expect(connections.locator('tr[data-host="example.com"]')).to_contain_text("allow")
    page.get_by_label("denied and failed only").check()
    expect(connections.locator('tr[data-host="example.com"]')).to_have_count(0)

    # the profile was changed and the agent got through: the denial is fixed, and the list stops counting it
    single_hub.world("audit", project="web", agent="coder-1", host="registry.npmjs.org", decision="allow", mode="tunnel")
    expect(page.get_by_test_id("denials")).to_contain_text("Nothing was denied that is not fixed since")
    page.get_by_label("show fixed").check()
    expect(row).to_contain_text("fixed")
    page.goto(single_hub.url + single_hub.p("web"))
    expect(page.locator('tr[data-agent="coder-1"]').get_by_test_id("denied")).to_have_count(0)


def test_the_network_tab_is_in_the_sidebar_and_says_where_a_connection_came_from_and_who_it_only_claimed_to_be(single_hub, page):
    single_hub.world("audit", project="web", agent="coder-1", host="example.com", decision="allow", mode="tunnel")
    single_hub.world("audit", project="web", agent="coder-1", host="api.example.com", decision="deny", code="credentials",
                     text="invalid proxy credentials")
    sign_in(page, single_hub, "admin")
    page.goto(single_hub.url + single_hub.p("web"))
    page.get_by_role("navigation", name="Main").get_by_role("link", name="Network").click()
    expect(page).to_have_url(single_hub.url + single_hub.p("web") + "/network")

    connections = page.get_by_test_id("connections")
    proved = connections.locator('tr[data-host="example.com"]')
    expect(proved.locator("td").nth(1)).to_have_text("coder-1")
    proved.click()
    expect(page.get_by_test_id("from")).to_contain_text("From 10.89.3.2, curl/8.11.1")
    proved.click()

    claimed = connections.locator('tr[data-host="api.example.com"]')
    expect(claimed.locator("td").nth(1)).to_have_text("coder-1?")
    claimed.click()
    expect(page.get_by_test_id("from")).to_contain_text("claimed to be coder-1, not proved")
    denial = page.get_by_test_id("denials").locator('tr[data-host="api.example.com"]')
    expect(denial).to_contain_text("coder-1? (unauthenticated)")
    denial.get_by_role("button", name="How to fix").click()
    expect(page.get_by_test_id("denial-from")).to_have_text("Latest from 10.89.3.2, curl/8.11.1, claimed to be coder-1, not proved.")


def test_the_network_tab_shows_which_requests_carried_a_credential_and_why_the_others_did_not(single_hub, page):
    outside = "not under the paths of service \"opencode\", so sent without its credential"
    single_hub.world("audit", project="web", agent="coder-1", host="opencode.ai", decision="allow", mode="inject", service="opencode",
                     requests=[{"path": "/zen/v1/chat", "injected": "opencode"}, {"path": "/inference/v1", "injected": "opencode"},
                               {"path": "/docs", "code": "outside-paths", "text": outside}])
    sign_in(page, single_hub, "admin")
    page.goto(single_hub.url + single_hub.p("web") + "/network")

    row = page.get_by_test_id("connections").locator('tr[data-host="opencode.ai"]')
    expect(row.get_by_test_id("injected")).to_have_text("opencode")
    expect(row.get_by_test_id("injected")).to_have_attribute("title", "the proxy set the credential of service opencode on 2 of 3 requests")
    expect(row.get_by_test_id("without-credential")).to_have_text("1 without credential")
    row.click()
    steps = page.get_by_test_id("step")
    expect(steps.nth(0)).to_contain_text("service opencode")
    expect(steps.nth(1)).to_contain_text("GET /zen/v1/chat")
    expect(steps.nth(1)).to_contain_text("opencode")
    expect(steps.nth(3)).to_contain_text("[outside-paths] " + outside)


def test_a_stranger_at_the_provider_is_refused_without_an_invite(hub, page):
    page.goto(hub.url + "/login")
    page.get_by_role("link", name="Sign in with Test login").click()
    page.get_by_label("Who are you?").fill("stranger")
    page.get_by_role("button", name="Sign in").click()
    expect(page.get_by_role("alert")).to_contain_text("takes an invite")


def test_an_owner_invites_somebody_with_a_link_that_works_once(hub, page, browser):
    sign_in(page, hub, "admin")
    page.goto(hub.url + hub.p("web") + "/members")
    page.get_by_label("Invite as").select_option("user")
    page.get_by_role("button", name="Make an invite").click()
    link = page.get_by_test_id("invite-link").inner_text()
    assert "/invite#egzo-invite-" in link
    expect(page.get_by_test_id("invites").locator("tr[data-invite]")).to_have_count(1)

    context = browser.new_context()
    guest = context.new_page()
    guest.goto(link)
    expect(guest.get_by_test_id("invite-grants")).to_contain_text("admin invites you to web as user")
    guest.get_by_role("button", name="Continue with Test login").click()
    guest.get_by_label("Who are you?").fill("guest")
    guest.get_by_role("button", name="Sign in").click()
    expect(guest.get_by_test_id("signed-in-as")).to_contain_text("guest")
    expect(projects_table(guest).get_by_role("link", name="web")).to_be_visible()

    second = context.new_page()
    second.goto(link)
    expect(second.get_by_test_id("invite-gone")).to_contain_text("no longer good")
    context.close()
    page.reload()
    expect(page.locator("tr[data-user]").filter(has_text="guest")).to_be_visible()
    expect(page.get_by_test_id("invites")).to_have_count(0)


def test_a_person_from_outside_makes_a_local_account_from_an_invite(hub, page, browser):
    sign_in(page, hub, "admin")
    page.goto(hub.url + "/settings/invites")
    page.get_by_label("Projects they may connect").fill("2")
    page.get_by_role("button", name="Make an invite").click()
    link = page.get_by_test_id("invite-link").inner_text()

    context = browser.new_context()
    guest = context.new_page()
    guest.goto(link)
    expect(guest.get_by_test_id("invite-grants")).to_contain_text("to connect up to 2 projects")
    guest.get_by_label("Choose a username").fill("guest")
    guest.get_by_label("Choose a password").fill("correct horse battery")
    guest.get_by_role("button", name="Create account").click()
    expect(guest.get_by_test_id("signed-in-as")).to_contain_text("guest")
    # a quota is what lets a person connect a project
    guest.goto(hub.url + "/settings/links")
    expect(guest.get_by_test_id("quota")).to_have_text("0 of 2 projects")
    guest.get_by_role("button", name="Link a project").click()
    expect(guest.get_by_test_id("link-code")).to_contain_text("egzo-link-")
    expect(guest.get_by_test_id("links")).to_have_count(0)
    context.close()


def test_a_person_signed_in_accepts_an_invite(hub, page, new_person):
    sign_in(page, hub, "admin")
    alice = new_person("alice")
    page.goto(hub.url + hub.p("web") + "/members")
    page.get_by_role("button", name="Make an invite").click()
    link = page.get_by_test_id("invite-link").inner_text()
    alice.goto(link)
    alice.get_by_role("button", name="Accept").click()
    expect(alice.get_by_role("heading", name="web")).to_be_visible()


def test_an_invite_can_be_revoked(hub, page, browser):
    sign_in(page, hub, "admin")
    page.goto(hub.url + "/settings/invites")
    page.get_by_role("button", name="Make an invite").click()
    link = page.get_by_test_id("invite-link").inner_text()
    page.get_by_test_id("invites").get_by_role("button", name=re.compile("^Revoke invite")).click()
    expect(page.get_by_text("No invite is waiting")).to_be_visible()
    context = browser.new_context()
    guest = context.new_page()
    guest.goto(link)
    expect(guest.get_by_test_id("invite-gone")).to_be_visible()
    context.close()


def test_an_operator_quota_and_a_person_over_it(hub, page, new_person):
    sign_in(page, hub, "admin")
    alice = new_person("alice")
    alice.goto(hub.url + "/settings/links")
    expect(alice.get_by_role("heading", name="Forbidden")).to_be_visible()
    page.goto(hub.url + "/settings/users")
    box = page.get_by_label("Project quota of alice")
    box.fill("1")
    page.get_by_test_id("users").locator("tr[data-user]").filter(has_text="alice@example.com").get_by_role("button", name="Save").click()
    expect(page.get_by_role("status")).to_contain_text("alice may connect 1 project")
    alice.reload()
    expect(alice.get_by_role("button", name="Link a project")).to_be_enabled()


def test_an_operator_goes_further_only_with_a_role_they_give_themselves(hub, page, new_person):
    sign_in(page, hub, "admin")
    alice = new_person("alice")
    page.goto(hub.url + "/settings/users")
    page.get_by_role("button", name="Make alice an operator").click()
    page.get_by_role("dialog").get_by_role("button", name="Make operator").click()
    expect(page.get_by_role("status")).to_contain_text("alice is an operator")

    alice.goto(hub.url + hub.p("web"))
    expect(alice.get_by_role("tab", name="Network")).to_have_count(0)
    expect(alice.get_by_role("tab", name="Spec")).to_have_count(0)
    expect(alice.get_by_role("button", name="Spawn agent")).to_have_count(0)
    expect(alice.get_by_test_id("agents").get_by_role("button", name=re.compile("^Attach"))).to_have_count(0)
    expect(alice.get_by_role("cell", name="coder-1")).to_be_visible()
    alice.get_by_role("tab", name="Members").click()
    alice.get_by_label("Your role").select_option("admin")
    alice.get_by_role("button", name="Give me the role").click()
    alice.get_by_role("dialog").get_by_role("button", name="Give me the role").click()
    expect(alice.get_by_role("status")).to_contain_text("Your role is set")
    expect(alice.get_by_test_id("members")).to_contain_text("alice")
    alice.goto(hub.url + hub.p("web") + "/audit")
    expect(alice.get_by_test_id("audit")).to_contain_text("role.set")
    alice.goto(hub.url + hub.p("web"))
    expect(alice.get_by_role("tab", name="Network")).to_be_visible()


def test_an_owner_unlinks_and_the_project_is_not_touched(hub, page):
    sign_in(page, hub, "admin")
    page.goto(hub.url + hub.p("data") + "/members")
    page.get_by_role("button", name="Unlink data").click()
    dialog = page.get_by_role("dialog")
    dialog.get_by_role("textbox").fill("data")
    dialog.get_by_role("button", name="Unlink").click()
    expect(page.get_by_role("status")).to_contain_text("Unlinked data")
    expect(projects_table(page).get_by_role("link", name="data")).to_have_count(0)
    assert not [c for c in (hub.calls("data") or []) if c.startswith(("rm", "stop"))]
    assert hub.world("verdict", project="data")["State"] == "unregistered"


def test_a_project_left_without_an_owner_is_shown_to_the_operators(hub, page, new_person):
    sign_in(page, hub, "admin")
    alice = new_person("alice")
    hub.world("grant", project="web", user="alice", role="owner")
    hub.world("grant", project="web", user="admin", role="user")
    page.goto(hub.url + "/settings/users")
    page.get_by_role("button", name="Disable alice").click()
    page.get_by_role("dialog").get_by_role("button", name="Disable").click()
    expect(page.get_by_role("status")).to_contain_text("alice is disabled")
    page.goto(hub.url + "/")
    expect(page.get_by_test_id("ownerless")).to_contain_text("web")
    page.goto(hub.url + "/settings/links")
    page.get_by_test_id("links").get_by_test_id("no-owner").first.click()
    expect(page.get_by_test_id("ownerless")).to_be_visible()
