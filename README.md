# egzo

**Docker Compose for AI coding agents.** Describe a team of agents in one YAML file, start them in
sandboxes, and attach to any of them with the real TUI you already know, Claude Code included.

> Status: working on Docker, early everywhere else. Config validation, `up` / `down`, isolated agents
> running as you, the egress proxy with credential injection, git workspaces (clone, shared, worktree),
> Claude Code and OpenCode in their native TUI (`egzo attach`), message delivery into the terminal, and the
> operator commands (`secrets`, `doctor`, `diff`, `ca rotate`, `proxy rules`) are built. Not yet: pi, and the
> Podman and gVisor spec runs. The executable spec in `specs/` is the status report:
> a passing test is done and a failing test is not done yet or broken (the failing specs are the todo list). Run it with
> `make specs` (it picks the platform from your machine; `ENGINE=docker` names one).

## Why

Coding agents are most useful when they can work unattended, and least safe when they do. Egzo
gives each agent a sandbox you can trust, without changing the tool you use.

- **Your harness, untouched.** Agents run the real `claude`, `opencode` and friends in a terminal.
  Attach with `egzo attach`, detach, reattach. Scrollback, keys, slash commands all behave as usual.
- **Structure for many agents.** Several agents, several harnesses side by side, each with its own
  workspace, permissions and credentials.
- **Autonomous by default.** The sandbox is the security boundary, so agents run in their harness's
  bypass mode and never stop to ask permission.
- **Secrets never reach the agent.** Credentials live in a proxy sidecar that injects them into
  outgoing requests. An agent that is fully compromised still has nothing to steal.
- **No daemon, nothing new to trust.** Egzo is a stateless CLI. Everything else is containers on your
  own Docker or Podman (whatever `DOCKER_HOST` points at). The engine is the source of truth.

## Example: a coder, a reviewer and an architect

A GitHub repo and three agents. The coder writes code, the reviewer reviews it, and the architect
reads the code to advise on design. Each agent works in its own git worktree of the same repo.

```yaml
# egzo.yaml
vaults:
  main:
    backend: env
    secrets:
      ANTHROPIC_API_KEY: { from: env:ANTHROPIC_API_KEY }
      GITHUB_TOKEN:      { from: env:GITHUB_TOKEN }

egress:
  default:                           # one profile for everyone; anything not allowed is denied
    allow: [platform.claude.com]
    services:
      anthropic: main/ANTHROPIC_API_KEY
      github: main/GITHUB_TOKEN

workspaces:
  repo:
    git: { url: https://github.com/acme/shop.git, branch: main }
    mode: worktree                   # every agent that lists `repo` gets its own worktree

agents:
  coder:
    harness: claude-code
    workspaces: [repo]               # its own worktree; pushes its own work
    prompt: ./prompts/coder.md       # implement tasks on branches, open PRs

  reviewer:
    harness: claude-code             # try `opencode` here to compare frameworks
    workspaces: [repo]
    prompt: ./prompts/reviewer.md    # review PRs, comment, approve or request changes

  architect:
    harness: claude-code
    workspaces: [repo]
    prompt: ./prompts/architect.md   # answer design questions, review the approach
```

No `egress:` on the agents: with no profile named, each gets `default`.

Start it, look around, talk to your agents:

```console
$ export ANTHROPIC_API_KEY=... GITHUB_TOKEN=...
$ egzo up                  # prepares a worktree per agent, starts the proxy and the agents
$ egzo ps
NAME            SERVICE    STATE    HEALTH   ACTIVITY  STATUS
shop-architect-1  architect  running           idle
shop-coder-1      coder      running           idle
shop-control-1    control    running  healthy
shop-proxy-1      proxy      running  healthy
shop-reviewer-1   reviewer   running           idle

$ egzo send architect "How should we add rate limiting to the checkout API?"
$ egzo send coder "Add rate limiting to the checkout API, then open a PR"
$ egzo attach coder        # the real Claude Code TUI; detach with Ctrl-]
$ egzo send reviewer "Review the new PR when the coder opens it"
```

The coder works in its own worktree and pushes its branch. The reviewer and the architect each have
their own worktree and fetch what they need from GitHub. The agents never hold the API key or the
GitHub token: the proxy injects them into outgoing requests, and everything else is denied.

## How it works

```
 egzo CLI ──(Docker/Podman API)──▶ engine
                                    ├─ proxy     injects credentials, allowlists egress, audits
                                    ├─ control   agent status, message queue, MCP tools
                                    └─ agents    one container each, own network, own workspace
```

- Agents can reach only the proxy and the control sidecar, never each other or your network.
- Messages and status flow through harness hooks and a small MCP tool set, not screen scraping.
- Projects are plain containers with labels, like Compose projects. `egzo up` converges to the file.

## Requirements

Linux with Docker or Podman. Harness images come from `ghcr.io/egzo-ai/egzo-harness-<name>`, or are built
with `make images` (`EGZO_HARNESS_PREFIX` points egzo at another registry). egzo uses the engine `DOCKER_HOST` points at, like the docker CLI
(default `/var/run/docker.sock`). For rootless Podman:

    systemctl --user enable --now podman.socket
    export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock

For gVisor, register `runsc` with Docker and set `runtime: runsc` on an agent, as in Compose.
Podman's Docker-compatible API cannot select a runtime, so egzo refuses to start an agent that asks for one on Podman rather than start it without.

## Later

A separate hub with a web UI for multi-project, multi-user day-to-day operation and chat
integrations such as Discord. The YAML file stays the only way to create or change a project.
