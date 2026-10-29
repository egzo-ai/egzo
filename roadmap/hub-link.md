# Roadmap: a project links itself to a hub

**Status.** Draft, direction decided (below); not built. It replaces the hub's engines: when it lands, the hub no longer connects
to engines, and everything in [hub.md](hub.md) about engines as data, engine credentials and discovery by labels goes away.
Pre-1.0: no migration and no second mode. The old path is removed, not kept next to the new one.

## Why

The hub connects to engines (a unix socket, tcp+TLS, ssh) and pulls everything through the engine API. That model cracks:

- **The hub holds root-equivalent access to every engine**: a socket, a TLS client certificate, an ssh key. A compromised hub owns
  every host behind it.
- **The hub must reach every engine.** A laptop, a box behind NAT or a company network cannot be added.
- **The hub sees everything on an engine**: every project with egzo labels, whether its owner wants it shown or not.
- **The hub pushes its settings into projects**: the preview domain of [agent-ports.md](agent-ports.md) had to be written into
  the control sidecar by `engine exec`, and two hubs on one engine fight over it.
- **Every feed is a long pull across the distance**: the engine's events, the status log, the audit log, the exec streams.

## The model

A project names its hub, and connects to it:

```yaml
hub: https://my-egzo.example.org
```

- **One hub per project.** The project file is the only place that says so; removing the key and running `egzo project up`
  unlinks it.
- **The project dials out.** `egzo project up` starts a **link** sidecar (the same binary, role `egzo link`, like control and the
  proxy). The link holds one outbound HTTPS connection to the hub on port 443, which NAT and company proxies pass. Nothing on the
  project's host listens on anything.
- **The hub asks, the link does.** Down the connection the hub sends egzo's verbs: list, spawn, start, stop, remove, attach, open a
  preview stream. The link runs them on its own engine with the core packages the hub calls today (`stack.Spawn`,
  `RemoveInstances`, `Observe`, `ExecStream`). Up the connection the link pushes the project as it changes, the agents' status
  feed and the proxy's audit trail.
- **The link exposes egzo's verbs and nothing else.** The hub cannot run a container, mount a path or exec a command of its
  choosing: it spawns from the project's own templates and attaches to the agents' sessions. The project file and the CLI stay
  the only way to change a project.
- **The hub holds no engine credentials.** It needs neither the Docker socket nor an ssh key, and its compose file mounts nothing
  of the host.

| | Before (the hub pulls) | After (projects link) |
|---|---|---|
| A compromised hub | root on every engine host | egzo's verbs on the linked projects: spawn from their templates, attach to their agents |
| Reachability | the hub reaches each engine | each project reaches the hub, outbound 443 |
| What the hub shows | everything on its engines | the projects whose owners linked them |
| Who decides | the hub's operator | the project's owner, in the project file |
| Secrets in the hub's database | engine credentials | none from projects (the public key of each link) |

## Registering

The first `egzo project up` with a `hub:` registers the project, like a device code:

```console
$ egzo project up
...
link: register this project at https://my-egzo.example.org/link/QXRB-7KPM (waiting; Ctrl-C to finish later)
link: registered as web, owned by alice
```

- The link makes an Ed25519 key pair. The private key stays on the link's volume, sealed with the
  [project secret key](../docs/project-file.md#the-secret-key); the hub keeps the public key with the registration.
- A person signed in to the hub opens the address, sees the project (its name, the host it runs on, the templates), and approves
  it. They become the project's owner on the hub. **Who may approve is to be decided**, with the review of the roles below: some
  privilege on the hub, not every account.
- `up` does not wait for the approval: the project runs without the hub until someone approves, and the link keeps asking.
  `egzo project check` and `egzo ps` say whether it is linked, pending, refused or unreachable.
- Each connection afterwards proves the key (a signature over a challenge from the hub). The hub is authenticated by TLS with
  the system's roots, or the CA the project names for it.
- **Unlinking:** removing `hub:` and running `up`, or `egzo project down`, removes the link and tells the hub, which forgets the
  project. The hub's operator can also revoke a project; its link is then refused, and `up` says so. A project registered again
  is a new project on the hub, with no roles.

## The link sidecar

- On a network of its own, with a route out to the hub. Never on an agent's network: the agents do not reach it, and it does not
  reach them except through the engine, like the CLI.
- It reaches its engine through the engine's socket, mounted by `up`: it is root-equivalent on its host, like the CLI that
  created it, and like the hub's quick start today. Rootless Podman keeps that to the user's account.
- It follows the engine's events and the control sidecar's and the proxy's logs itself (the code of the hub's `docker_feed.go`
  moves here) and pushes what changed: the hub runs nothing on the engine to keep up.
- **gVisor**: a container under gVisor probably cannot connect to a host unix socket through a bind mount (DESIGN.md, "Control
  plane", to verify). If the spike confirms it, a project on a gVisor engine cannot be linked: `up` refuses `hub:` there and says
  why. The hub is optional, gVisor still works without it, and a project that needs that isolation runs unlinked on its own
  machine.
- One link per project, created and removed with it, recreated by `up` when its image changes.

## The connection

- **A WebSocket carrying a stream multiplexer** (yamux or similar, chosen in the spike): it passes reverse proxies and company
  proxies that an HTTP/2 stream to a backend does not. One stream per verb, per terminal and per preview connection; the feeds
  have one each.
- **A versioned protocol.** Link and hub are separate releases on separate machines: the protocol says its version at the
  handshake, and a hub refuses a link it does not speak with a message that says which to upgrade
  ([control-compatibility.md](control-compatibility.md) becomes this). Before 1.0, one version at a time: no ranges, no
  compatibility shims.
- **The protocol lives in core** (`internal/hublink`: the messages, the link's side), and the hub imports it, so core still never
  imports the hub. The hub stops importing `stack`, `engine` and their dependencies (compose-go, the Docker client).
- **Reconnecting** with a growing delay, from one second to a minute. A dropped connection ends the terminals and previews that
  ran over it; a task that ran is `interrupted`, as at a hub restart. The project shows as **offline since 14:02** on the hub.
- **The hub must be reachable by the projects**, which turns its posture around: today it is "meant for a private network".
  Only the link endpoint has to be: it can be its own listener (`EGZO_LINK_LISTEN`) and host name, accepting registered keys
  and pending registrations only. The UI stays where it is.
- **What a link says is untrusted.** A linked project speaks for itself only; its names, status lines and audit events are data,
  shown escaped, limited in size and rate.

## What changes in the hub

Removed, with their code, settings and docs:

- **Engines**: Settings, Engines, `EGZO_LOCAL_ENGINE`, the engine credentials and their encryption, the ssh host key pinning,
  `hub/internal/engines/docker.go` and `ssh.go`, the engine badge and filter, `(engine, project)` as the key of everything.
- **Discovery by labels and the scan interval** (`EGZO_SCAN_INTERVAL`): the hub knows the projects that linked.
- **Following logs and events through the engine API**: the link pushes.
- **The Docker socket in `hub/compose.yaml`** and its warning.

Changed:

- **A project is a registration**: an id from the hub, the project's name, the host it says it runs on. URLs carry the id
  (`/p/<id>/a/coder-1`). Two projects named `web` on two machines are two projects.
- **The `Engine` seam stays** and gets one implementation: the link. Tasks, terminals with take-over, the Network tab, Activity and
  the audit log keep their logic; what they call goes over the connection.
- **Roles are reviewed**: who may approve a registration, and the **viewer** role that previews need ([agent-ports.md](agent-ports.md)).
- **The quick start**: the hub without the socket, and a project on the same machine linking to it.
- [messaging-adapters.md](messaging-adapters.md) and [discovery.md](discovery.md) are rewritten for the link: the operator API
  and the event stream they use are reached through it instead of `engine exec`.
- `EGZO_SECRET_KEY` keeps what it still protects (the sign-in provider's secret, sessions), or goes if nothing is left.

## Specs and tests

- Core, specs first: `hub:` in the project file and `check --resolved`; registration (pending, approved, refused, revoked) and
  each verb against a small fake hub in `specs/fixtures`; the link reconnecting; nothing listening on the host; the link not
  reachable from an agent; `down` unlinking; `up` refusing `hub:` on gVisor, if the spike says so.
- The hub: Go unit tests with the seam faked, as now; the link endpoint tested with core's link against `httptest`;
  `make hub-stack` with a real project linking to the hub in the compose stack.

## Order of work

1. **Spikes**: the link as a sidecar with the engine's socket on Docker, rootless Podman and gVisor; a WebSocket with a
   multiplexer through a company HTTP proxy and through Caddy and nginx in front of the hub.
2. **Core**: `hub:`, the `link` role, registration, the protocol with the verbs and streams, the pushed feeds, with specs.
3. **The hub**: the link endpoint, the seam over the link, registration pages, projects by id; then remove the engines and
   everything listed above.
4. **Previews** ([agent-ports.md](agent-ports.md)) over the link.
5. **Roles**: approving registrations, the viewer role.

## Open questions

- Who may approve a registration (with the roles review).
- Narrowing what the hub may do from the project file (`hub: { url: ..., allow: [attach, previews] }`): a later idea.
- What the hub shows of a project that is offline: its last known state, marked as such, or nothing but its name.

## Alternatives considered

- **Keeping the engines next to the links**: two opposite implementations of everything the hub does. Rejected.
- **The link as a host service** (`egzo hub connect`, a systemd unit using the CLI's own engine connection): no socket mount,
  works with gVisor, one per machine. But it is installed and kept running apart from the projects, whose lifecycle it does not
  follow, and "a project connects itself" becomes "a machine does". Kept as the fallback if the sidecar fails the spike on
  rootless Podman.
- **The control sidecar as the link**: it is on every agent's network, and giving it the engine would make a container the
  agents talk to root-equivalent. Rejected.
- **HTTP/2 instead of a WebSocket**: cleaner streams, but common reverse proxies do not pass long bidirectional HTTP/2 streams to
  a backend.
