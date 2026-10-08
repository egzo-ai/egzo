# Messaging: talking to agents, and agents talking to each other

See [managing projects](managing-projects.md) for the rest of the CLI.

Everything people and agents say to each other is a **message**, kept by the control sidecar. The terminal only
ever gets a short fixed line that announces a message; the agent fetches the text with a tool. Nothing a message
contains can be typed into the terminal, so a message cannot pretend to be you.

## Sending a task

```console
$ egzo send coder-1 "Fix the flaky checkout test, then open a PR"
$ egzo send coder-1 "Stop, use the v2 API instead" --interrupt    # stops what it is doing first
$ egzo send reviewer-1 "Review PR 88" --wait                      # block until it answers
$ egzo spawn coder issue-412 -m "..." --wait                      # spawn, send, and wait
```

A message waits in the queue until the agent is idle, nobody has typed in its terminal for a while (30 s by default)
and the harness is ready. Then a line like `check egzo message m3f... and handle the request for me.` is typed in, and
the agent fetches and answers it. If it does not fetch it within a minute, the line is typed again, up to three
times, after which the message is marked `unconfirmed`. A message for a stopped agent waits until it runs again.

`--wait` prints progress notes to stderr and the answer to stdout. Exit codes: 0 done, 3 declined, 4 failed, 5 the
`--timeout` passed (the message stays open).

## What agents can do

Agents get an `egzo` MCP server with these tools:

| tool | use |
|---|---|
| `list_messages` | what the agent owes: open requests and questions, unread replies |
| `get_message` | read a message addressed to it |
| `resolve` | close a request or question with a result (`done`, `declined`, `failed`) |
| `update` | a progress note for whoever asked |
| `ask` | ask the sender a question; the answer comes back as a reply |
| `message` | send a new request to the operator, a person or another agent |
| `status` | one line on what it is doing, shown by `egzo ps` |
| `agents` | the other agents, with their activity |

So agents can hand work to each other (`message` to `agent:reviewer-1`, wait for the reply, then resolve their own
request). Agents never share a network: this is the only path between them.

## Questions from agents

```console
$ egzo questions                  # what agents have asked and nobody has answered
$ egzo messages [--all]           # every open message, and where it stands
$ egzo answer m3f... "Use the v2 API"
$ egzo answer m3f... "No, skip it" --outcome declined
```

A message to a person is not typed into any terminal; it waits here until answered. The answer goes back to the
agent as a reply, announced in its terminal like any message.

## What you see in `egzo ps`

- `ACTIVITY`: `starting`, `idle`, `working`, `blocked` (the harness is stuck on its own UI, such as a login that only a
  person can answer), `stopped`.
- `OPEN`: requests the agent has fetched and not resolved.
- `WAITING`: it has asked a question nobody answered.

Hooks (Claude Code) or a plugin (OpenCode) provide the activity ([harnesses](harnesses.md)). For a `custom` image, quiet output means idle.

## Limits

At most 20 open requests per recipient, threads at most 8 messages deep, and 30 messages a minute per sender, so a
loop between two agents ends on its own. A message is at most 16 KB. `egzo events` streams everything as JSON
lines if you want to watch or script it.

## Tuning

Timings can be set per agent:

```yaml
agents:
  coder:
    harness: claude-code
    inject:
      human_quiet: 10s     # how long nobody may have typed before a line is typed in
      ack_timeout: 90s     # how long to wait for a fetch before announcing again
```
