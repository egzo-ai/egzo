# Roadmap: agents without a terminal (LangGraph, SDK scripts)

**Status.** Designed, not built. Needs [neutral-status-vocabulary.md](neutral-status-vocabulary.md) first, or at least its state
endpoint.

## Why

Every harness egzo runs today is a TUI on a pty. A lot of useful agents have no terminal at all: a Python script that calls the
Anthropic or OpenAI client in a loop, a LangGraph graph, a service with its own queue. They still want what egzo gives: the
sandbox, the egress proxy that injects credentials, workspaces, messages between agents and people, the status feed and the hub.
Nothing of the sandbox and the proxy cares about the harness, and the `custom` harness already runs any image. What is missing is
a way to **hand such an agent its work** and to **know what it is doing**, without a terminal to type into.

The credential helpers (`anthropic`, `anthropic-oauth` and the like in `internal/config/builtins.go`) are part of secrets and
services: they ease configuring the egress proxy, and that is by design. A script uses them as any agent does (a placeholder key
in its environment, the proxy replaces it); they need no change.

## What a Python agent looks like to egzo

What already works, with no change to core:

- The agent's image runs the script as its entrypoint (the `custom` harness). It has the sandbox, the proxy, `HOME`, workspaces.
- It reaches the `egzo` tools over MCP: `http://control:7777/mcp` with the agent's Basic credentials (`EGZO_CONTROL_URL`,
  `EGZO_AGENT`, `EGZO_TOKEN`). `langchain-mcp-adapters` takes `headers` on a `streamable_http` connection. Its Basic-auth handling
  with a custom `httpx` client should be checked, since one report of a failure exists.
- It calls the control's agent API for its status. In LangChain a callback handler (`on_chain_start`, `on_tool_start`,
  `on_chain_end`) is the natural place.

What does not work: the control **announces** a message by typing a line into the agent's terminal when the agent is idle
(`DESIGN.md`, "Messages"). A script has no terminal. The announcement is the only way work arrives.

## The idea: delivery by pull

A second delivery mode next to the terminal one. The agent says so in its definition (`inject.mode: tty | pull`, `tty` the
default, and `pull` the only mode for a harness that is not a TUI).

- **`wait_for_message(timeout)`**, a new tool of the `egzo` MCP server (and a plain HTTP endpoint for scripts without MCP). It
  blocks until a request, a question's answer or a resolution is addressed to the agent, or the timeout passes, and
  returns the messages that are ready, exactly as `get_message` would. Fetching is still the acknowledgement: returning a message
  moves it to `fetched`, so `announced` is skipped (or is the same moment). Nothing new in the states.
- **The loop belongs to the agent**: `while True: m = wait_for_message(); run the graph or call the model; resolve(m.id, ...)`.
  The platform instructions (how to read an egzo message, how to resolve it) are the same text; they are given to the model in the
  system prompt by the script, or by the shim below.
- **Gates**: the control withholds a message from a `pull` agent that is not `idle`/waiting, so a second request does not arrive
  while the first run is going (the "enqueue" strategy of double-texting in LangGraph's server, and the same thing egzo does for a
  TUI today). The agent reports `working` when it starts a run and `idle` when it blocks in `wait_for_message`; blocking *is* idle,
  so the control can derive it without a report.
- **Interrupt** (`egzo message send --interrupt`) has no key to type. For `pull` it is a flag on the next `wait_for_message`
  result or an event the agent may watch, and honouring it is the agent's choice (cancel the task). Out of the first version.
- **Limits** (open requests, depth, rate) are the control's and apply as they are.
- **A message pending when the agent starts** is returned by its first `wait_for_message`, the way it is announced when a TUI is ready.

### How LangGraph fits

- A thread ID per conversation (`re` of the message, or the sender) gives the graph its state in a checkpointer. A SQLite or file
  checkpointer on the agent's home volume survives recreating the agent (the home is already a volume).
- A graph that pauses with `interrupt()` is `blocked` in egzo terms; the question goes out with `ask(id, text)`, the answer comes
  back as a message, and the loop resumes with `Command(resume=...)`. `ask` leaves the request open, which is what a paused graph is.
- Resuming restarts the node from its beginning, so side effects before `interrupt()` repeat: that is LangGraph's rule, and the
  shim's documentation says it.
- LangGraph's own Agent Server (threads, runs, webhooks, Postgres) is a service with an API, not a script. Running it as an egzo
  service is possible and a different question, see below.

## A small shim

A Python package, `egzo-agent`, so nobody writes the plumbing again. It does not ship in the egzo binary.

- `egzo_agent.tools()`: the `egzo` MCP tools for LangChain/LangGraph (through `langchain-mcp-adapters`), with the credentials from the environment.
- `egzo_agent.StatusCallback`: a LangChain callback handler reporting `working`, `idle` and `blocked` (with a reason).
- `egzo_agent.serve(handler)`: the pull loop. It reads the messages, calls `handler(message)`, resolves with the result or
  `failed` on an exception, sets `status()`, and exits cleanly on SIGTERM.
- The same three pieces without LangChain, for a script that calls the Anthropic or OpenAI client directly.
- Image: `harness/python-agent` (a base image with the shim and the egzo binary) and a template, as `harness/claude-code` is.

Whether the shim lives in this repository (a `python/` directory, tested in CI) or its own is open.

## What changes in core

1. `agent.inject.mode` (`tty | pull`), validated: a `pull` agent has no ready markers, interrupt key or session holder.
2. A harness kind without a pty. `egzo agent run` is the entrypoint of the TUI images; a `pull` image runs its script directly
   (or through `egzo agent run`, which then only prepares the home and the CA and execs). `egzo agent attach` has nothing to
   attach to and says so; `egzo logs` is the way in. The hub's terminal for such an agent is read-only logs.
3. `wait_for_message` in the MCP server and the agent API; the control's delivery loop skips the announcement for a `pull` agent.
4. Status for a `pull` agent comes from the state endpoint of [neutral-status-vocabulary.md](neutral-status-vocabulary.md).
   Without reports, "blocked in `wait_for_message`" is idle and anything else is working.
5. The platform instructions become a function of the mode: the line about "what you see typed" does not apply to a `pull` agent.

## Not part of the first version

- Interrupting a run in progress.
- Running LangGraph's Agent Server (or any agent that is a web service with its own API) as an egzo agent, with messages bridged to
  its runs. It is a service more than a harness; egzo's `service:` entries may be the right place. Decide when someone needs it.
- A way to attach to a Python REPL.
- Push delivery (SSE, MCP notifications). `wait_for_message` as a long poll is enough; the stream is a later optimisation.

## Open

- Name and shape of the mode (`inject.mode: pull`, or a harness `kind`).
- Whether `wait_for_message` returns one message or a batch, and the longest timeout (a proxy or an HTTP client may cut an idle call).
- Where the shim lives and how it is versioned against the control API ([hub-link.md](hub-link.md#versions) has the rule for the link).
- What the hub shows for an agent with no terminal.

## Tests

- Core: specs first. A stub agent of about 30 lines of Python (no model, no LangChain) in a `custom` image: it blocks in
  `wait_for_message`, receives a message from the operator, resolves it, and its activity goes `idle` to `working` to `idle`.
  Pending messages at start, a timeout that returns nothing, a second request held while the first is being handled, ids
  addressed to someone else refused as today. Red until the mode is built, per the project rule.
- Shim: ordinary Python tests with a fake control; one end-to-end run with the stub in the specs.
- A model is never called by the specs, as for the TUIs.
