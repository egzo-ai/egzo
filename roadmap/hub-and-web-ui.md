# Roadmap: egzo-hub + web UI (planning, not built)

Moved out of DESIGN.md. Core implements none of this; what core guarantees for it is summarised in DESIGN.md, "Not in core".

## Model
- The hub + web UI is a **separate deliverable** (`egzo-hub`, own binary and image), distributed as a
  docker-compose stack and allowed its own database. It lives in the same git repository as core
  and shares code through `internal/` (one Go module); it is not part of the `egzo` CLI.
- The hub connects to **engines** (docker/podman: unix socket, tcp+TLS, ssh; local or remote) and
  manages egzo projects and agents running on them. Quick start: mount the local docker socket
  (root-equivalent; docs must say so and point to rootless engine / ssh context as safer).
- Multi-project (like a docker UI shows all compose projects) and multi-user. **Users are global
  to the hub instance** and live only in the hub database.
- **Observe and operate only.** The project YAML + CLI is the one and only way to create or change
  a project (`up`, `down`, recreate, edit). The hub never writes YAML and never applies config.
  - Observe: dashboard, status, event timeline, logs, proxy audit, read-only view of the spec.
  - Operate: spawn instances of declared templates, start/stop/restart/remove instances, interrupt,
    attach, send messages, answer questions agents ask of people.
  - Collaborate: presence, one writer + read-only observers per agent (read-only attach), shared
    timeline with sender on every message, questions routed to users.
- **Secrets are CLI-only.** The hub never reads or writes secret values; it may list which secrets
  exist and which agents use them. A hub compromise must not leak credentials.
- **No users in YAML files, ever.** User identities, roles and per-project grants live in the hub
  database. YAML may only define which *permissions/roles exist* and what each allows, never who
  holds them.
- Trust: whoever holds the engine access is trusted. The hub is the policy enforcement point for
  its users; it passes the acting user as an asserted `actor` field. No token signing/verification
  in projects. The CLI stays "OS user = operator".

## Messaging clients are hub features
Discord (later Slack, Telegram, Teams) is implemented in the hub, not in core projects. The hub
holds one bot per instance, maps chat identities to hub users (a field on the user record),
enforces the same permissions as the web UI, lays out channels across projects, and routes
questions agents ask (`ask`) to the right person. Core only provides the contract below; a messaging
client is just another client of it, tested in core with a fake client. Consequence: no Discord
without a hub (accepted). A hub-less bridge could later be a separate tool on the same contract.

## Core <-> hub contract (public API; version it)
1. Label schema (`ai.egzo.project|service|kind|config-hash|spec-version|project-dir|created-by` plus
   `ai.egzo.agent.*`).
2. Control sidecar operator API (status, event stream, message queue, spec snapshot read),
   versioned, reached via `engine exec` only (unix socket inside the container): no published
   ports, identical over socket/TLS/ssh. The agent-facing TCP API is separate and not for the hub.
3. Terminal attach = engine exec attach (hub bridges it to xterm.js).
4. Self-describing projects: flat labels + `inspect` give the agent/project card (observed state,
   harness, workspace) with no exec; the redacted resolved-spec snapshot on the control volume
   (read via exec) gives the detailed view while control is running.
5. Shared Go packages (list, inspect, read-spec, start/stop, attach, send) reused by the hub.
6. Messaging contract for any client (CLI, web, chat): `send` + queue API (to an agent); typed
   event stream (message sent, announced, fetched, resolved; status change; question asked and answered); questions (`ask`) with a
   stable message id so they can be answered later from anywhere; actor on every message/answer.

## Foundation requirements for core
- Stable, versioned labels and control API (above).
- Principal/address format `user:<id>`, `agent:<project>/<name>` in message envelopes, audit
  entries and permissions from day one; actor field everywhere (default `operator`).
- Permission vocabulary per project (e.g. observe, attach-rw, send, answer, lifecycle), defined in
  YAML as roles->permissions, with no user bindings.
- Schema distinguishes host-bound workspaces (bind) from portable ones (git/volume) so a project
  that is operated on a remote engine cannot silently depend on host paths.
- Core logic lives in importable Go packages, not inside CLI command code.

## Hub risks to track
Privileged socket access (rootless + later label-scoped proxy); engine credentials for remote
engines (ssh/TLS) stored by the hub; many exec streams (engine events + on-demand sidecar streams);
CLI `up` recreating containers while users are attached (hub must handle disconnects cleanly).
