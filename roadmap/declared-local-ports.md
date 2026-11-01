# Plan: declared ports, hub or local

Status: agreed, not built. Branch: `declared-local-ports`, from `main`, one PR. It replaces what `roadmap/agent-ports.md` says about `expose`,
`port-forward` and raw TCP. The preview design in that file (hub listener, sign-in, certificates) stands. PRs #75 and #76 are out of scope: they are
reworked after this lands.

egzo is pre-1.0: replace things outright, with no aliases, no migration and no deprecation.

## The design in short

- Every port an agent may serve is declared by name in its template. Nothing else can ever open.
- A declared port is closed until the agent calls `expose(name, where)`, with `where` either `hub` or `local`, never both.
- `hub` is a preview, as built in #73 and #74. It takes an `http` port and a project connected to a hub.
- `local` is a port on the machine, through a small relay. It needs `local_ports` in the file, which is off by default, and no hub.
- `tcp` and `udp` are local only.
- `expose` answers the one address where the app can be reached.
- `egzo agent port-forward` is removed. `egzo agent forward` stays: previews use it.

## The project file

```yaml
local_ports:             # optional: without it, local ports are off
  bind: 192.168.8.242    # required: the IP to listen on
  host: mybox.lan        # optional, defaults to bind; required for 0.0.0.0 and ::
  pool: 20000-29999      # optional, this is the default
  max_exposed: 10        # optional, this is the default: how many local ports may be open at once in the project

agents:
  frontend:
    harness: claude-code
    ports:
      web: "5173/http"
      db: "5432/tcp"
      voice: "5432/udp"
  reviewer:
    harness: claude-code   # no ports: it is told nothing, and no relay is made
```

The minimum to turn local ports on is `local_ports: { bind: 127.0.0.1 }`.

Refused by `Resolve`, so by `up` and `project check`, with the usual `egzo.yaml:LINE:COL` errors:

- A port that is not `PORT/PROTOCOL`: the protocol is required and is `http`, `tcp` or `udp`, the port is 1 to 65535.
- A port name outside `[a-z][a-z0-9-]*`, or over 32 characters.
- More than 32 ports in a template: `maxExposures`, the per-agent bound that keeps status lines small.
- `max_exposed` below 1, or more `tcp` and `udp` ports in a template than `max_exposed`: they could not all be open at once.
- The same port number twice in a template, except one `tcp` and one `udp`.
- A `tcp` or `udp` port in a project with no `local_ports`: it could never open.
- An `http` port in a project with neither `hub` nor `local_ports`: it could never open.
- `local_ports` with no `bind`, a `bind` that is not an IP, `0.0.0.0` or `::` with no `host`, or a `pool` that is not `LOW-HIGH` within 1 to 65535.

An IP that does not exist on the machine is not checked and gets no special handling: the relay fails to start and the engine's error is what the
person sees, like any server. DHCP is the operator's concern.

## What `expose` does

```
expose(name, where)   where is required: "hub" or "local"
unexpose(name)
```

| Call | Answer |
|---|---|
| `http`, `hub`, a hub serves the project | `address: https://<id>-<project>.<domain>/` |
| `http`, `local` | `address: http://<host>:<outside>/` |
| `tcp` or `udp`, `local` | `address: <host>:<outside>` |
| `tcp` or `udp`, `hub` | Refused: raw ports are local only |
| `hub`, no hub serves the project | Refused: `previewTemplate()` is empty |
| `local`, no `local_ports` | Refused: local ports are off in this project |
| A name that is not declared | Refused, the message lists the agent's names |
| The other `where` while one is open | Refused: unexpose it first |
| The same call again | The same answer, nothing changes |
| `local`, with `max_exposed` local ports already open in the project | Refused: unexpose one first |

The answer is `{name, port, protocol, where, address}`, plus `id` for `hub`. `command` and the free-form arguments (`port`, `protocol`, display name) go.

`unexpose` answers `{ok: true, note}`, over MCP and HTTP alike, and `note` is always exactly: `Active connections may remain active - close your server to end them.`
The rest of the behaviour stays: the budget in `messages.go`, the `exposed` and `unexposed` events, a boot ending every exposure, and the status feed.
`maxExposures` stays at 32 per agent. `local_ports.max_exposed`, default 10, counts `local` exposures across the project and nothing else: `hub` exposures are
not counted. The count takes `exposureMu` and covers every agent. `Desire` hands the value to the control sidecar as `EGZO_LOCAL_MAX_EXPOSED`, so changing it
takes an `up`. Lowering it below what is open closes nothing: it only refuses further exposures. Hub preview limits are not part of this work. They may come
later, when `hub` grows into an object.

Exposures are keyed by name. A hub `id` stays the HMAC of registration and port.

## Telling the agent

Only an agent with ports is told. Remove the ports paragraph from `PlatformInstructions`. `agentContainer` sets one variable when the template has ports:

```
EGZO_PORTS={"ports":{"web":"5173/http","db":"5432/tcp"},"hub":true,"local":true}
```

`hub` is whether the project names a hub, `local` whether it has `local_ports`. `harness.Instructions` takes this and adds a paragraph that lists
the ports, says to use those ports for those servers and bind `0.0.0.0`, says they are closed until `expose(name, where)`, says only to expose one when a
person needs to reach it, and offers only the values of `where` this project has. Keep the tool descriptions in `mcp.go` free of per-agent text. All
three harnesses call `Instructions`: `claudecode.go`, `codex.go`, `opencode.go`.

`EGZO_PORTS` only informs. Control enforces its own list, below.

## The relay

Only made when the template has ports and the project has `local_ports`. One relay per instance, whatever its number of ports.

```
client -> published port -> relay (on <project>_local_ports and the agent's internal network) -> agent:PORT
```

- **Role:** `egzo relay`, a hidden command in the egzo image, package `internal/relay`. It holds no secret of the project, no engine socket and no
  volume. Container `<project>-<instance>-relay-1`, kind `relay`, a new `kindRelay` in `desired.go`.
- **Hardening:** `sidecarUser`, read-only root, the existing cap drop and `no-new-privileges`, 64 MiB, a pids limit of 64, `Restart: unless-stopped`.
- **Network:** `up` makes one `<project>_local_ports` network, not internal, only when the project has `local_ports`. Relays share it so a project
  uses one address pool, not one per agent. The agent's own network stays internal: an agent must never join this one.
- **Order:** an internal network as the first network loses its published ports silently. So create the relay on `<project>_local_ports`, connect it
  to the agent's network, then start it. Add a field to `ContainerSpec` for the published ports and one for the extra network, and handle both in
  `createContainer`.
- **It lives as long as the instance.** It stays up while the agent is stopped, which keeps the port taken and means `agent stop` and `agent start` do
  not touch it. A connection while the agent is down just fails.
- **It listens on both its networks, and turns the agent away.** Docker delivers to the network a port was published on, but rootless Podman's `rootlessport`
  takes one of the container's networks at each start, the first in a Go map, so it may be the agent's. The relay looks up the agent at each new connection or
  UDP session and closes anything from that address, before the gate and before any byte goes through. The agent can connect to the relay on its own
  network, and reaches nothing. The check is on the agent's address and never on the network: Podman's forwarder connects from the relay's own address.
- **The target** is the agent's container name, resolved at each connection: the agent may not be up yet.
- **The gate:** on each new TCP connection or UDP session, the relay calls `GET /v1/expose` on control with the instance's `EGZO_TOKEN` and goes ahead only if
  that name is open with `where: local`. If control does not answer, the connection is refused. `unexpose` stops new connections. An established one
  lasts until it closes, and a UDP session until it idles out, which is what the note in `unexpose`'s answer tells the agent.
- **TCP:** both directions copied, half close kept, dial timeout 5 s, at most 64 connections per port.
- **UDP:** one socket per client address, at most 64 per port, idle timeout 2 minutes, a datagram that does not fit is dropped. Replies go only to the client
  of that session. Nothing the agent sends can start a session.
- `http` is relayed as TCP. The relay does not read it.

### Stable outside ports

The port is chosen once, at spawn, and pinned as an exact host port in the relay's definition, so stop, start and a reboot keep it and `agent rm` frees it.
Do not use a host port range in `-p`: the engine would choose again.

1. For each port name, `sha256(instance id + name)` modulo the pool size gives a start. Skip ports in use by the project's other relays, and ports already
   chosen for this relay.
2. Create and start the relay. If the start fails with `port is already allocated` or `address already in use`, remove it, treat that port as
   taken and try again, up to 16 times.
3. Any other error fails the spawn with the engine's message, and the undo stack removes what was made.

### Where it goes in spawn and removal

- `spawnSteps` in `internal/stack/spawn.go`: after `fetchTokens` and `bindProxy`, before the agent's container. Add it to the undo stack. Then
  `PUT /v1/agents/{name}/ports` tells control the declared list with each outside address. Send this for every agent with ports, relay or not, and store
  it as `<dir>/ports/<agent>.json`, removed with the registration. An agent with no such file has no ports.
- `DesireInstance` returns the relay spec; see how `resources` and `container` are kept apart in `Spawn`, because the relay needs the token.
- `RemoveInstances`: remove the relay right after the agent's container and before the network. Check `hub`'s path too: `internal/link/engine.go` `Remove`.
- `internal/stack/plan.go:72`: `up` removes what it does not want, so keep containers of kind `relay` with an instance as it keeps agents.
- `Down` takes containers before networks, so a relay needs no special case. Check it.
- `Template` gets the project's local ports and whether it names a hub, so `templateHash` covers them and a changed `local_ports` makes instances stale,
  and the existing stale flow applies. `Published.View` must carry them into `DesireInstance`.

## What the hub changes

Little: `hub` exposures go through the same path as today.

- `hublink.Exposure` gets `Where`, and the status feed carries it along with `Name`.
- `Link.noteExposures` and `hub/internal/hub/previews.go` `exposedIn` keep only `where: hub`. A local exposure must never be served as a preview. Test both.
- `tcp` no longer reaches the hub, but leave the `forward` code as it is: hub TCP may come later.
- The panel is #76's work, and is not touched here.

## Work, in order

Specs first for core. A spec for something not built yet fails: no `xfail`, `skip` or `todo` for that. Commit each step when it works.

1. **Roadmap.** Commit this file and trim `roadmap/agent-ports.md` to point here. Update the mentions in `roadmap/hub.md`, `hub-link.md`, `hub-settings.md`,
   `messaging-adapters.md`, `windows-and-macos.md` and `support-engine-named-volumes.md` where they describe what changes.
2. **Project file.** Specs in `specs/project/test_project_ports.py` for every refusal above and for a file that passes. `internal/config`: `Agent.Ports`,
   `File.LocalPorts`, their resolved forms, checks in `resolve.go`, and `keys.go` picks the keys up from the types.
3. **`expose` by name.** Rewrite `specs/agent/test_agent_expose.py`, `test_agent_expose_boot.py` and `test_agent_expose_hub.py`. Change `internal/control/exposures.go`,
   `agentapi.go` (`POST /v1/expose` with `{name, where}`, `DELETE /v1/expose/{name}`, a new `GET /v1/expose`), `mcp.go`, `api.go` (`PUT /v1/agents/{name}/ports`,
   removal with the registration), `events.go`, `internal/stack/apply.go` and `engine_commands.go` (`ps` shows `web:hub,db:local` under `PORTS`, `--json`
   has the address), and the hub changes above. Update `specs/fixtures/fakehub/main.go` and `specs/hubkit.py`, and the test projects, whose agents must now
   declare ports.
4. **Instructions.** `EGZO_PORTS` and `harness.Instructions`, with a spec in `specs/harness/test_harness_integrations.py`: an agent with ports is told them and
   one without is told nothing about ports.
5. **The relay.** `internal/relay` with unit tests, including TCP half close, UDP sessions, limits, idle timeout and a refused gate. Then the stack changes. Specs in
   `specs/agent/test_agent_local_ports.py`, listed below.
6. **Remove `port-forward`.** Delete `internal/cli/portforward.go` and its test, `specs/agent/test_agent_port_forward.py`, and what only it uses in
   `internal/portforward` (`Mapping`, `ParseMapping`, `Command`). Keep `Forward`, `AnnouncedCommand`, `Connected` and `specs/agent/test_agent_forward_verb.py`.
   Check `specs/clitree.py` and `specs/test_meta.py` for the command tree.
7. **Docs.** `docs/project-file.md` (both new keys), `docs/managing-projects.md` (replace "Opening an agent's dev server"), `docs/messaging.md` (the ports section),
   `docs/hub.md`, `docs/security.md`, `docs/sandboxing.md`, `DESIGN.md`, `docs/README.md`, `known-issues/mcp-support-is-required.md`. `security.md` must stop saying nothing is
   published. It says what is: only declared ports, only when an agent exposes them, only in a project with `local_ports`, and with no sign-in, so `bind` is
   a decision about who can reach the app.

## Specs for local ports

Each one is a command-literal test in the existing style. Agents share a module's project: the machine has a limited number of networks.

- With no `local_ports`: no relay exists and no `_local_ports` network exists.
- An agent from a template with no ports has no relay.
- A spawned agent's port refuses connections until `expose`, and works after it, for TCP and for UDP.
- A server bound to `0.0.0.0` answers at the address `expose` returned, for `http`, `tcp` and `udp`.
- `unexpose` stops new connections, and its answer carries the note about active connections.
- Two instances of one template expose the same name and get different addresses, each reaching its own server.
- The address is the same after `agent stop` and `agent start`, and after a restart of the relay.
- `agent rm` removes the relay. `project down` leaves none.
- The agent cannot reach anything through the relay: with a relay attached it has no route out, and what it sends the relay is closed or dropped before the gate.
- One more than `max_exposed` open locally in the project is refused, with the default and with a value set in the file, and `hub` exposures do not count towards it.
- A local exposure is not served as a preview.

A spec that uses something a reference platform lacks is a strict `xfail` on that platform only. Rootless Podman is one to watch for `bind` and for
the relay's second network.

## Working rules

- Update every spec this work touches. Locally run only the specs you added or changed, by file, with `--engine docker`, and never the whole suite: CI does that.
- Run the Go unit tests with `-race`: `go test -race ./...` and `make hub-test` for `hub/`.
- No load or scale experiments. The machine has 8 GB and runs live agents. A probe with a container or two, under a hard `timeout`, with cleanup, is fine.
- Core never imports `hub/`. `internal/link` must not import `internal/control`: shared constants go in `hublink`.
- Control stays without an engine socket: agents can reach it.
- Never watch the whole CI matrix. Debug one cell at a time with `pytest -x`.
- Push and open the PR only when the user asks. Target `main`.

## Done when

- Every step above is committed on `declared-local-ports`, Go tests pass with `-race`, and the added and changed specs pass locally.
- A project with `local_ports: { bind: 127.0.0.1 }` and a `5432/tcp` port, with no hub, can have an agent expose it and be reached at the address it answered.
- A project with neither `hub` nor `local_ports` and no ports behaves exactly as it does today, with no extra container or network.
- Nothing in the code, docs or specs still mentions `port-forward` or `expose` with a port number.
