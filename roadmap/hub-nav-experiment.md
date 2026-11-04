# Hub navigation experiment

An experiment on branch `egzo/claude-shinydune1088` (November 2026), shown on a preview hub. Tests were not updated: the
browser e2e (`hub/e2e/test_hub.py`) expects the old sidebar, the projects table and the old Agents tab.

## What changed

- **Two levels of nav.** After sign-in, the project list (tiles or list) with no left nav; a top bar holds what the sidebar foot
  held (settings, account, theme, sign out). Inside a project, the left nav is that project's only: its sections and its open
  terminals. Panes and the terminal shortcuts stay within the project.
- **Timeline removed**, down to the hub API (`GET /p/{project}/events`) and `links.Engine.Events`. The project link's `events` verb
  in core stays, unused by the hub; removing it is a core change with spec updates.
- **Templates and agents are two tabs.** Templates shows what each template gives an agent (harness, model, workspaces, egress
  profile, ports), read from the spec snapshot by `GET /p/{project}/templates`, with a Spawn agent button each. Agents is the flat
  list of agents, with their template.
- **No "N running of M"** and no bar on the project list: it read as a limit. Counts are by state.
- **No keyboard navigation on the Agents tab** (j/k, Enter, a, s).
- **No stats line or role badge** under a project page's heading.
- **An Advanced section** in the project's left nav (owners and operators), holding what is rarely needed. For now: **Unlink
  this project**, moved out of Members.
- **No question counts** on the project list, the top bar or the back button: messaging is not in the hub yet, so they were a
  piece of a feature that is missing. An agent's own "waiting" state still shows on its row and its terminal.
- **The agent page has no Overview tab.** Its template, who made it, when, and who holds its terminal are one line under its
  name; its sections are Activity (the default), Network and Logs. The box explaining the terminal is gone.
- **Account is its own page**, opened from the avatar; Settings keeps Links, Invites, Users and the Audit log. The project's left
  nav no longer links to the hub's Settings.
- **Plain wording** on the Settings pages and on the "agent status unavailable" banner, which shows on the Agents page only.

## To decide later

- **Services tab: removed for now.** It listed the project's sidecars (control, proxy) and their state, which is rarely what
  someone opens a project for. It may come back in an **advanced section** of the project, with other low-level views (Spec,
  Network, perhaps the Audit log). A project whose sidecars are down still gets its banner; their state is
  shown nowhere else now.
- **What the Advanced section holds.** It has Unlink now. Candidates: Services (above), Spec (the raw resolved project file),
  perhaps Network and the Audit log. Whether the left nav shows it folded, and who sees it when it holds more than Unlink.
- **Messaging in the hub**: questions from agents and answering them. The hub shows nothing of it until it is designed.
