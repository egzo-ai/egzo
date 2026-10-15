# A harness must support MCP; harnesses without it (pi) are out of scope for v1

Status: **open, deferred** (decided 2026-10-05). Not planned for v1.

## The rule

egzo's agent-facing verbs (`list_messages`, `get_message`, `resolve`, `update`, `ask`, `message`, `status`,
`agents`) reach the agent as **MCP tools** served by the control
sidecar at `http://control:7777/mcp`. So a harness has to be able to talk MCP over streamable HTTP, with a
fixed header for the agent's credentials, to be integrated.

- **Claude Code** and **OpenCode**, the two integrated harnesses, both do, and the specs cover it
  (`test_harnesses.py`: the `egzo` MCP server is in each one's configuration).
- **pi** (Mario Zechner's coding agent) has no MCP support, deliberately: its design leaves it out, and
  people reach MCP servers through a bridge (`mcporter` or an extension). It is still an accepted harness
  name in `egzo.yaml`, but it has no image and no integration, so it cannot be used today.
- `custom` runs any image and has no MCP guarantee: the image author provides whatever the agent needs.

## How a harness without MCP could be supported later

Do not teach the harness MCP. The control sidecar's agent API is plain HTTP, and the MCP tools are a thin
adapter over the same verbs (`internal/control/agentapi.go`, `mcp.go`). So a second front end over the same
verbs is enough:

- Expose them as a command line, `egzo agent <command>`, for example `egzo agent list-messages`,
  `get-message ID`, `resolve ID TEXT --outcome done`, `update ID TEXT`, `ask ID TEXT`, `message TO TEXT`,
  `status TEXT`, `agents`. The `egzo` binary is already in every harness
  image, and the credentials are already in the container (`EGZO_CONTROL_URL`, `EGZO_AGENT`, `EGZO_TOKEN`),
  so nothing new has to be installed and no configuration file is needed. `egzo hook` already works this way.
- The CLI calls the HTTP verbs directly. It does not need an MCP client; it is not an MCP bridge, although it
  plays the role bridges such as `mcporter` play for pi (the harness runs a shell command, the result comes
  back as the command's output).
- The platform instructions name the command instead of the tool, and the typed line that announces a
  message says "run `egzo agent get-message <id>`" instead of "call get_message".
- Each verb exists once, as an HTTP route with a handler, and has two front ends: MCP and CLI. A new verb
  must be added to both, with a spec for each.

Conditions: the harness needs a shell tool the agent will use (bypass mode makes that prompt-free), and
the model has to call the command with the exact id, which a short fixed command makes easy. A harness with
neither MCP nor a shell cannot be supported.

## Why it is deferred

v1 integrates Claude Code and OpenCode. pi would need its own image, its hooks (or a quiescence-based idle
signal), the CLI front end and its specs, for one more harness. The message model (a short typed line with a random message id, the message fetched through the tool) is built
with the MCP front end only.
