# Roadmap: a project links itself to a hub

**Status.** Core and the hub are built (steps 2 and 3 of the [order of work](#order-of-work)): the hub no longer connects to engines. Until roles
and invites land, only operators make link codes. Previews (step 4), roles and invites (step 5) and the rewrite of
[messaging-adapters.md](messaging-adapters.md) and [discovery.md](discovery.md) are open.

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

A signed-in person makes a code, and the project presents it. The link never registers itself: a stranger who reaches the endpoint has no way
to make the hub store anything.

```console
$ egzo project up
...
link: not registered: run `egzo project register`
$ egzo project register
Link code:
link: linked to https://my-egzo.example.org as p1a2b3c4d5e6, owned by alice
```

- A person who may create a project clicks **Link a project** in Settings, Links ("Roles and invites", below). The hub shows a code once. It is 128
  random bits, starts `egzo-link-` so that secret scanners find one that was committed, is good once, for 15 minutes, can be cancelled, and is kept as a
  SHA-256 only.
- The link makes an Ed25519 key pair. The private key stays on the link's volume, sealed with the
  [project secret key](../docs/project-file.md#the-secret-key); the hub keeps the public key with the registration.
- `egzo project register` asks for the code without echo, or reads it from stdin, and hands it to the link through `engine exec` stdin, like the secret
  key. There is no flag for it, and it is never in the project file: a file can be committed, and flags reach a shell's history and `ps`.
- The link puts the code in its proof, covered by the signature, after the protocol check: it cannot be moved to another key. The hub spends the code
  and makes the project in one transaction, approved, owned by whoever made the code.
- What the hub does with a connection:

  | Key | Code | Result | Stored |
  |---|---|---|---|
  | Unknown | none | `unregistered`, closed | nothing |
  | Unknown | wrong, expired or used | `unregistered`, closed: one answer, so that a failure says nothing | nothing |
  | Unknown | good | the code is spent, `approved`, served | the project, an audit entry |
  | Approved | any | the code is ignored and stays good, `approved`, served | nothing new |
  | Revoked | any | `revoked`, closed, the code stays good | nothing new |
  | Forgotten after a revocation | good | an unknown key: a new project with no roles | the project, an audit entry |

- `up` never prompts and never waits: the project runs without the hub until it is registered. A link the hub turned away stops dialing, until
  `register` or `up` wakes it. `egzo project check` and `egzo ps` say whether it is linked, not registered, revoked or unreachable.
- There is no preview: the operator does not see the name or the host before the project is in. The short life, the single use, the audit entries and
  Revoke limit what a leaked code can do, and do not remove it.
- Each connection afterwards proves the key (a signature over a challenge from the hub). The hub is authenticated by TLS with
  the system's roots, or the CA the project names for it.
- **Unlinking:** removing `hub:` and running `up`, or `egzo project down`, removes the link and tells the hub, which forgets the
  project. The hub's operator can also revoke a project; its link is then refused, and `up` says so. A revoked project cannot unlink: its record stays
  until an operator forgets it. A project registered again is a new project on the hub, with no roles.

## The link sidecar

- On a network of its own, with a route out to the hub. Never on an agent's network: the agents do not reach it, and it does not
  reach them except through the engine, like the CLI.
- It reaches its engine through the engine's socket, mounted by `up`: it is root-equivalent on its host, like the CLI that
  created it, and like the hub's quick start today. Rootless Podman keeps that to the user's account.
- It runs as the sidecars' user with the socket's group as its primary group. Only the engine's host knows that group and the CLI may
  be elsewhere, so `up` reads it with a throwaway container that mounts the socket. The socket's path is the one the CLI dials when
  that is a unix socket, and `/var/run/docker.sock` otherwise.
- The secret key and any `HTTPS_PROXY` reach it through `engine exec`, on stdin, so they are never in `inspect`, and it holds them in memory.
- **After a restart, `egzo project up` is required**, and resumes the project with the same key and registration. Whether something runs it
  unattended is outside egzo: a person, or a host's own boot script. egzo stores nothing to resume by itself. The link has a restart policy so
  that it is there to say `locked`, but it cannot connect until `up`: the hub shows the project offline.
- It follows the engine's events and the control sidecar's and the proxy's logs itself (the code of the hub's `docker_feed.go`
  moves here) and pushes what changed: the hub runs nothing on the engine to keep up.
- **gVisor**: a container under gVisor probably cannot connect to a host unix socket through a bind mount (DESIGN.md, "Control
  plane", to verify). egzo refuses nothing in advance: `up` starts the link as anywhere else, and if the engine stops it, the
  link fails with the engine's own error and `egzo ps` shows it. The specs run on gVisor like on every reference platform, and
  the docs say what works there. The hub is optional: a project that needs gVisor's isolation and cannot link runs unlinked.
- One link per project, created and removed with it, recreated by `up` when its image changes.

## The connection

- **A WebSocket carrying yamux** ([chosen in the spike](#spike-results)): it passes reverse proxies and company proxies that an
  HTTP/2 stream to a backend does not. One stream per verb, per terminal and per preview connection; the feeds have one each.
- **A versioned protocol.** Link and hub are separate releases on separate machines: the protocol says its version at the
  handshake, and a hub refuses a link it does not speak with a message that says which to upgrade
  ([control-compatibility.md](control-compatibility.md) becomes this). Before 1.0, one version at a time: no ranges, no
  compatibility shims.
- **The protocol lives in core** (`internal/hublink`: the messages and the hub's side, with no engine in it; `internal/link`: the link's
  side), and the hub imports `hublink`, so core still never imports the hub. The hub stops importing `stack`, `engine` and their
  dependencies (compose-go, the Docker client).
- **Nothing runs before an approval.** The link accepts no stream and pushes no feed until the hub says the project is approved, and stops
  both the moment that ends. The proof signs the hub's address too, so a hub cannot replay a challenge of another hub to its links.
- **Reconnecting** with a growing delay, from one second to a minute. A dropped connection ends the terminals and previews that
  ran over it; a task that ran is `interrupted`, as at a hub restart. The project shows as **offline since 14:02** on the hub.
- **The hub must be reachable by the projects**, which turns its posture around: today it is "meant for a private network".
  Only the link endpoint has to be: it can be its own listener (`EGZO_LINK_LISTEN`) and host name, accepting registered keys
  and links that present a code only. The UI stays where it is.
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
- **Roles and invites** replace today's roles and the operators' implicit ownership (below).
- **The quick start**: the hub without the socket, and a project on the same machine linking to it.
- [messaging-adapters.md](messaging-adapters.md) and [discovery.md](discovery.md) are rewritten for the link: the operator API
  and the event stream they use are reached through it instead of `engine exec`.
- `EGZO_SECRET_KEY` went: nothing is left for it to protect. `EGZO_PUBLIC_URL` or `EGZO_LINK_URL` is required instead, and a link's proof is
  checked against that address, never one taken from a request.

## Roles and invites (decided)

**Signing in is not enough: an invite is required.** Today a person who signs in with single sign-on gets an account with no
access, and waits for a grant. With a company-wide provider that is everyone in the company. Instead, the hub needs an **invite
system**: a person needs a sign-in **and** a valid invite, and an account without one is refused.

- An invite is a link with a one-time code: single use, expiring, revocable. It carries what it grants: a role on a project (sent
  by the project's owner), or the flags below (sent by an operator).
- It is redeemed by whoever signs in with it, by password or single sign-on. Nothing is matched by email
  ([hub-access.md](../docs/hub-access.md)).
- A person from outside (the viewer of a preview) needs an account: the invite is also how they get one, so the hub needs local
  accounts made from an invite, not only `admin` and single sign-on.

Roles are assigned on projects:

- **owner**: can destroy the project, which on the hub is **unlink only**: the hub forgets it and refuses its link. The project
  keeps running on its machine until someone runs `egzo project down` there; the hub never does. Can grant (invite). A project
  keeps at least one owner; an owner cannot remove or lower themselves.
- **admin**: access all agents (operate, attach, logs, network, the audit log), the CLI's included.
- **user**: can spawn their own agents, operate and attach to them, and view their logs and network.
- **viewer**: the web proxy only: a preview of any agent of the project, opened with its link. The link is not guessable but
  shareable ([agent-ports.md](agent-ports.md)): the agent or a member hands it over. A viewer sees nothing else of the project,
  not even a list of its previews.

Flags on users:

- **operator**: oversees the hub and all its resources: every project, its owners, its agents and their state, the links, the
  users. Manages users, invites, quotas, and revokes any project. To go further (a terminal, logs, a project's network), an
  operator grants themselves a role on the project, audited and shown on its Members tab like any grant. The starter account
  `admin` is an operator.
- **project quota**: how many projects the person may connect to the hub. 0 cannot create. Operators bypass it. It can be
  over-subscribed: lowering a quota under what a person already has removes nothing, it stops new ones.

A person disabled by an operator loses everything at once, as today. Their projects stay linked, so a project can be left with
no valid owner: the operators see it, and grant a new owner or revoke it.

## Specs and tests

- Core, specs first: `hub:` in the project file and `check --resolved`; registration (unregistered, approved, revoked) and
  each verb against a small fake hub in `specs/fixtures`; the link reconnecting; nothing listening on the host; the link not
  reachable from an agent; `down` unlinking. On gVisor, what does not work is a
  strict xfail of that platform, as for any unsupported feature.
- The hub: Go unit tests with the seam faked, as now; the link endpoint tested with core's link against `httptest`;
  `make hub-stack` with a real project linking to the hub in the compose stack.

## Order of work

1. **Spikes**: the link as a sidecar with the engine's socket, and a WebSocket with a multiplexer through a company HTTP proxy
   and through Caddy and nginx in front of the hub. Done on Docker ([results](#spike-results)); rootless Podman and gVisor are
   later spikes.
2. **Core**: `hub:`, the `link` role, registration, the protocol with the verbs and streams, the pushed feeds, with specs.
3. **The hub**: the link endpoint, the seam over the link, registration pages, projects by id; then remove the engines and
   everything listed above.
4. **Previews** ([agent-ports.md](agent-ports.md)) over the link.
5. **Roles and invites**: invites, the viewer role, the project quota, operators.

## Spike results

Run on 2026-10-31 on Docker 29.1.3 with runc. The clients and proxies ran in containers on one machine.

### The link as a sidecar

A probe in a container with the socket mounted and a sidecar's settings (`--cap-drop ALL`, `no-new-privileges`, read-only root,
a tmpfs on `/tmp`, a network of its own) did everything the link needs: list, an internal network, create and start a container,
follow events and logs, exec with stdin and stdout (8 MiB through `cat`), stop and remove, and outbound HTTPS.

- **The sidecar user cannot open the socket as it is.** `65532:65532` is refused (`660 root:docker`). `65532:<socket gid>` and
  `--group-add <socket gid>` both work: the link keeps a non-root user and needs the socket's group.
- **For step 2:** `up` must learn the socket's group and its path on the engine's host. A CLI on another machine cannot `stat` it.
  Not tried yet.

### The connection

The client dialled through `HTTPS_PROXY`, and the hub's side and the link's side both opened streams.

| In the way | Result |
|---|---|
| Caddy | passes, no configuration |
| nginx with `Upgrade` and `Connection` headers | passes |
| nginx with a plain `proxy_pass` | refused: 426 |
| Squid, CONNECT | passes |
| Squid, CONNECT with Basic auth | passes with the credentials in `HTTPS_PROXY`; 407 without |
| Squid, TLS inspection (default) | refused: 426 |
| Squid, TLS inspection with `http_upgrade_request_protocols websocket allow all` | passes |

The client skipped certificate checks here, so trusting an inspecting proxy's CA is untested. NTLM and Kerberos proxies are too.

| | yamux 0.1.2 | smux 1.5.58 |
|---|---|---|
| 200 streams opened by the hub, through Caddy | 597 ms | 372 ms |
| half-close | `Close` | `CloseWrite` |
| throughput through Caddy | 456 MB/s | 517 MB/s |
| round trip beside a bulk download, 30 ms and 50 Mbit (p50, p99) | 30 ms, 171 ms | 30 ms, 170 ms |
| a stalled stream | stops at its window, others unaffected | the same |
| silent path noticed, keep-alive 15 s | 17 s | 52 s |
| licence | MPL-2.0 | MIT |

**yamux is the choice.** The two perform alike. yamux noticed a dead path sooner with no tuning beyond the interval (smux's timeout
was set to 30 s and could be lowered), and half-close, which `egzo agent forward` needs, is its plain `Close`.

What the link must do, whichever multiplexer:

- **Keep-alive is required.** Without it nginx cut an idle connection at its default 60 s, and a silent path was not noticed in
  120 s. Use 15 s.
- **Explain a 426**: it means something between the link and the hub dropped the upgrade, usually a reverse proxy without the
  headers, or an inspecting proxy. The docs carry the nginx lines.
- **Take the proxy from the environment**, credentials included: `up` must hand `HTTPS_PROXY` to the link.
- Use HTTP/1.1 for the upgrade, no WebSocket compression, and no read limit on the WebSocket: yamux frames its own data.

### Left for the later spikes

Partial notes, not conclusions:

- **Rootless Podman**: `65532:65532` is refused and root in the container worked. Events and log following returned nothing on the
  test machine under `journald`, from the host too, and worked with the file backends.
- **gVisor**: a container under default `runsc` could not connect to the mounted socket. With `--host-uds=open` it did, with the
  socket's group as the primary gid (`--group-add` was not enough). Outbound from a user-defined network failed: open.

## Open questions

- Narrowing what the hub may do from the project file (`hub: { url: ..., allow: [attach, previews] }`): a later idea.
- What the hub shows of a project that is offline, after the hub restarted: its name and host, as what it pushed is in memory. Before that, its
  last known state, marked as such. Keeping that state in the database is open.

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
