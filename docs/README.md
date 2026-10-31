# egzo documentation

Egzo is early (0.x releases): see [`roadmap/initial-release.md`](../roadmap/initial-release.md) for what the docs
still have to be checked against. These pages are quick pointers, not a reference; `egzo <command> --help` and `egzo project check --resolved` are the
detail, and the executable specs in `specs/` are the status report.

## Start here

1. [Introduction](intro.md): what egzo is, how it is built, a first run
2. [Installing and choosing an engine](install.md): build, Docker, Podman, gVisor, `egzo system check`
   - [Using gVisor](gvisor.md): why, the cost, the two flags

## Using egzo

3. [The project file](project-file.md): `egzo.yaml` by example
4. [Managing projects](managing-projects.md): `project up`, `agent spawn`, `agent attach`, `agent port-forward`, `agent rm`, `project down`
5. [Workspaces](workspaces.md): directories, git worktrees, and what removing an agent deletes
6. [Messaging](messaging.md): sending tasks, questions from agents, agents talking to each other
7. [Supported harnesses](harnesses.md): Claude Code and OpenCode
8. [Overriding base images](images.md): adding tools to an agent
9. [Browsing](browser.md): the browser every agent has, and what it may reach

## The hub (web UI)

10. [The hub](hub.md): running it, linking projects, the terminal in the browser, settings
11. [Hub access](hub-access.md): the starter account, single sign-on, invites, people, roles, operators, quotas
12. [The hub API](hub-api.md): the JSON API the web app talks to

## Safety and problems

13. [Sandboxing agents](sandboxing.md): writing egress policies
14. [The security model](security.md): what is enforced and what is not
15. [Troubleshooting](troubleshooting.md)
