# The platform instructions do not tell Claude Code where its answer goes

Status: **open** (found 2026-10-04, Claude Code 2.1.289 in `~/egzo-test2`).

## Symptom

`egzo send claude "…"` reaches the agent: the message is typed in, acknowledged by the `UserPromptSubmit`
hook and shown in the TUI. But Claude answers **in its own TUI**, where nobody is looking when the agent runs
unattended. The reply only reached the human after Claude was told, in a second message, to use the egzo tool
(it then called `say`, event 44 in that project's log).

## Cause

`PlatformInstructions` (`internal/harness/harness.go`), appended to Claude Code's system prompt, explains the
`[egzo msg … from …]` header and lists the `egzo` MCP tools. It never says:

- that a message from egzo is expected to be **answered through egzo**, and not in the terminal;
- which tool to use for the answer (`say`, or `handoff` for a message from another agent);
- what a normal final reply is for.

It also steers away from the right behaviour: "Use them sparingly and never put secrets into them" reads as
"avoid the tools", and the `say` tool's own description says "Use it sparingly" and "something worth
knowing, such as a result or a decision". The MCP server's own instructions, "Messages the humans queue for you
arrive through check_inbox", are stale: messages are typed into the terminal, not pulled.

A related gap: egzo does not capture the final assistant text at all. The `Stop` hook payload carries it
(`last_assistant_message`, confirmed in the event log), but it stays a raw `hook` event, so nothing in the CLI
shows it. Better instructions are the weaker fix; capturing the reply is the sturdier one.

## What a fix needs

- Instructions that say it plainly: a message with an egzo header is answered through egzo, and the agent
  should not rely on its terminal being read. Name the tool, and say what progress updates are for.
- The stale sentence in the MCP server's instructions removed or corrected, and the "sparingly" wording
  reconsidered in the instructions and in the `say` description.
- Ideally a `reply` event built from the `Stop` hook's final message, tied to the id of the message that started the
  turn, and `egzo send --wait` printing it (see the discussion that opened this issue). Then the instructions
  only need to say that the final answer is relayed.
- A spec that fails until it works: send a message to a real Claude Code (no model call is needed for the
  delivery half) and assert the agent's text comes back as an event the CLI can show. A text-only spec
  can check that the instructions name the tool and the destination.

## Why it matters

egzo agents are meant to run unattended. An agent that answers only in a terminal is, from egzo's side, silent.
