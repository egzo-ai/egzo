# Roadmap: egzo-hub and web UI

**Status.** The hub and its web UI are built (`hub/`, see [docs/hub.md](../docs/hub.md) and "Built" at the end of
this page). The messaging adapters (NATS, `egzo-discord`) are a separate item: [messaging-adapters.md](messaging-adapters.md). Login through Dex
and "the first user is an operator" were replaced by [hub-login.md](hub-login.md).

Core implements none of this; what core guarantees for it is summarised in DESIGN.md, "Not in core".

## Model

- The hub + web UI is a **separate deliverable** (`egzo-hub`, own binary and image), shipped as a docker-compose
  stack and allowed its own database. It lives in the same git repository as core, in `hub/` (its own Go module),
  and imports core packages, never the reverse (see "Code layout and rules"); it is not part of the `egzo` CLI.
- The hub connects to **engines** (docker/podman: unix socket, tcp+TLS, ssh; local or remote) and manages egzo
  projects and agents running on them. **Multi-engine from day one**: the key of everything is `(engine, project)`.
- Multi-project and multi-user. **Users are global to the hub** and live only in the hub database.
- **Observe and operate only.** The project YAML + CLI is the one and only way to create or change a project
  (`project up`, `project down`, recreate, edit). The hub never writes YAML and never applies config.
- **Secrets are CLI-only.** The hub never reads or writes secret values; it may list which secrets exist and which
  agents use them. A hub compromise must not leak credentials.
- **No users or roles in YAML, ever.** Identities, roles and grants live in the hub database. The roles are fixed by
  the hub (below); the project file knows nothing about them.
- Trust: whoever holds the engine access is trusted. The hub is the policy enforcement point for its users and
  passes the acting user as an asserted `actor` on what it does. No token signing in projects. The CLI stays
  "OS user = operator".

## Deployment

- A docker-compose project. No TLS termination: users put a TLS reverse proxy in front (out of scope); the hub
  exposes one high port.
- The public host is derived safely from `X-Forwarded-Host` and the related headers, following best practice (trust
  them only from a configured proxy).
- All configuration comes from environment variables, set in the compose file.
- **Engines are data, not configuration.** They live in the hub database and the operator adds, edits and removes
  them in the UI (the connection is tested before it is saved). Credentials are encrypted at rest with a key from the
  environment (`EGZO_SECRET_KEY`), write-only in the UI (replaced, never shown), and an ssh host key is pinned on first
  connect with its fingerprint shown to the operator; a changed key refuses to connect. The local socket engine is
  seeded from the environment so the quick start works unchanged.
- Quick start mounts the local docker socket. That is root-equivalent: the docs must say so and point to a rootless
  engine or an ssh context as the safer way.
- One engine being down must not take the page down: it is shown as unreachable, and the others keep working.

## Users and login

- **OIDC through Dex**, bundled as a compose service. The hub only speaks OIDC to it; the user plugs their provider
  (GitHub first) into Dex with Dex's own documentation. No provider code in the hub.
- A user is created on the first successful login and has access to nothing.
- **The very first user is an operator.** An operator holds **every project as its owner, without a grant** (see
  [hub-settings.md](hub-settings.md)); operators make and remove other operators, and the last one stays. That is the only
  special case; every other check is a normal RBAC check.
- To be invited, a user must already exist: they log in, see nothing, and an owner grants them a role.

## Permissions (RBAC)

- Resources: **projects and agents** only. Roles are granted per project (and per engine: the grant key is
  `(engine, project)`). An agent has a creator field (the `actor` label that spawn already writes). Agents spawned
  from the CLI belong to the operators (special case).
- Roles:
  - **user**: spawn, start, stop, attach and delete their own agents.
  - **administrator**: the same on every agent of the project.
  - **owner**: administrator, plus granting and removing roles. Destroying a project (`project down`) is not a hub action, by design: the CLI does it.
- An owner cannot remove themselves or drop their own permissions. An owner can remove other owners.
- Chat from Discord needs no fourth permission: in a channel an agent was added to, any registered user with a role on
  the project may talk to it (see "Who decides what" in [messaging-adapters.md](messaging-adapters.md)). It is not shown in the web UI.

## Web UI

Information model: engine → projects → agents, services.

- **HUD**: the signed-in user and the engine switcher (all configured engines, with their state).
- **Welcome page**: the projects the user can see, across engines. Click one to open it.
- **Project page**: the agents, with Start / Stop / Remove / Attach, and a top action to spawn an agent.
  Observe: status, event timeline, logs, a read-only view of the spec.
- **Agent page**: status, a terminal (tab or button; the hub bridges engine exec attach to xterm.js), resource usage
  (later). One terminal per agent at a time, with presence and take-over (no read-only observers: one pty has one size; the CLI is
  not managed, a documented known issue).
- **No messaging in the web UI.** Messages and questions are not shown, sent or answered here. Talking to an agent is
  the terminal, or a messaging adapter. The agent page can show that an agent is waiting on a question, from its
  status, so a person knows to go answer it elsewhere.

## Code layout and rules

**hub -> core, never core -> hub.** Any file is identifiable as hub or core by its path alone.

    cmd/egzo/, internal/, specs/   core (unchanged)
    hub/go.mod                     the hub module: github.com/egzo-ai/egzo/hub
    hub/cmd/egzo-hub/              the hub binary
    hub/internal/...               hub and adapter code (all of it: nothing importable outside hub/)
    hub/web/                       the web UI source (frontend toolchain stays inside hub/)
    hub/Dockerfile*, hub/compose.yaml   images, the compose stack

- **A separate Go module** (`hub/go.mod`). Core's `go.mod`, `staticcheck` and `govulncheck` never see NATS, OIDC,
  the frontend or Discord libraries. The hub requires core with `replace github.com/egzo-ai/egzo => ../`. Go's
  `internal` rule is path-based, so the hub can import core's `internal/...` packages (confirmed by the build).
- **The compiler enforces the direction.** Code under `hub/internal/` can be imported only from `hub/`, and
  `hub/cmd/` packages are `main`. So nothing hub-side is importable by core. Never put hub code in a non-`internal`
  package under `hub/`.
- **A lint check** (`make lint`) also fails if any core package depends on a path containing `/hub/`, as a guard
  against a future move.
- **The web UI lives in `hub/web/`**, built in the image build and embedded in the hub binary (`go:embed`); build
  output is not committed.
- **Tests**: hub and adapters do **not** use the `specs/` first model. They use plain Go unit tests and other best
  practices (the engine seam faked, an embedded NATS, `httptest`), and a separate CI job. `specs/` stays core only.
- `make lint`, `make test` and the images get hub targets that run inside `hub/`; core's targets do not touch it.
- Core docs and roadmap stay in `docs/` and `roadmap/`; hub documentation is part of them (`docs/hub.md`, `docs/hub-access.md`, `docs/hub-api.md`).

## Core ↔ hub contract (public API; version it)

1. Label schema (`ai.egzo.project|service|kind|config-hash|spec-version|project-dir|created-by|actor` plus
   `ai.egzo.agent.*`).
2. Control sidecar operator API (status, event stream, message queue, spec snapshot read), reached via `engine exec`
   only (unix socket inside the container): no published ports, identical over socket/TLS/ssh. The agent-facing TCP
   API is separate and not for the hub.
3. Terminal attach = engine exec attach.
4. Self-describing projects: flat labels + `inspect` give the agent/project card with no exec; the redacted
   resolved-spec snapshot on the control volume gives the detailed view while control is running.
5. Shared Go packages (list, inspect, read-spec, start/stop, attach, send, spawn, rm, workspace put/get) reused by the
   hub: they take an engine client and a `PrepEnv` and read no file of the host (done with portable workspaces).
6. Messaging contract for any client: `message send` + queue API, a typed event stream (message sent, announced,
   fetched, resolved; status change; question asked and answered), questions (`ask`) with a stable message id and
   an `actor` on every message and answer. Consumed by the hub on behalf of messaging adapters, not by the web UI.

## What core did for the hub

- [x] **An engine constructor that takes a host.** `engine.Dial(Target)`: a unix or tcp host, TLS material as PEM from
      memory, and an optional dialer. ssh is a dialer the hub supplies (`hub/internal/engines/ssh.go`, with the host key
      pinned), so core gains no ssh dependency. `Connect` is `Dial` with `DOCKER_HOST`.
- [x] **Version the operator API paths** (`/v1`). `/healthz` stays unversioned. The agent-facing API already was.
- [x] **Check that every operation records its actor.** Spawn writes the `actor` label (the hub passes `user:<id>`), and
      messages and answers carry one. Start, stop, rm and attach change engine state only and leave no trace in core: the
      hub writes its own audit log for them, with the signed-in user.
- [x] Verify the shared packages take a client and a `PrepEnv` and read no host file. The hub calls `stack.Spawn`,
      `RemoveInstances`, `Observe`, `ReadPublished` and the control requests from a process whose directory is not the
      project's (`make hub-integration`).
- Not needed in core: a roles/permissions vocabulary in YAML (dropped), and a fake of the engine client. A fake of the
  whole Docker API would drift from the real one, and the specs already test against real engines. The hub gets its
  own narrow interface instead (below).

## Hub design notes

- **An `Engine` seam owned by the hub**: `Projects`, `Agents`, `Spawn`, `Start`, `Stop`, `Rm`, `Attach`, implemented by
  delegating to the core packages, and faked in the hub's tests. That covers what is the hub's own logic: discovery,
  the operators' access, RBAC, fan-out over N engines, an engine going down.
- Discovery is by labels, per engine. The hub does not poll: it follows each engine's event stream and lists the engine again when a
  container of egzo changes, keeps a slow timer only as a fallback (`EGZO_SCAN_INTERVAL`, 60 seconds; 5 seconds for an engine whose
  events do not work), and follows each project's control sidecar log for what its agents are doing. Streams retry with growing delays.
- Risks to track: privileged socket access (rootless, later a label-scoped proxy); engine credentials for remote
  engines stored by the hub; one log stream per project with a running control sidecar and one event stream per engine (the hub holds them open); `project up`
  recreating containers while users are attached (the hub must handle disconnects cleanly).

## Decided

- Roles are the hub's alone; no users or roles in YAML. Messaging is not in the web UI.
- Web UI: one flat project list with an engine badge and an engine filter (both hidden with one engine); URLs carry
  the engine.
- Engine connections are data, managed by the operator in the UI (encrypted, write-only, host key pinned).
- The first login is an operator, with no claim code. Operators hold every project without a grant.
- Hub code lives in `hub/`, a separate Go module; hub imports core, never the reverse; hub and adapters are
  unit-tested, not spec-driven (see "Code layout and rules").

## Built

In `hub/`, with the decisions above unless listed under "Not yet".

- The model: a separate module, multi-engine, observe and operate only, secrets never read, roles in the hub's database.
- Login through OIDC (Dex in the compose stack); the first login is an operator, and operators hold every project without a
  grant. Roles user / administrator / owner per `(engine, project)`, "own agents" by the `actor` label, an owner cannot
  remove or lower themselves. Every operation is audited, allowed or refused.
- Streams instead of polling ([container logging](../known-issues/container-logging-follows-the-engine-default.md)): the control
  sidecar logs the agents' status as JSON on standard output (on change, and a heartbeat every 30 seconds); the hub follows it through
  the engine's log API and the engine's events for discovery, retries both with backoff, and serves the pages from what it holds. A
  running agent whose status cannot be read is **running**, never "starting", and the project says why. `egzo system check` tries the
  engine's default log driver. See `docs/hub.md`, "How the hub keeps up".
- Monitoring agents' internet access ([proxy-audit.md](proxy-audit.md)): the hub follows each proxy's audit trail, groups what was
  denied by instance, host and reason in its database, says why and how to fix it, and marks a group fixed when the agent gets through.
- Settings for operators ([hub-settings.md](hub-settings.md)): Engines, and Users, where an operator makes or removes other
  operators; the last one cannot be removed.
- Engines as data in the database (credentials sealed with `EGZO_SECRET_KEY`, write-only; ssh host key pinned after the
  operator confirms its fingerprint); one engine down does not take the page down.
- The web UI, a Svelte app on a JSON API ([web-ui-spa.md](web-ui-spa.md)): projects (engine badge and filter only with several
  engines), project page (agents, spawn, start / stop / remove, services, timeline, network, spec, audit log, members), agent page
  (status, timeline, network, logs), and terminals: several at once, in a sidebar, held by one person each with presence and take-over. No
  messaging. A question an agent is waiting on is shown as a status.
- Tasks ([hub-tasks.md](hub-tasks.md)): a spawn, start, stop or removal is answered at once and runs in the background, shown on
  its agent with its step for everyone, one at a time per agent; a spawn is one agent from the start; failures stay until dismissed;
  the project's Activity tab lists them.
- Public address from `X-Forwarded-*`, believed only from configured proxies; same-origin checks on every state change and on
  the terminal websocket.
- Tests: Go unit tests with the engine seam faked; Playwright tests of the pages; `make hub-integration` (real engine, real
  project) and `make hub-stack` (the compose stack: hub image, Dex, a real project, a real terminal).

## Not yet

- **The messaging adapters** (embedded NATS, the hub API for link and expose, `egzo-discord`): see
  [messaging-adapters.md](messaging-adapters.md).
- **Discovery, the rest** (a Refresh button, showing how fresh a listing is): see [discovery.md](discovery.md).
- **The timelines without a command in the sidecar.** The status is streamed, but the project and agent timelines, the spec and the
  templates are still read by running a command in the control sidecar: the timelines every 15 seconds while one is on the screen,
  the templates once per five minutes. Following the event stream the way the status is followed (a second line type on the
  sidecar's standard output, or a stream of its own) would remove the last periodic command.
- **Hub settings, later parts** (a hub-level audit view, removing users, a richer users list, break-glass operator): see [hub-settings.md](hub-settings.md).
- **Incompatible controls** (the rest of it: a version the hub can compare): see [control-compatibility.md](control-compatibility.md), needed for 1.0.0.
- Resource usage, and a way for an owner to remove a user from the hub altogether.
