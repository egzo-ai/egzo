# Supported harnesses

A harness is the coding agent that runs inside the container. Egzo runs the real TUI of each one behind a small
session holder (`egzo agent run`), so what you see on `egzo agent attach` is the tool itself.

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
- **Status and messages.** Each harness reports (Claude Code through hooks, OpenCode through a plugin) when the agent is working or idle, and an `egzo` MCP server gives it the tools
  to answer requests, ask a person, and message another agent. Messages ([messaging](messaging.md)) are announced by a short line typed into
  the terminal; the agent fetches the content with a tool.
- **A browser.** Chromium and the Playwright MCP server are in the image and registered with the harness ([browsing](browser.md)).
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

The image carries OpenCode 2 (`@opencode/cli`). It reads its key from `OPENCODE_API_KEY`, so give it a placeholder and the
agent never holds the key:

```yaml
vaults:
  main: { backend: pass, secrets: [opencode/go-key] }

egress:
  opencode:
    allow: [models.opencode.ai]
    services:
      opencode-go:
        hosts: [opencode.ai]               # a service's hosts are reachable too
        secret: main/opencode/go-key
        placeholder: OPENCODE_API_KEY

agents:
  open: { harness: opencode, egress: opencode, model: opencode-go/glm-5.3-flash }
```

OpenCode talks to two hosts: `models.opencode.ai` (the model list) and `opencode.ai` (the model calls, under
`/inference/go/`, and the Console).

### Getting the key for an OpenCode Go subscription

The Console has no page that shows a key to copy. A key is a *service account* of your workspace:

1. In the [Console](https://opencode.ai/console), in the workspace that holds the Go subscription (a Go workspace has one
   member), create a service account. The one this was tested with had the permissions *models: request* and *providers:
   write*, and no others.
2. The service account gives you an API key (it starts with `oc_s`). Store it in your password manager:
   `pass insert opencode/go-key`.

You can also get a feel for it first with OpenCode itself, in a test agent: `egzo exec <agent> -- opencode auth login opencode`
offers *OpenCode Console account* (a browser login) and *API key (service account)*. The browser login stores a session in
the agent's home, which egzo cannot inject; use the key.

## Trying two side by side

```yaml
agents:
  claude: { harness: claude-code, workspaces: [repo] }
  open:   { harness: opencode,    workspaces: [repo] }
```

```console
$ egzo agent spawn claude -m "Add pagination to the orders endpoint" 
$ egzo agent spawn open   -m "Add pagination to the orders endpoint"
$ egzo ps
```

Each gets its own worktree, so the two results can be compared as branches.

## Images

Each harness has an image, `ghcr.io/egzo-ai/egzo-harness-<name>:<egzo version>`. Build them from the repository with
`make images`, or point egzo at another registry with `EGZO_HARNESS_PREFIX`. If an image cannot be pulled, `egzo agent spawn`
stops and names the image and the `image:` override. See [Overriding base images](images.md), and [installing](install.md)
for building them. `pi` waits on MCP support: `known-issues/mcp-support-is-required.md`.
