# Supported harnesses

A harness is the coding agent that runs inside the container. Egzo runs the real TUI of each one behind a small
session holder (`egzo agent run`), so what you see on `egzo agent attach` is the tool itself.

| `harness:` | Tool | Status |
|---|---|---|
| `claude-code` | Claude Code (`claude`) | built, with hooks and MCP |
| `opencode` | OpenCode (`opencode`) | built, with hooks (a plugin) and MCP |
| `codex` | Codex (`codex`) | built, with hooks and MCP; a ChatGPT sign-in or an OpenAI API key |
| `custom` | any image | runs as given; no integration (see [images](images.md)) |
| `pi` | pi | accepted as a name, no image or integration yet |

## What every integration does

- **Bypass mode is the default.** The harness is configured to run without permission prompts, because the sandbox is
  the boundary. `permissions: default` on an agent turns the harness's prompts back on, for people who want them.
- **No first-run friction.** Onboarding, theme and workspace-trust dialogs are pre-answered, so a fresh instance
  starts at a prompt.
- **Credentials stay out.** The harness gets a placeholder credential; the proxy injects the real one.
- **Status and messages.** Each harness reports (Claude Code and Codex through hooks, OpenCode through a plugin) when the agent is working or idle, and an `egzo` MCP server gives it the tools
  to answer requests, ask a person, and message another agent. Messages ([messaging](messaging.md)) are announced by a short line typed into
  the terminal; the agent fetches the content with a tool.
- **A browser.** Chromium and the Playwright MCP server are in the image and registered with the harness ([browsing](browser.md)).
  Both harnesses keep an MCP server's tools off the model's tool list unless told otherwise: Claude Code behind tool search (`ToolSearch`),
  OpenCode 2 behind Code Mode (one `execute` tool that runs a script). egzo keeps its own tools on the list by name (`alwaysLoad` for Claude
  Code, `codemode: false` for OpenCode), and leaves the browser's behind them.
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

The image carries OpenCode 2 (`@opencode/cli`). Bind the key of an OpenCode Go (or Console) workspace to the built-in
`opencode` service; the agent only holds a placeholder `OPENCODE_API_KEY`:

```yaml
vaults:
  main: { backend: pass }

egress:
  opencode:
    allow: [models.opencode.ai]            # the model list; the model calls go to opencode.ai, which the service covers
    services:
      opencode: main/opencode/go-key

agents:
  open: { harness: opencode, egress: opencode, model: opencode-go/glm-5.3-flash }
```

OpenCode talks to two hosts: `models.opencode.ai` (the model list) and `opencode.ai` (the model calls, under
`/inference/`, and the Console, for the workspace's config and web search). The service sends the key there only, not to the
rest of `opencode.ai`, which is also the website.

### Getting the key for an OpenCode Go subscription

The Console has no page that shows a key to copy. A key is a *service account* of your workspace:

1. In the [Console](https://opencode.ai/console), in the workspace that holds the Go subscription (a Go workspace has one
   member), create a service account. The one this was tested with had *models: request* and *providers: write*; *models: request* alone is the safer
   choice, and has not been tested.
2. The service account gives you an API key (it starts with `oc_s`). Store it in your password manager:
   `pass insert opencode/go-key`, as in the example above.

You can also get a feel for it first with OpenCode itself, in a test agent: `egzo exec <agent> -- opencode auth login opencode`
offers *OpenCode Console account* (a browser login) and *API key (service account)*. The browser login stores a session in
the agent's home, which egzo cannot inject; use the key.

## Codex

The image carries Codex (`@openai/codex`). It works with a ChatGPT Plus or Pro account or with an OpenAI API key.

```yaml
egress:
  codex:
    services:
      chatgpt: { credential: { method: oauth } }   # a ChatGPT sign-in: chatgpt.com, under /backend-api/ only
  codex-key:
    services:
      openai: main/openai/api-key                 # or an API key for api.openai.com

agents:
  codex: { harness: codex, egress: codex, workspaces: [repo] }
```

- **A ChatGPT account** is an [OAuth service](sandboxing.md#oauth-services): no vault secret. The first Codex agent shows
  Codex's own sign-in screen: attach (`egzo agent attach codex-1`) and pick *Sign in with Device Code*, the option that
  works from a container. The proxy keeps the sign-in for every agent of the project, renews it, and gives each new
  Codex agent an `auth.json` of stand-ins, so it starts signed in. Codex never holds a token that works without the proxy.
- **An API key** goes to the built-in `openai` service; the agent's `auth.json` holds a placeholder.
- **Settings.** egzo starts Codex with its settings on the command line (`-c`): no approval prompts (`permissions:
  default` restores them), Codex's own sandbox off (the container is the sandbox), the workspaces trusted, the `egzo` MCP
  server, the state hooks, egzo's instructions as `developer_instructions`, no update check, and
  `features.use_agent_identity=false` (an identity Codex registers and signs with would be a credential the agent holds).
  Your `~/.codex/config.toml` in the agent's home is left alone.
- **Hooks.** Codex runs a hook only once a person has reviewed it, unless it is started with
  `--dangerously-bypass-hook-trust`. egzo starts it so, for the hooks it gives on the command line; hooks you add yourself
  run under the same flag.
- `model:` is passed as `-c model=...`; effort is Codex's `model_reasoning_effort`, which `env` cannot set: put it in the
  agent's `~/.codex/config.toml`.

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
