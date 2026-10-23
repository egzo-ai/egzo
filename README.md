# egzo: simple agent sandboxing and orchestration

![The egzo hub: the agents of a project, then a live Claude Code session in the browser](assets/hub.gif)

Egzo is a simple CLI tool to sandbox coding agents and keep your projects organized, with an optional web UI, [the hub](docs/hub.md),
to watch and drive them from a browser.

Define your project in a simple YAML document, spin up the project with `egzo project up`, then spawn isolated Claude Code instances using
`egzo agent spawn [template]`. Monitor with `egzo ps`, and destroy them once done with `egzo agent rm`.

As a bonus, since it just spins up normal containers, any docker/podman tools or commands you use to manage containers just work.
Destroy the containers or use `egzo project down` and nothing remains on your system.

If you have not guessed it by now: yes, the design is largely inspired by docker-compose. Unlike compose's `services`, agents are defined
as templates, and you spawn throwaway instances using `egzo agent spawn my-template` whenever you need to work on a task. You get a Claude Code (or
any other supported agent).

## Supported platforms

- Linux with Docker or rootless Podman as the container engine.
- Docker with gVisor (runsc) is also supported for additional security.
- Rootful Podman is not supported for now: [`known-issues/rootful-podman-intermittent-egress.md`](known-issues/rootful-podman-intermittent-egress.md).
- macOS and Windows (through WSL2) are planned but not supported yet: [`roadmap/windows-and-macos.md`](roadmap/windows-and-macos.md).

egzo uses the engine `DOCKER_HOST` points at, like the docker CLI (default `/var/run/docker.sock`). Harness images come
from `ghcr.io/egzo-ai/egzo-harness-<name>`, or are built with `make images` (`EGZO_HARNESS_PREFIX` points egzo at
another registry). For rootless Podman:

    systemctl --user enable --now podman.socket
    export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock

For gVisor, register `runsc` with Docker and make it the engine's default runtime (`"default-runtime": "runsc"` in
`daemon.json`). egzo does not choose a runtime: everything runs under the engine's default.

## Documentation

See [`docs/`](docs/README.md): the CLI and the project file, then [the hub](docs/hub.md), the web UI.

## Features

- **Claude Code and OpenCode.** More harnesses planned.
- **Real TUIs, in sandboxes.** The standard coding agent interface you are used to. `egzo agent attach` connects, `Ctrl-]` detaches.
- **Templates and instances.** Declare agents once in `egzo.yaml`, then `egzo agent spawn` as many instances as you need,
  each with its own container, network, home and worktree. `agent rm` and `project down` clean up.
- **Isolation by default.** Restricted internet and file access, all capabilities are dropped, memory and process limits.
- **gVisor and rootless podman.** Tested in the specs matrix, for additional security: gVisor as the engine's default runtime, or a rootless engine.
- **Egress policy.** Allow internet access using a whitelist, or start simple by allowing all domains. Everything remains audited.
- **Credential injection.** API keys and tokens can be injected live by the built-in proxy, so your agents never need to see any secrets,
  greatly reducing consequences of prompt injections and risks of secrets leaking.
- **Password manager integration.** Fetch api keys and secrets directly from your password manager.
- **Git worktrees.** A base clone per repository, a worktree per agent, in engine volumes, made through the proxy, so agents hold no git credentials. Nothing is kept in your project directory.
- **Messaging.** Send messages or tasks to agents without opening their full UI, opening the door for quick work or automated jobs.
- **Built-in browser.** Chromium and the Playwright MCP just works out of the box; ([docs/browser.md](docs/browser.md)).
- **Your own tools.** Extend a harness image with extra software, or run any image with the `custom` harness.
- **The hub, a web UI.** Every project on one or more engines, what each agent is doing, and their real TUIs in the browser,
  several side by side. Single sign-on (any OpenID Connect provider) and per-project roles for a team; [docs/hub.md](docs/hub.md).

## Example: architect with Claude, code with OpenCode

A GitHub repo and 2 agents.

```yaml
# egzo.yaml
vaults:
  main:
    backend: env
    secrets: [ANTHROPIC_API_KEY, GITHUB_TOKEN]

egress:
  default:                           # one profile for everyone; anything not allowed is denied
    allow: [platform.claude.com]
    services:
      anthropic: main/ANTHROPIC_API_KEY
      github: main/GITHUB_TOKEN

workspaces:
  repo:
    git: { url: https://github.com/egzo-ai/egzo.git, branch: main }

agents:
  architect:
    harness: claude-code
    workspaces: [repo]
    prompt: ./prompts/architect.md   # "You are a senior software architect..."

  coder:
    harness: opencode
    workspaces: [repo]
    prompt: ./prompts/coder.md       # "Make no mistake"
```

Bring the project up, then start the agents you need from their templates.

Each `agents:` entry is a template: `egzo project up` starts the infra, `egzo agent spawn` starts a throwaway agent.

```console
$ export ANTHROPIC_API_KEY=... GITHUB_TOKEN=...
$ egzo project up                                     # the proxy, the control sidecar, the templates
$ egzo agent spawn architect -m "I want to build my own agent orchestration system"
$ egzo agent spawn coder issue-123 -m "Start work on the oldest backlog item" --attach
$ egzo ps
NAME        SERVICE    STATE    HEALTH   ACTIVITY  OPEN  WAITING  ACTOR     AGE  STALE  STATUS
architect-1 architect  running           working   1              operator  2m
issue-123   coder      running           working   1              operator  1m
foo-control-1  control  running  healthy
foo-proxy-1    proxy    running  healthy

$ egzo agent attach architect-1  # the real Claude Code TUI; detach with Ctrl-]
$ egzo agent spawn coder review-123 -m "Review the new PR" --wait    # waits for the answer, exits 0 when done
$ egzo agent rm review-123
```

An instance is named by you (`issue-123`) or by egzo (`architect-1`). The same template can be spawned as
often as you like, each with its own worktree, network and home. Spawning needs no edit of `egzo.yaml`.

The agents are never given the API key or the GitHub token: the proxy injects them into outgoing requests, and
anything not allowed is denied by default.

## How it works

```
 egzo CLI ──(Docker/Podman API)──▶ engine
                                    ├─ proxy     injects credentials, allowlists egress, audits
                                    ├─ control   agent status, message queue, MCP tools
                                    └─ agents    one container each, own network, own workspace
```

- Agents can reach only the proxy and the control sidecar, never each other or your network.
- Messages and status flow through harness hooks and a small MCP tool set, not screen scraping.
- [The hub](docs/hub.md) is one more container that reads the same engines; it adds people, roles and a browser terminal, not state.
- Projects are plain containers with labels, like Compose projects. `egzo project up` converges the infrastructure to the file
  and publishes the templates; `egzo agent spawn` makes agents from them, so day-to-day work needs no file edits.

## License

Copyright (C) Neopeak Internet Solutions inc. egzo is licensed under the [GNU Affero General Public License v3.0](LICENSE) (`AGPL-3.0-only`).
