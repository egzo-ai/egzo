# Supported harnesses

A harness is the coding agent that runs inside the container. Egzo runs the real TUI of each one behind a small
session holder (`egzo agent run`), so what you see on `egzo attach` is the tool itself.

| `harness:` | Tool | Status |
|---|---|---|
| `claude-code` | Claude Code (`claude`) | built, with hooks and MCP |
| `opencode` | OpenCode (`opencode`) | built, with hooks (a plugin) and MCP |
| `custom` | any image | runs as given; no integration (see [images](images.md)) |
| `pi` | pi | accepted as a name, no image or integration yet |

## What every integration does

- **Bypass mode is the default.** The harness is configured to run without permission prompts, because the sandbox is
  the boundary. `permissions: default` on an agent turns the harness's prompts back on, for people who want them.
- **No first-run friction.** Onboarding, theme and workspace-trust dialogs are pre-answered, so a fresh instance
  starts at a prompt.
- **Credentials stay out.** The harness gets a placeholder credential; the proxy injects the real one.
- **Status and messages.** Hooks report when the agent is working or idle, and an `egzo` MCP server gives it the tools
  to answer requests, ask a person, and message another agent. Messages ([messaging](messaging.md)) are announced by a short line typed into
  the terminal; the agent fetches the content with a tool.
- **Persistent home.** Each instance has a home volume, so conversations survive recreating the container.

## Claude Code

```yaml
egress:
  default:
    allow: [platform.claude.com]
    services:
      anthropic: main/ANTHROPIC_API_KEY      # an API key
      # anthropic-oauth: main/CLAUDE_TOKEN   # or a subscription token from `claude setup-token`

agents:
  coder:
    harness: claude-code
    model: claude-sonnet-5-5
```

The agent receives `ANTHROPIC_API_KEY` (or `CLAUDE_CODE_OAUTH_TOKEN` for `anthropic-oauth`) as a placeholder that is
only meaningful to the proxy. Extra workspaces become additional directories with workspace trust pre-seeded.

## OpenCode

```yaml
egress:
  default:
    allow: [models.opencode.ai]
    services:
      anthropic: main/ANTHROPIC_API_KEY     # or whichever provider you configure

agents:
  reviewer:
    harness: opencode
    workspaces: [repo]
```

OpenCode reports idle through a plugin egzo installs. Its provider host must be reachable through the profile like
any other.

## Trying two side by side

```yaml
agents:
  claude: { harness: claude-code, workspaces: [repo] }
  open:   { harness: opencode,    workspaces: [repo] }
```

```console
$ egzo spawn claude -m "Add pagination to the orders endpoint" 
$ egzo spawn open   -m "Add pagination to the orders endpoint"
$ egzo ps
```

Each gets its own clone (or worktree), so the two results can be compared as branches.

## Images

Each harness has an image, `ghcr.io/egzo-ai/egzo-harness-<name>:<egzo version>`. Build them from the repository with
`make images`, or point egzo at another registry with `EGZO_HARNESS_PREFIX`. If an image cannot be pulled, `spawn`
stops and names the image and the `image:` override. See [Overriding base images](images.md), and [installing](install.md)
for building them. `pi` waits on MCP support: `known-issues/mcp-support-is-required.md`.
