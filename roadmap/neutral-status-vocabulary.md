# Roadmap: egzo's own status vocabulary, no Claude Code names

**Status.** Built (code, specs, unit tests). Decided on the way: the endpoint is `POST /v1/state`; `reason` is free text (at most 500 bytes
recorded) and `egzo state` forwards `tool_name` and `prompt` from a JSON event on stdin; Claude Code's `Notification` is two hooks selected by
Claude's own matcher (`idle_prompt` is idle, `permission_prompt|elicitation_dialog` is blocked), so no text is parsed anywhere; the `message`
field is no longer recorded. No backward compatibility: egzo is pre-release. A prerequisite of [non-tui-agents.md](non-tui-agents.md), and worth doing on its own.

## The problem

An agent tells the control sidecar what it is doing with a *hook*: `POST /v1/hooks/<Name>` (`egzo hook <Name>` in a harness's
hook command). The names are Claude Code's: `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `Stop`,
`Notification`. The control maps them to an activity (`internal/control/delivery.go`, `hookActivity`):

| Hook | Activity |
|---|---|
| `SessionStart`, `Stop` | `idle` |
| `UserPromptSubmit`, `PreToolUse`, `PostToolUse` | `working` |
| `Notification` | `blocked`, unless its message says it "waits for your input" (then `idle`) |

OpenCode has its own events (`session.idle`, `permission.asked`, `question.asked`, `chat.message`), and its plugin
(`internal/harness/opencode.go`) translates them into Claude Code's names, down to a made-up `Notification` message. Any other
harness would have to do the same. Three costs:

- A harness impersonates another one. What it really knows (it is waiting for a permission, it asked a question) is squeezed
  into a free-text `Notification` that the control then reads back with a substring match (`"waiting for your input"`).
- The hook endpoint is the protocol of every harness, and it is defined by the first one built. It cannot grow with a second.
- `PreToolUse` and `PostToolUse` mean "working", but the events stream records them as hooks of a tool, a different thing.

## The idea

The protocol belongs to egzo. A harness reports a **state change**, in egzo's words, and the control does no mapping.

- **One endpoint of states**: `POST /v1/agent/state` (name to be decided; it may stay under `/v1/hooks/`) with
  `{state, reason?}`, where `state` is one of the activities the control already has: `starting`, `idle`, `working`,
  `blocked`. `reason` is a short text for people (`permission`, `question`, `login`, `a tool name`), shown by `egzo ps`
  and the hub and recorded in the event. The control never parses it for a decision: today's "waits for your input" match goes away
  because the harness says `idle` itself.
- **A harness reports what it knows.** Claude Code's integration keeps its hooks, but the command each hook runs says which state it
  means: `egzo state working --reason PreToolUse`. The mapping from Claude's hook names to states moves into
  `internal/harness/claudecode.go`, next to the settings that declare the hooks, where it can be seen and tested with the rest of
  that harness. `Notification` is the one hook that needs a rule (blocked or idle by its message), and it belongs to
  Claude Code's integration, not to the control.
- **OpenCode** reports states straight from its plugin: `session.idle` is `idle`, `permission.asked` is `blocked` with a
  reason, `chat.message` is `working`. No fake `Notification` text.
- **The old endpoint is removed**: `/v1/hooks/<Name>` and its six Claude names go away in the same change. Egzo is pre-release, so
  there is no alias and no compatibility window; harness images are rebuilt with the egzo that ships the new endpoint.
- **The event stream** keeps `activity` events as they are (the hub and the adapters read them). The `hook` event type is
  the raw record of what a harness sent. It becomes `state` events with the reason; the `hook` event type goes away.
  The `tool_name` and `message` fields stay the only payload fields kept (see `hookFields` in `internal/control/agentapi.go`).
- **Client**: `egzo hook <Name>` is replaced by `egzo state <state> [--reason TEXT]`, with the same
  guarantees: it never fails, never waits long, and never holds the harness up.

## Not part of this

- New states. The four activities are enough, and `stopped` stays the engine's. Richer detail ("running tool X", progress) is a
  `reason`, or the agent's own `status(text)` tool.
- A harness without any reporting still uses quiescence (`inject.idle_signal: quiescence`). That does not change.
- How an agent without a terminal receives work: [non-tui-agents.md](non-tui-agents.md).

## Tests

- Core: specs first (`specs/message/test_agent_api.py`, `specs/harness/test_harness_integrations.py`): each state is accepted and
  changes the activity; an unknown state is refused; the reason is shown and never changes the decision; the old hook endpoint and `egzo hook` no longer exist. Claude Code's settings declare hooks that report the new states. OpenCode's plugin reports them
  without a `Notification`.
- Unit (Go): the Claude Code mapping lives in `internal/harness` and is tested without a container; `hookActivity` is gone from the control.
