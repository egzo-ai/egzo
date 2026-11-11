# Reference: how a project links to a hub

How a project and a hub talk: the model, registering, the link sidecar, the connection, versions, and the roles and invites the
hub keeps. For running a hub, see [The hub](../hub.md) and [Hub access](../hub-access.md); for the API the web app uses,
[the hub API](../hub-api.md). Moved from the design page `roadmap/hub-link.md` when the roadmap moved to GitHub issues.

## Why

The hub used to connect to engines (a unix socket, tcp+TLS, ssh) and pull everything through the engine API. That model cracked:

- **The hub holds root-equivalent access to every engine**: a socket, a TLS client certificate, an ssh key. A compromised hub owns
  every host behind it.
- **The hub must reach every engine.** A laptop, a box behind NAT or a company network cannot be added.
- **The hub sees everything on an engine**: every project with egzo labels, whether its owner wants it shown or not.
- **The hub pushes its settings into projects**: the preview domain of [agent-ports.md](../hub.md#previews) had to be written into
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
  preview stream, send a message for one of its users and answer one. The link runs them on its own engine with the core packages (`stack.Spawn`,
  `RemoveInstances`, `Observe`, `ExecStream`). Up the connection the link pushes the project as it changes, the agents' status
  feed, the proxy's audit trail and the control sidecar's events (see [Messages](#messages)).
- **The link exposes egzo's verbs and nothing else.** The hub cannot run a container, mount a path or exec a command of its
  choosing: it spawns from the project's own templates and attaches to the agents' sessions. The project file and the CLI stay
  the only way to change a project.
- **The hub holds no engine credentials.** It needs neither the Docker socket nor an ssh key, and its compose file mounts nothing
  of the host.

| | Before (the hub pulls) | After (projects link) |
|---|---|---|
| A compromised hub | root on every engine host | egzo's verbs on the linked projects: spawn from their templates, attach to their agents, talk to them as any of its users |
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
  [project secret key](../project-file.md#the-secret-key); the hub keeps the public key with the registration.
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
  created it. Rootless Podman keeps that to the user's account.
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

- **A WebSocket carrying yamux** ([chosen in the spike](#what-was-tested)): it passes reverse proxies and company proxies that an
  HTTP/2 stream to a backend does not. One stream per verb, per terminal and per preview connection; the feeds have one each.
- **A versioned protocol.** Link and hub are separate releases on separate machines: see [Versions](#versions).
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

## Messages

The hub relays what its users say to agents, and what agents say to them, for the chat bridges (#145). It never speaks as the operator.

- **`send`** is a message from a hub user to an agent: `Agent`, `Actor` (`user:<id>`, nothing else), `Text`, `Interrupt`, and a `Key`,
  the chat platform's id for the message. The key is required: the same key from the same user is the same message, so a retry after a
  dropped connection sends nothing twice. The same key with another text or agent is an error. It answers the message's id. While the project is offline there is no link to ask, and nothing
  queues: the hub drops the message.
- **`resolve`** is a hub user answering a question, or closing a request, addressed to them: `Message`, `Actor`, `Text`, `Outcome`, and
  `Key`, the chat platform's id for the answer. A user answers only what is addressed to that user; the operator, at the CLI, answers
  anything. It answers `{ID, Already}`: the resolution's id, the same for a retry with the same key. An answer is final: a user who
  answers again what they resolved changes nothing, and gets their first resolution with `Already` true. The same key with another answer
  is an error, and so is answering what someone else resolved.
- **`spawn`** takes, besides `Template` and `Name`, `Actor`, who the agent is told started it, `StarterName`, their name, and `Task`, what
  it starts on: typed once as its first prompt, or with `Unattended` sent as a request from the actor, and the agent told that nobody
  reads its terminal. `Unattended` needs a `Task` and a `user:` actor. The task's text goes to the project only.
- **`people`** is the hub saying what it knows now of the users who started the project's agents: `People`, by `user:<id>`, each with a
  `Name`, and `Reachable`: whether a bridge can reach them now. It is the whole list each time. An agent whose starter became reachable, or
  unreachable, gets a notice from `egzo`; the first word on a person, and the same again, are no news. The hub sends it when the project's
  agents change, and when one of those users links or unlinks a chat account, is disabled or enabled, or has their role changed.
- **`undelivered`** is the hub saying that an agent's message to one of its users did not reach them: `Message`, and `Text`, why, in words
  the hub chose for the agent. The agent gets a notice from `egzo` that names the message, its start and the reason, and says how to send
  it again. A question or request is closed as `failed`; a result stays as the agent closed the request; an update gets no notice. Told
  twice, it is one notice. Only an agent's message to a `user:` can be reported.
- **The `events` feed** follows the control sidecar's event stream. It begins after `FeedStart.After`, the sequence number of the last
  event the hub has, so a link that was away resumes where the hub is and loses nothing. A hub that never read the feed says -1: only the
  events to come, and the first is a `start` event whose `Seq` says where that is, which the hub keeps like any other, so a feed opened
  again before anything else came misses nothing either. A hub that cannot tell where it is ends the feed rather than guess. An event of a
  log that began again (the control volume made anew) has a smaller number than the hub's: the link sends the whole new log.
- **What the feed carries.** Every event worth showing (no `state`). A message says who it went from and to, its kind and what it is
  about, and its text and choices **only when a user is one end of it**: what agents say to each other or to the operator stays in the project.
  The `events` verb, which the web UI's timelines ask, carries no message text at all.

## Versions

Link and hub are released apart and run on different machines, so what each speaks has to be said out loud. Before 1.0 there is one version at a time: no ranges,
no compatibility shims.

**Built.**

- The handshake carries `hublink.Version`. A hub refuses a link it does not speak and says which side to upgrade: "this hub speaks protocol 2 and the link
  speaks 1: upgrade the project (a newer egzo, then `egzo project up`)". The link stops dialling and says so.
- A status line carries its format, `v`. A line of another format is an error that says to update the hub.
- A project that writes no status is `unavailable` after 90 seconds, and its running agents show as running, never "starting". That covers the symptom that started
  this: the hub once showed an agent as **Starting** for ever, because an older control sidecar answered `404` and the error was swallowed.

The hub no longer talks to a control sidecar. The link does, and `up` makes both from one image, so they are one release. That removes the need for a
control API version the hub can compare, and for a typed result from every control call: what the hub sees is the protocol above.

**Still to decide for 1.0**: the rule for changing the protocol, and the rest of telling an old release from a broken one: [#130](https://github.com/egzo-ai/egzo/issues/130).

## Roles and invites

Built, as [hub-access.md](../hub-access.md) describes. What the text below left open:

- An invite lives 7 days. A link code that finds its maker's quota used up is refused and stays good, like one of a key already registered.
- An owner's unlink forgets the project outright, so its key is unknown and the link hears it is not registered. Revoke, which keeps the record and refuses
  the key, stays the operators'.
- An operator with no role sees the project page and its Members, and nothing else: not the timeline, the tasks or the spec either. They grant themselves a
  role, and can make somebody the owner only of a project with none: for anything else they are an owner first.
- A viewer's role is not shown to them: the project is not found, as for a stranger.
- What a quota counts is who connected a project, not who owns it now.
- A local account signs in with `EGZO_LOCAL_LOGIN` on, and there is no password reset.

**Signing in is not enough: an invite is required.** With a company-wide provider, an account for everyone who signs in would be everyone in
the company. So a person needs a sign-in **and** a valid invite, and an account without one is refused.

- An invite is a link with a one-time code: single use, expiring, revocable. It carries what it grants: a role on a project (sent
  by the project's owner), or the flags below (sent by an operator).
- It is redeemed by whoever signs in with it, by password or single sign-on. Nothing is matched by email
  ([hub-access.md](../hub-access.md)).
- A person from outside (the viewer of a preview) needs an account: the invite is also how they get one, as a local account made from it.

Roles are assigned on projects:

- **owner**: can destroy the project, which on the hub is **unlink only**: the hub forgets it and refuses its link. The project
  keeps running on its machine until someone runs `egzo project down` there; the hub never does. Can grant (invite). A project
  keeps at least one owner; an owner cannot remove or lower themselves.
- **admin**: access all agents (operate, attach, logs, network, the audit log), the CLI's included.
- **user**: can spawn their own agents, operate and attach to them, and view their logs and network.
- **viewer**: the web proxy only: a preview of any agent of the project, opened with its link. The link is not guessable but
  shareable ([agent-ports.md](../hub.md#previews)): the agent or a member hands it over. A viewer sees nothing else of the project,
  not even a list of its previews.

Flags on users:

- **operator**: oversees the hub and all its resources: every project, its owners, its agents and their state, the links, the
  users. Manages users, invites, quotas, and revokes any project. To go further (a terminal, logs, a project's network), an
  operator grants themselves a role on the project, audited and shown on its Members tab like any grant. The starter account
  `admin` is an operator.
- **project quota**: how many projects the person may connect to the hub. 0 cannot create. Operators bypass it. It can be
  over-subscribed: lowering a quota under what a person already has removes nothing, it stops new ones.

A person disabled by an operator loses everything at once. Their projects stay linked, so a project can be left with
no valid owner: the operators see it, and grant a new owner or revoke it.

## What was tested

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
