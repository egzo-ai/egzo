# Messaging: talking to agents, and agents talking to each other

See [managing projects](managing-projects.md) for the rest of the CLI.

Everything people and agents say to each other is a **message**, kept by the control sidecar. The terminal only
ever gets a short fixed line that announces a message; the agent fetches the text with a tool. Nothing a message
contains can be typed into the terminal, so a message cannot pretend to be you.

## Sending a task

```console
$ egzo message send coder-1 "Fix the flaky checkout test, then open a PR"
$ egzo message send coder-1 "Stop, use the v2 API instead" --interrupt    # stops what it is doing first
$ egzo message send reviewer-1 "Review PR 88" --wait                      # block until it answers
$ egzo agent spawn coder issue-412 -m "..." --wait                      # spawn, send, and wait
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
| `expose` | open one of its declared ports for the hub or for the local machine, and get the address |
| `unexpose` | close it |

So agents can hand work to each other (`message` to `agent:reviewer-1`, wait for the reply, then resolve their own
request). Agents never share a network: this is the only path between them.

## Ports an agent opens

An agent can only serve the ports its template [declares](project-file.md#ports), by name. A declared port is closed until the agent calls
`expose(name, where)`, where `where` is `hub` (a preview people open from the [hub](hub.md); `http` ports in a project that names one) or `local`
(a port on the machine that runs the project; needs [`local_ports`](project-file.md#local-ports)). The answer is
`{name, port, protocol, where, address}`, plus an `id` for `hub`, and `address` is the one place the app can be reached:

| call | answer |
|---|---|
| `http`, `hub` | `https://<id>-<project>.<domain>/`, from the hub |
| `http`, `local` | `http://<host>:<outside>/` |
| `tcp` or `udp`, `local` | `<host>:<outside>` |

Refused: a name that is not declared (the answer lists the names), `tcp` or `udp` for the hub, `hub` when no hub serves the project, `local` when the
project has no `local_ports`, the other `where` while a port is open (close it first), and a `local` port beyond `local_ports.max_exposed` open in the
project. Asking again for what is open gives the same answer.

`unexpose(name)` answers `{ok: true, note}`, and the note says that active connections may remain active until the agent closes its server.

An agent with ports is told them when it starts: which ports, that the servers listen on `0.0.0.0`, that they are closed until `expose`, that it exposes
one only when a person needs it, and which values of `where` this project has. An agent with no ports is told nothing about them.

- A restart of the agent closes what it exposed, because its server is gone and it starts with no memory of it. It exposes the port again, or its prompt
  says to, and a `local` port has the same address; a preview keeps its id too. A new agent with the same name gets new previews. A restart of the
  sidecars changes nothing. It is the entrypoint that tells control of a restart, so a `custom` image that replaces it [keeps its exposures](images.md).

`egzo ps` shows them in `PORTS` as `web:hub,db:local`, and `egzo ps --json` with their address. A stopped agent shows none. To close a port, ask the agent, or stop it.

## Questions from agents

```console
$ egzo message ls --kind question                  # what agents have asked and nobody has answered
$ egzo message ls [--all]           # every open message, and where it stands
$ egzo message reply m3f... "Use the v2 API"
$ egzo message reply m3f... "No, skip it" --outcome declined
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
