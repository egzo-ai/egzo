# Roadmap: egzo-hub and web UI

**Status.** The hub and its web UI are built (`hub/`, see [docs/hub.md](../docs/hub.md) and "Built" at the end of
this page). The messaging adapters (NATS, `egzo-discord`) are a separate item: [messaging-adapters.md](messaging-adapters.md). Login through Dex
and "the first user is an operator" were replaced by [hub-login.md](hub-login.md). The hub connecting to engines was replaced by projects
linking to it ([hub-link.md](hub-link.md)): this page keeps what still holds, and engines are gone from it.

Core implements none of this; what core guarantees for it is summarised in DESIGN.md, "Not in core".

## Model

- The hub + web UI is a **separate deliverable** (`egzo-hub`, own binary and image), shipped as a docker-compose
  stack and allowed its own database. It lives in the same git repository as core, in `hub/` (its own Go module),
  and imports core packages, never the reverse (see "Code layout and rules"); it is not part of the `egzo` CLI.
- Projects **link themselves** to the hub ([hub-link.md](hub-link.md)). The key of everything is the id of a registration.
- Multi-project and multi-user. **Users are global to the hub** and live only in the hub database.
- **Observe and operate only.** The project YAML + CLI is the one and only way to create or change a project
  (`project up`, `project down`, recreate, edit). The hub never writes YAML and never applies config.
- **Secrets are CLI-only.** The hub never reads or writes secret values; it may list which secrets exist and which
  agents use them. A hub compromise must not leak credentials.
- **No users or roles in YAML, ever.** Identities, roles and grants live in the hub database. The roles are fixed by
  the hub (below); the project file knows nothing about them.
- Trust: the hub is the policy enforcement point for its users and passes the acting user as an asserted `actor` on what it does. No token signing in projects. The CLI stays
  "OS user = operator".

## Deployment

- A docker-compose project. No TLS termination: users put a TLS reverse proxy in front (out of scope); the hub
  exposes one high port.
- The public host is derived safely from `X-Forwarded-Host` and the related headers, following best practice (trust
  them only from a configured proxy).
- All configuration comes from environment variables, set in the compose file.

## Users and login

- **OIDC through Dex**, bundled as a compose service. The hub only speaks OIDC to it; the user plugs their provider
  (GitHub first) into Dex with Dex's own documentation. No provider code in the hub.
- An account takes an invite; signing in is not enough ([hub-access.md](../docs/hub-access.md)).
- **Operators** oversee every project and grant themselves a role to go further; operators make and remove other operators, and the last one stays.

## Permissions (RBAC)

- Resources: **projects and agents** only. Roles are granted per project. An agent has a creator field (the `actor` label that spawn already writes). Agents spawned
  from the CLI are the admins'.
- Roles:
  - **viewer**: the web proxy only.
  - **user**: spawn, start, stop, attach and delete their own agents.
  - **admin**: the same on every agent of the project.
  - **owner**: admin, plus granting and removing roles, and unlinking. Destroying a project (`project down`) is not a hub action, by design: the CLI does it.
- An owner cannot remove themselves or drop their own permissions. An owner can remove other owners. A project keeps a valid owner.
- Chat from Discord needs no fourth permission: in a channel an agent was added to, any registered user with a role on
  the project may talk to it (see "Who decides what" in [messaging-adapters.md](messaging-adapters.md)). It is not shown in the web UI.

## Web UI

Information model: projects → agents, services.

- **HUD**: the signed-in user.
- **Welcome page**: the projects the user can see. Click one to open it.
- **Project page**: the agents, with Start / Stop / Remove / Attach, and a top action to spawn an agent.
  Observe: status, event timeline, logs, a read-only view of the spec.
- **Agent page**: status, a terminal (tab or button; the hub bridges the link's attach to xterm.js), resource usage
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

## Core ↔ hub contract

The protocol of `internal/hublink`, versioned at the handshake ([hub-link.md](hub-link.md)): a link's verbs and feeds. The link runs the shared
Go packages (list, inspect, read-spec, start/stop, attach, send, spawn, rm, workspace put/get), which take an engine client and a `PrepEnv` and
read no file of the host. Actors: spawn writes the `actor` label (the hub passes `user:<id>`), and messages and answers carry one. Start, stop, rm
and attach leave no trace in core: the hub writes its own audit log for them, with the signed-in user.

## Hub design notes

- **An `Engine` seam owned by the hub** (`hub/internal/links`): the verbs of one project, implemented by `hublink.Peer` and faked in the hub's
  tests. That covers what is the hub's own logic: registrations, the operators' access, RBAC, a project going offline.
- Risks to track: `project up` recreating containers while users are attached (the hub must handle disconnects cleanly).

## Decided

- Roles are the hub's alone; no users or roles in YAML. Messaging is not in the web UI.
- Web UI: one flat project list with the host each project says it runs on; URLs carry the id of its registration.
- `admin`, the starter account, is the first operator ([hub-login.md](hub-login.md)). Operators oversee every project, and go further with a role.
- Hub code lives in `hub/`, a separate Go module; hub imports core, never the reverse; hub and adapters are
  unit-tested, not spec-driven (see "Code layout and rules").

## Built

In `hub/`, with the decisions above unless listed under "Not yet".

- The model: a separate module, projects that link themselves, observe and operate only, secrets never read, roles in the hub's database.
- Login through OIDC (Dex in the compose stack), with an invite for anyone new; operators oversee every project. Roles viewer / user / admin / owner per project, "own agents" by the `actor` label, an owner cannot
  remove or lower themselves. Every operation is audited, allowed or refused.
- Projects that link ([hub-link.md](hub-link.md)): the link endpoint, registrations made with a link code, projects by id, offline since a
  time, tasks interrupted when a link drops. What the link pushes replaces every listing and every log followed through an engine, and a
  running agent whose status cannot be read is **running**, never "starting". See `docs/hub.md`, "How the hub keeps up".
- Monitoring agents' internet access ([proxy-audit.md](proxy-audit.md)): the link pushes each proxy's audit trail, and the hub groups what was
  denied by instance, host and reason in its database, says why and how to fix it, and marks a group fixed when the agent gets through.
- Settings for operators ([hub-settings.md](hub-settings.md)): Links, Invites, and Users, where an operator makes or removes other
  operators; the last one cannot be removed.
- The web UI, a Svelte app on a JSON API ([web-ui-spa.md](web-ui-spa.md)): projects, project page (agents, spawn, start / stop / remove, services, timeline, network, spec, audit log, members), agent page
  (status, timeline, network, logs), and terminals: several at once, in a sidebar, held by one person each with presence and take-over. No
  messaging. A question an agent is waiting on is shown as a status.
- Tasks ([hub-tasks.md](hub-tasks.md)): a spawn, start, stop or removal is answered at once and runs in the background, shown on
  its agent with its step for everyone, one at a time per agent; a spawn is one agent from the start; failures stay until dismissed;
  the project's Activity tab lists them.
- Public address from `X-Forwarded-*`, believed only from configured proxies; same-origin checks on every state change and on
  the terminal websocket.
- Tests: Go unit tests with the seam faked, and core's real link against the endpoint; Playwright tests of the pages; `make hub-stack` (the
  compose stack: hub image, Dex, a real project that links itself, a real terminal).

## Not yet

- **The messaging adapters** (embedded NATS, the hub API for link and expose, `egzo-discord`): see
  [messaging-adapters.md](messaging-adapters.md).
- **Roles and invites, and previews over the link**: see [hub-link.md](hub-link.md), "Order of work".
- **Previews of an agent's dev server** (one origin per preview on a domain of its own), and the
  `__Host-` session cookie they need first: see [agent-ports.md](agent-ports.md).
- **Discovery, the rest** (a Refresh button, showing how fresh a listing is): see [discovery.md](discovery.md).
- **The timelines without a command in the sidecar.** The status is streamed, but the project and agent timelines, the spec and the
  templates are still read by running a command in the control sidecar: the timelines every 15 seconds while one is on the screen,
  the templates once per five minutes. Following the event stream the way the status is followed (a second line type on the
  sidecar's standard output, or a stream of its own) would remove the last periodic command.
- **Hub settings, later parts** (a hub-level audit view, removing users, a richer users list): see [hub-settings.md](hub-settings.md).
- **Incompatible controls** (the rest of it: a version the hub can compare): see [control-compatibility.md](control-compatibility.md), needed for 1.0.0.
- Resource usage, and a way for an owner to remove a user from the hub altogether.
