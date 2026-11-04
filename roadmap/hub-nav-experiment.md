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

## To decide later

- **Services tab: removed for now.** It listed the project's sidecars (control, proxy) and their state, which is rarely what
  someone opens a project for. It may come back in an **advanced section** of the project, with other low-level views (Spec,
  Network, perhaps the Audit log). The sidecars' state still shows on the line under the project's name, and a project whose
  sidecars are down still gets its banner.
- What an advanced section holds, and whether the left nav shows it folded.
