# Introduction

Egzo is Docker Compose for AI coding agents. You describe agents as **templates** in one `egzo.yaml`, start as many
**instances** as you need, each in its own sandbox, and attach to any of them with the real TUI of the harness
(Claude Code, OpenCode). Egzo does not re-render anything: detach, reattach, scrollback and keys behave as usual.

Three ideas carry the design:

- **The sandbox is the security boundary.** Agents run in their harness's bypass mode and never stop to ask
  permission. What limits the damage is where they run: a container of their own, on a network with no route out except
  through the egzo proxy, running as your uid, optionally under gVisor.
- **Secrets never reach the agent.** Credentials live in the proxy, which adds them to outgoing requests for the
  hosts you named. A compromised agent cannot leak credentials it does not have. It can still use them through the
  proxy while it runs, for those hosts; [the security model](security.md) spells out what that means.
- **No daemon.** The `egzo` CLI is stateless. Everything else is containers on your own Docker or Podman, and the
  engine is the source of truth.

## How it fits together

![egzo architecture](architecture.svg)

In short:

```
 egzo CLI ──(Docker/Podman API)──▶ engine
                                    ├─ proxy     injects credentials, allowlists egress, audits
                                    ├─ control   agent status, message queue, MCP tools
                                    └─ agents    one container each, own network, own workspace
```

- `egzo project up` starts the **proxy** and the **control** sidecar and publishes the templates. It starts no agent.
- `egzo agent spawn <template>` makes an **instance**: a container, its own network, home volume and git worktrees (all in engine volumes).
- An agent can reach only the proxy and the control sidecar, never another agent and never your network.
- Messages and status flow through harness hooks and a small MCP tool set (`egzo` tools such as `resolve`, `ask`),
  not through screen scraping.

## Requirements

Linux with Docker or Podman, reached through `DOCKER_HOST` like the docker CLI does (default `/var/run/docker.sock`). Docker is
the engine to start with. Rootless Podman and gVisor are covered in [Installing](install.md), as advanced setups.

Run `egzo system check` to check the engine and its isolation. [the security model](security.md) says what the isolation does and
does not give you.

## A first run

```console
$ egzo project init --harness claude-code     # scaffolds egzo.yaml
$ export ANTHROPIC_API_KEY=...
$ egzo project up
$ egzo agent spawn claude --attach          # the real Claude Code, in a sandbox
```

Detach with `Ctrl-]`; `egzo agent attach <name>` comes back. `egzo project init` fills the repository URL from your git `origin`;
check the generated file before `egzo project up` (it leaves `TODO` comments where it had to guess). Next: [the project
file](project-file.md), then [managing projects](managing-projects.md) and [sandboxing agents](sandboxing.md). If
something misbehaves, see [troubleshooting](troubleshooting.md).

## Status

Working on Docker: validation, `project up` / `agent spawn` / `agent rm` / `project down`, templates and instances, the egress proxy with
credential injection, git workspaces, Claude Code and OpenCode, message delivery, and the operator commands. Not yet:
the `pi` harness; the Podman and gVisor spec runs are reported in CI but are not required checks. A separate web UI, [the hub](../hub/docs/README.md), is built and is not part
of the CLI. Binaries and images are published for Linux (amd64 and arm64), still as early 0.x releases ([`roadmap/initial-release.md`](../roadmap/initial-release.md)).
