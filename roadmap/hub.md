# Roadmap: egzo-hub, web UI and messaging adapters (planning, not built)

Replaces `hub-and-web-ui.md` and `web-ui-requirements.md`. Where the two disagreed, the web UI requirements won.
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
- **The very first user is the operator.** There is only one. The operator gets **owner on every project the hub has
  no owner for** (an orphan): at first login, and again whenever a scan finds a project (any engine) with no owner.
  That is the only special case; every check afterwards is a normal RBAC check.
- To be invited, a user must already exist: they log in, see nothing, and an owner grants them a role.

## Permissions (RBAC)

- Resources: **projects and agents** only. Roles are granted per project (and per engine: the grant key is
  `(engine, project)`). An agent has a creator field (the `actor` label that spawn already writes). Agents spawned
  from the CLI belong to the operator (special case).
- Roles:
  - **user**: spawn, start, stop, attach and delete their own agents.
  - **administrator**: the same on every agent of the project.
  - **owner**: administrator, plus granting and removing roles, plus destroying the project (`project down`).
- An owner cannot remove themselves or drop their own permissions. An owner can remove other owners.
- Chat from Discord needs no fourth permission: in a channel an agent was added to, any registered user with a role on
  the project may talk to it (see "Who decides what" in the messaging adapters). It is not shown in the web UI.

## Web UI

Information model: engine → projects → agents, services.

- **HUD**: the signed-in user and the engine switcher (all configured engines, with their state).
- **Welcome page**: the projects the user can see, across engines. Click one to open it.
- **Project page**: the agents, with Start / Stop / Remove / Attach, and a top action to spawn an agent.
  Observe: status, event timeline, logs, proxy audit, a read-only view of the spec.
- **Agent page**: status, a terminal (tab or button; the hub bridges engine exec attach to xterm.js), resource usage
  (later). One writer and read-only observers per agent (read-only attach), presence.
- **No messaging in the web UI.** Messages and questions are not shown, sent or answered here. Talking to an agent is
  the terminal, or a messaging adapter. The agent page can show that an agent is waiting on a question, from its
  status, so a person knows to go answer it elsewhere.

## Code layout and rules

**hub -> core, never core -> hub.** Any file is identifiable as hub or core by its path alone.

    cmd/egzo/, internal/, specs/   core (unchanged)
    hub/go.mod                     the hub module: github.com/egzo-ai/egzo/hub
    hub/cmd/egzo-hub/              the hub binary
    hub/cmd/egzo-discord/          the Discord adapter binary (each adapter: hub/cmd/egzo-<platform>/)
    hub/internal/...               hub and adapter code (all of it: nothing importable outside hub/)
    hub/web/                       the web UI source (frontend toolchain stays inside hub/)
    hub/docs/, hub/Dockerfile*, hub/compose.yaml   hub documentation, images, the compose stack

- **A separate Go module** (`hub/go.mod`). Core's `go.mod`, `staticcheck` and `govulncheck` never see NATS, OIDC,
  the frontend or Discord libraries. The hub requires core with `replace github.com/egzo-ai/egzo => ../`. Go's
  `internal` rule is path-based, so the hub can import core's `internal/...` packages. To confirm with the first
  build.
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
- Core docs and roadmap stay in `docs/` and `roadmap/`; hub documentation goes in `hub/docs/`.

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

## What core must still do for the hub

- [ ] **An engine constructor that takes a host.** `engine.Connect` builds its client from `DOCKER_HOST`
      (`client.FromEnv`), so a process opens one engine. The hub needs `Dial(host, credentials)` for unix, tcp+TLS
      and ssh. This is the one change multi-engine requires of core.
- [ ] **Version the operator API paths** (`/v1`). The agent-facing API already is.
- [ ] **Check that every operation records its actor** (spawn, start, stop, rm, attach, answer), and that spawn
      writes the creator the "own agents" rule reads.
- [ ] Verify the shared packages take a client and a `PrepEnv` and read no host file.
- Not needed in core: a roles/permissions vocabulary in YAML (dropped), and a fake of the engine client. A fake of the
  whole Docker API would drift from the real one, and the specs already test against real engines. The hub gets its
  own narrow interface instead (below).

## Hub design notes

- **An `Engine` seam owned by the hub**: `Projects`, `Agents`, `Spawn`, `Start`, `Stop`, `Rm`, `Attach`, implemented by
  delegating to the core packages, and faked in the hub's tests. That covers what is the hub's own logic: discovery,
  orphan grants, RBAC, fan-out over N engines, an engine going down.
- Discovery is by labels, per engine, on a timer and on engine events.
- Risks to track: privileged socket access (rootless, later a label-scoped proxy); engine credentials for remote
  engines stored by the hub; many exec streams (engine events plus on-demand sidecar streams); `project up`
  recreating containers while users are attached (the hub must handle disconnects cleanly).

## Messaging adapters

Messaging is out of the web UI. Agents are reached from chat platforms through **adapters**: one per platform
(`egzo-discord` first), each its own binary and image in this repository, an optional service of the compose stack.
The adapter is the **external gateway**: it is trusted to vet what comes in from its platform and decides what goes
out. The hub never learns a platform's concepts (guilds, roles, workspaces). Modeled on Scion's Discord plugin
(`inspiration/scion/extras/scion-discord`, studied, not copied): its register flow and question buttons are kept; its
project-level channel links, `@all` and single trust domain are dropped.

### Transport: one topic per agent

The hub embeds a **NATS** server (JetStream for durable consumers). To verify: that it embeds cleanly in the hub
binary, and JetStream's storage on a volume.

    agent.<engine>.<project>.<agent>.out    the hub publishes what the agent says: messages, questions, status
    agent.<engine>.<project>.<agent>.in     adapters publish what users say and answer; the hub forwards it to the agent
    hub.control                             the hub publishes: agent.removed, user.deleted, grant.changed

- The **agent is the topic**. The hub is only the relay between an agent (through the operator API) and its topic. It
  is the only subscriber to the engines' sidecar streams; adapters never see an engine.
- Every adapter subscribes to all agents' `.out` and `.control`, and selects what it relays to which place. The hub
  does not track what is exposed where. Adapters are trusted with all agent traffic.
- Static broker permissions, the same for every adapter: subscribe to `.out` and `hub.control`, publish to `.in`.
- Names are `[a-z0-9_-]`, so they are safe subject tokens.

### The hub API (two calls, from the adapter)

An authenticated HTTP API (`/v1`, a token per adapter) for the two moments an adapter needs the hub:
- **Link**: the adapter asks for a code for an external user; the user, logged in on the web UI, confirms it; the
  adapter polls for the result and gets the hub user id. The adapter stores the pair; the hub keeps nothing.
- **Expose**: the adapter asks whether hub user U may expose agent A (*administrator* on its project). Yes or no.

Nothing else goes through the API.

### Who owns what

| Adapter | Hub |
|---|---|
| vets what comes in; decides what goes out | users, roles, RBAC |
| exposes agents to places (agent → place) | the second check on every message `.in` |
| links external users to hub users, keeps the mapping | confirms link codes, answers expose checks |
| its own state (SQLite on a volume): roles, webhooks, message → agent map, pending questions | relays agent ↔ topic, publishes lifecycle events |

### Messages

- **Inbound**: the adapter vets the platform side (the sender is in the place, linked, the agent is exposed there) and
  publishes to the agent's `.in` with the hub user id, the text and the platform message id. The hub **double-checks**
  before relaying: the user exists, holds any role on the agent's project, the agent exists. If so it sends to the
  agent with `actor = user:<id>`. If not, the message is **dropped and audited**. There is no reply: external users
  do not get a refusal, the message simply does not go through. The platform message id makes it idempotent.
- **Outbound**: the adapter renders the agent's `.out` for its platform, only for places the agent is exposed to.
  Answers to a question and interrupts go `.in` under the same check.
- **Stale state**: the adapter reacts to `hub.control` (drop an exposure on `agent.removed`, a link on `user.deleted`,
  update on `grant.changed`) and reconciles with the hub on reconnect. A stale adapter can still relay output to a
  place after a hub user is deleted; the second check on `.in` stops the other direction. Accepted.
- **Capabilities**: an adapter handles what its platform supports (threads, buttons for question choices, rich text,
  per-agent identity, message length). Hub messages are one neutral model: text, choices, attachments.

### Who decides what

- **The place** (who can see and speak): the platform. A private channel protects an agent: the adapter only receives
  what it can view.
- **The person**: the hub. A sender must hold **any role on the agent's project**. Answering a question and
  interrupting use the same permission. Decided knowingly: a `user` can steer another user's agent through a shared
  channel; exposing it is an administrator's act, per channel. The stricter fallback: chat follows attach.

### Trust

A compromised adapter reads every agent's output and can speak as any hub user it names, to any agent that user may
chat with. It cannot reach engines, secrets or the hub's data beyond the two API calls. Accepted: adapters are trusted
and few. The hub does not record links or exposures, so the web UI cannot list or revoke where an agent is exposed.

### egzo-discord

One bot per guild. `/egzo register` is the link flow. Agents are exposed one at a time, as roles:

    /egzo agent add id:<engine:project/agent> [name:<role name>]    # run in the channel; default engine implied
    /egzo agent remove [--all]                                       # out of this channel, or out of every channel
    /egzo agent rename|list

- Adding calls the hub's expose check, then creates, once per guild, a **role** for the agent (mentionable, no
  members, named after the agent unless `name` is given) and records that the agent is **in this channel**. The
  binding is `role → agent` plus a **set of channels**; the first channel is the **home channel**, where unprompted
  events (a question asked while idle) go. The bot never adopts an existing role, refuses a name it already uses, and
  warns about a human-made role of the same name.
- `@coder` autocompletes and highlights; the bot receives `<@&roleid>`. Nobody holds the role, so a mention pings
  nobody. Several mentions route to several agents. No `@all`, no default agent: plain text with no mention and no
  context is ignored, and so is a mention in a channel the agent is not in. No direct messages (a role does not exist
  in a DM).
- A **reply** to an agent's message goes to that agent; a **thread** opened in a channel the agent is in goes to it.
  `/egzo say agent:<autocomplete> text` is the fallback and where `interrupt` is an option.
- Replies go to the channel or thread of the message that addressed the agent. The sender is forwarded with the text.
- The 250-role limit and a clash of names are the operator's problem: the command says what to rename. The role shows
  in the `@` picker of every channel, so an agent's name is visible to the guild; accepted.
- On `agent.removed` the bot deletes the role (or marks it dead if it cannot).
- Adding or removing needs Discord's Manage Roles and, via the hub, *administrator* on the project.
- Bot permissions: Manage Roles (its role above the agent roles), Send Messages, Create Public Threads, Embed Links,
  Read Message History, View Channels (granted per channel for private ones), Use Application Commands. Reading text
  needs the privileged **Message Content** intent (to verify what arrives without it).

## Decided

- Roles are the hub's alone; no users or roles in YAML. Messaging is not in the web UI.
- One NATS topic per agent (`.in`, `.out`), adapters subscribe to all agents; the hub only relays. Adapters are trusted.
- Two HTTP calls from adapters to the hub: link and expose. Everything else is on the bus.
- The hub double-checks every message `.in`; a refused message is dropped and audited, with no reply.
- Chat permission: any role on the project, in a place the agent was exposed to; the platform decides the place.
- Ids are `[engine:]project/agent` (no colon: the default engine) in commands, `actor` strings and audit; principal
  `agent:[engine:]project/name`; subjects use the full form with dots. Core's `project/agent` stays valid.
- No `via` field in core: the hub audits the platform identity from the message `.in`.
- Web UI: one flat project list with an engine badge and an engine filter (both hidden with one engine); URLs carry
  the engine.
- Engine connections are data, managed by the operator in the UI (encrypted, write-only, host key pinned).
- The first login is the operator, with no claim code. Projects with no owner are claimed by that login.
- Discord: enable the privileged Message Content intent. Without it content is empty except for messages that mention
  the bot, DMs, the bot's own messages and replies to the bot's messages; the docs do not say whether a role mention
  counts, so the role design cannot rely on it. Self-hosted bots in under 100 guilds need no verification (to re-check).
- Hub code lives in `hub/`, a separate Go module; hub imports core, never the reverse; hub and adapters are
  unit-tested, not spec-driven (see "Code layout and rules").
- An adapter is an optional service of the compose stack, in this repository, with its own binary and image.
- Adapter credentials come from the environment: one secret per adapter (`EGZO_ADAPTERS=discord:<secret>`), from which the
  hub derives the NATS user (static permissions) and the HTTP API token. Prototype the embedded NATS first.

## Open questions

None left. To verify while building: embed `nats-server` in the hub binary with JetStream on a volume (prototype first);
test whether the Discord bot receives content for a role mention without the Message Content intent.
