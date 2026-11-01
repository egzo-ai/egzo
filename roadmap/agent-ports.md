# Roadmap: opening an agent's dev server from outside (port forward, hub previews)

**Status.** In progress. Built so far: the `__Host-` cookies, `egzo agent forward` and `egzo agent port-forward` (below), and the
viewer role and invites ([hub-link.md](hub-link.md)).

Need: an agent builds a web app and starts a demo server in its sandbox (`npm run dev`, `python3 -m http.server`, the hub
itself). It tells its user "the demo is live at <link>", and the user opens the link in their own browser, from their machine,
to try it. Today they cannot:

- an agent is on an internal network of its own, shared only with the control sidecar and the proxy; nothing is published on
  the host, and an internal network cannot publish a port;
- the hub never joins those networks, and it runs on another machine: it reaches a project only through the project's link
  ([hub-link.md](hub-link.md)), and the CLI only through the engine API (unix socket, tcp+TLS, ssh);
- dev servers listen on `127.0.0.1` inside the container by default (Vite does), so even a neighbour on the network could not
  reach them;
- the agent has no address to give: it does not know where, or whether, a hub serves its project.

## How: a byte stream through `engine exec`

The terminal already works this way: the hub runs `egzo agent attach` in the agent's container and bridges the exec stream to
the browser. A port is the same, without a terminal:

1. `POST /containers/{id}/exec` with `egzo agent forward 3000`, stdin and stdout attached, no TTY.
2. `POST /exec/{id}/start` hijacks the HTTP connection into a raw stream, over whatever carries the engine API.
3. In the container, `egzo agent forward` connects to `127.0.0.1:3000` and copies bytes between that socket and its stdin and
   stdout. End of stdin shuts down the socket's write side; the server closing ends the command.
4. The client copies bytes between its end of the stream and whoever wants the port. The client is the CLI
   (`egzo agent port-forward`), or the project's link, for the hub, which carries the bytes over its connection.

```
browser ──HTTPS──▶ hub (machine A) ◀──link connection, outbound from B──  link (machine B) ──exec──▶ agent container
                   reverse proxy        one stream per connection          engine socket            egzo agent forward 3000
                                                                                                    └─▶ 127.0.0.1:3000
```

- **One exec per TCP connection.** Without a TTY, Docker frames stdout and stderr in one stream; core already splits them
  (`internal/engine/demux.go`). Opening an exec costs tens of milliseconds locally, more over ssh: fine for a demo with
  keep-alive. One long-lived exec carrying many connections is a later optimisation, if ever needed.
- **Nothing changes in the sandbox.** No published port, no network joined, no change to the agent's egress: the stream is the
  operator reaching in through the engine, never the agent reaching out. `egzo agent forward` connects to loopback only, so it
  cannot be used to reach the control sidecar, the proxy or anything else on the agent's network.
- **The same over every engine**: a unix socket, tcp+TLS, ssh, Podman, gVisor. Whoever holds the engine connection decides where
  the port shows up; the engine host never listens on anything.

## Core (specs first)

### The agent exposes a port: the `expose` tool

The agent decides what is a preview: it knows it started a demo, on which port, and what it is. The `egzo` MCP server gets two
tools:

- **`expose(port, name?, protocol?)`**: records that the agent serves something on `port` of its own loopback, with an optional
  short name ("shop") and a protocol, `http` (the default) or `tcp`. For `http` it answers with **the preview's address** when a
  hub serves the project (`https://k7f3q9x2m4ad3v8w-p1a2b3c4d5e6.egzo-preview.example.net/`). Otherwise it answers with the command
  a person runs instead (`egzo agent port-forward coder-1 3000`): without a hub, and for `tcp`, which the hub does not serve yet.
  The agent passes the answer to its user. Exposing an exposed port again changes its name and protocol and keeps its id.
- **`unexpose(port)`**: the preview is gone; the address answers 404 until the port is exposed again.
- **Previews are plain HTTP**: HTTP/1.1, WebSockets and event streams. A server that speaks TLS itself, or anything else, is a
  `tcp` exposure. `udp` is not accepted: `engine exec` is a byte stream, and datagrams carried in one lose what UDP is for.
- **A restart of the agent ends its exposures**: the server is gone and the agent starts with no memory of them, so it starts with
  nothing exposed and exposes what it needs, which its prompt can ask of it. The sandbox tells control before the harness starts,
  so the first thing a harness does may be to expose a port. A restart of the sidecars ends nothing.
- Nothing is checked at `expose` time: a server may start after it, and stop and start again. A preview of a port where nothing
  listens says so to the viewer ("nothing listens on port 3000 in coder-1 now").
- The control sidecar keeps the exposures per agent name, next to the registration, on its volume. The agent's removal removes them. The status feed carries
  them, so the hub learns of a new preview from the link within a second.
- **The id** in the address is an HMAC of the agent's registration and the port under the project key (80 bits, 16 characters of
  base32): stable, so an agent that exposes a port again gets back the address it already gave out, and nothing has to be kept
  for that. Control keeps registrations by name and knows no instance id (`roadmap/instance-id.md`), so the exposures belong to the
  registration: every removal unregisters the agent, and a new instance of the name, with a registration of its own, never
  inherits an old one's address. Opaque, never the project's or the agent's name: links travel in chat and logs.
- **The address** is made from a template control learns from the hub (below). Without one, the answer is the `port-forward`
  command. Control never needs to reach the hub.

### Where the address comes from

The domain belongs to the hub's deployment, not to the project. A project names its hub ([hub-link.md](hub-link.md)), and the
hub gives the project's link an **address template** at each connection, in the verdict that approves it; the link hands it to
control, which keeps it on its volume. The template holds the scheme, the port if any, the domain and the hub's id of the project:
`https://%s-p1a2b3c4d5e6.egzo-preview.example.net/`. So the project is in the host name: the hub knows which project a host belongs
to without having heard from it, an offline project can say so, and no project can claim an address of another.

A verdict that does not approve the project clears the template, and so does `up` when `hub:` is gone. The project file never
names the domain, so a project moved to another hub gets that hub's. A template does not contain addresses: control keeps the ids,
so a hub that moves to another domain moves every preview with it.

### For people without a hub

- **`egzo agent forward <port>`**, the in-container role above, like `egzo agent attach`. Loopback only (`127.0.0.1`, then
  `::1`). A port where nothing listens is an exit status and one line on stderr that the client shows.
- **`egzo agent port-forward <agent> [[address:]local:]port`** on the host, like `kubectl port-forward`: listens on
  `127.0.0.1:<local>` (default: the same number) of the machine where it runs, and opens one exec per connection it accepts. With
  `DOCKER_HOST=ssh://box` on a laptop, the port is on the laptop. Ctrl-C ends it; it ends by itself when the agent stops. It
  forwards any port, exposed or not: whoever holds the engine holds the agent anyway. `port-forward` first asks the image's egzo
  whether it can forward: what a failed exec says goes down the stream, so a client would read it as the server's words.
- **A client gone and a server that ignores it.** The end of stdin shuts down the socket's write side, because the client may only
  have ended its input. A stream cannot tell that from a client that left, so `egzo agent forward` gives the server a minute of
  silence after the end of stdin, then exits.
- **`egzo agent ls`** / **`egzo ps --json`** show the exposed ports.

### Specs

- `expose` without an address template answers the `port-forward` command; with one, the address; the same address again after
  `expose` again; nothing exposed after a restart of the agent, and the same address when the port is exposed again; nothing changed
  by a restart of the sidecar; another address after the agent is removed and a new one has its name.
- `expose` with `tcp` answers the `port-forward` command, with a template too; `udp` and anything else is refused.
- `unexpose`; the exposures in the status log line and in `ps --json`; an agent cannot expose for another.
- `egzo agent forward --announce` says it connected before any byte, and says nothing when nothing listens; `--close-on-eof` ends
  at the end of stdin, and without it a half-closed client still gets its answer.
- A server in an agent reached through `port-forward`: bytes both ways, a WebSocket upgrade, a large body, the server closing
  first, the client closing first; a server bound to `127.0.0.1` only; nothing listening; a stopped agent.
- On every reference platform, gVisor included.

## The hub: previews

### Shape: one binary, a second listener

A preview connection is a stream the hub opens over the project's link ([hub-link.md](hub-link.md)); the link runs
`egzo agent forward` on its engine. The links are connected to the hub's process, so the preview proxy lives there too: a second
listener (`EGZO_PREVIEW_LISTEN`) of the same binary, in the same container.

The workload is not new: a terminal is already a long-lived stream bridged to a WebSocket, and a preview is the same behind
`httputil.ReverseProxy`, with more connections. What differs is volume and an untrusted server on the other side, which limits
handle (below).

The preview code is its own package (`hub/internal/preview`) that depends on two things only: authorizing a request (cookie to
user and preview) and `DialPort(ctx, agent, port) (net.Conn, error)` on the `Engine` seam (faked in tests with `net.Pipe`). A
context covers the dial and nothing after it, as with `net.Dialer`: the connection ends with itself or with the link. A preview
service of its own would have to reach the links through the hub anyway, so every byte would still cross the hub's process; it
is not worth a second service.

The verb is `forward`, with an agent and a port, and it knows no protocol: the stream is bytes, with `CloseWrite` for a half
close, so a client for `tcp` exposures can use it as it is. The link refuses a port the agent did not expose, and picks the
command from the exposure: `egzo agent forward --announce` always, so that a port where nothing listens is an answer and not a
stream that ends, and `--close-on-eof` for `http`, which never half closes. A server that keeps talking to a client that left
would otherwise hold its exec for up to `drainLimit`, and every reload of a page leaves one such connection.

### Addresses: one origin per preview, on a domain of its own

A preview is code the agent wrote, served to people's browsers. On the hub's origin it would run with the viewer's hub session.
So every preview has its own origin, `<id>-<project>.<EGZO_PREVIEW_DOMAIN>`, and the hub serves it on the preview listener only.
The host names the project; the hub maps the id to an agent and a port from the `http` exposures it reads in the status feed. An
unknown host is a 404, and a project that is offline says so. `EGZO_PREVIEW_DOMAIN` needs `EGZO_PUBLIC_URL`, where the redirects
below go.

- **A separate registrable domain** for previews (`*.egzo-preview.example.net`, not `*.preview.example.com` next to the hub at
  `hub.example.com`). A sibling subdomain is the same *site* as the hub: `SameSite` cookies do not separate them, and a page
  there can set a cookie on `.example.com` that the hub then receives (cookie tossing). The fix below closes that for the
  session; a separate domain removes the whole class. The docs recommend it; a sibling subdomain works, with the fix.
- Routing on the host name keeps apps working unchanged: a path prefix (`/preview/abc/`) breaks every app that uses absolute
  paths (`/assets/...`), and distinct ports on one host share cookies with each other and with the hub (cookies ignore ports).

### Required first: the hub's cookies get the `__Host-` prefix

The session cookie is `egzo_session`, host-only, `SameSite=Lax`, `Secure` over https (`hub/internal/web/server.go`). A page on a
sibling subdomain can set `egzo_session` for the parent domain, and the hub reads it: a preview could plant a session of its
choosing (session fixation). The hub's Origin check still stops cross-site requests, but nothing stops the planted cookie.

- Over https, the cookies are `__Host-egzo_session` and `__Host-egzo_login`. A browser accepts a `__Host-` cookie only when it is
  `Secure`, has `Path=/` and no `Domain`, so no other host can set or overwrite it.
- Over plain http (a trial on `localhost`, a LAN) the prefix is impossible; the names stay as they are, and previews are refused
  unless the preview domain is not a subdomain of the hub's site. The hub checks this at start-up and says why.
- Before 1.0 there is no migration: everyone signs in again once.
- A unit test: a request carrying a planted unprefixed `egzo_session` next to the real one is the real one's user, or nobody.

### Signing in: always

**A preview is never public.** Every request needs a person signed in to the hub who may view it; there is no anonymous
preview, no setting to make one, and no share link that works without signing in.

The link the agent gives out is a plain address, and signing in happens by redirects, because the hub's cookie never reaches a
preview host, and must not:

1. A request to `https://<host>/...` without a preview cookie is redirected to the hub: `<EGZO_PUBLIC_URL>/preview/<host>?to=<path>`.
2. The hub signs the person in if needed (password or SSO, as for any page), checks that they may view the preview, and
   redirects back to `https://<host>/_egzo/enter?ticket=...`, with a one-time ticket (seconds, bound to the user and the
   preview).
3. The preview listener checks the ticket, sets its own host-only cookie for that preview, and redirects to the path first asked
   for.

- Every request is checked again: the user is enabled, still may view it, and the preview still is exposed by that instance. A
  disabled user or a removed role loses previews at the next request. What makes no more requests (a WebSocket, an event stream)
  ends as a terminal does: with the session, with the role, and with `unexpose`.
- The preview cookie belongs to the hub session it came from, so it ends with it.
- `/_egzo/` is the hub's on every preview host and never reaches the app. The preview cookie is stripped before the request goes
  on, and an answer cannot set it; the rest (the app's own cookies, `Authorization`) is the app's.
- An upgrade or an unsafe method whose `Origin` is not the preview's own host is refused.
- The audit log records who opened which preview (once per ticket, not per request).

**Who may view a preview:** every role on the project, for every agent's previews: an agent exposes a port on purpose. The
**viewer** role gets previews and nothing else; a person from outside gets it, and an account, through an invite
([hub-link.md](hub-link.md), "Roles and invites"). An operator with no role sees that a preview exists and grants themselves a
role to open it, as for a terminal.

### The proxy

- `httputil.ReverseProxy` with a transport that dials through `DialPort`. WebSocket upgrades pass (Vite's hot reload).
- `Host` is rewritten to `localhost:<port>`: dev servers refuse unknown host names (Vite's `allowedHosts`). `X-Forwarded-Host`
  and `X-Forwarded-Proto` say the real ones, and `Origin` stays what the browser sent: an app uses them, or is told its address.
  What an app answers is not rewritten: a proxy that fixes the simple cases leaves the rest half working.
- Limits, because the server on the other side is untrusted: open streams per preview and per hub, time to dial, response
  header size and time, idle time. A request over the stream limit waits a moment, because a dev server's page opens hundreds of files at once;
  after that it is a 503 that says which limit, never a hub that slows down.
- Not built yet: a limit per project, across its previews, and idle connections that do not count. An idle connection keeps its
  slot for up to 30 seconds, and a request over the limit cannot close one, so previews viewed a moment ago can fill the hub's limit.
- The listener has limits of its own: the hub's 30 seconds to read a request would cut an upload short.
- Redeploying the hub drops open previews, as it drops terminals today. A reload brings them back.

### The web app

A **Previews** panel for the project's users, admins and owners (a viewer has links, not pages), on the agent page,
and on the project page for every agent: each exposed port with its name and address,
**Open**, and **Copy link**. No preview domain configured, or a `tcp` exposure: the panel shows the `egzo agent port-forward`
command, and says what to set where that helps.

## Certificates for the preview domain: a wildcard, with DNS-01

The hub does not terminate TLS (`docs/hub.md`); the reverse proxy in front does, with **one wildcard certificate**
(`*.egzo-preview.example.net`), which the ACME DNS-01 challenge issues. It is more setup once than a per-host certificate, and
everything after is simpler:

- **Nothing has to be public.** The CA checks a DNS record, never the server, so a hub on a LAN or a tailnet gets a real
  certificate.
- **No rate limit to plan for.** One certificate, renewed every two months, whatever the number of previews.
- **Preview addresses stay private.** Every certificate's names are published in the Certificate Transparency logs; a
  wildcard publishes only the domain.
- **No delay** at the first open of a new preview.

The docs give recipes for Caddy and Traefik with a DNS provider's API, and the lighter way when the proxy should not hold
credentials for the zone: **`acme-dns`**, a CNAME from `_acme-challenge.egzo-preview.example.net` to a small DNS server that
only answers challenges (Caddy and lego support it). A team with its own CA (a private network) uses it instead, and every
viewer's browser trusts the root once (Caddy's `tls internal` does this for a trial). Whatever is in front has to pass WebSockets
and keep or forward `Host`, with `EGZO_TRUSTED_PROXIES` set. egzo ships no proxy: `make hub-stack` uses Caddy as a test, and
an example compose file may follow.

## Order of work

Built: the `__Host-` cookies, `egzo agent forward` and `egzo agent port-forward`, the link, the viewer role and invites.

1. Core, with specs: `forward --announce` and `--close-on-eof`, the `expose` and `unexpose` tools, the exposures in the status feed
   and `ps`, the address template from the hub through the link. Docs: `docs/messaging.md` (the tools),
   `docs/managing-projects.md`, `docs/security.md`.
2. The proxy: the `forward` verb and `DialPort` on the seam (the link and the fake), `hub/internal/preview` (hosts, proxy, limits),
   `EGZO_PREVIEW_DOMAIN` and `EGZO_PREVIEW_LISTEN`. Unit tests with the fake engine, and a test on a throttled link of what a large
   preview does to a terminal on the same connection.
3. Signing in: the redirects, tickets, the preview cookie, the role on each request, ending what is open, the audit log.
4. The Previews panel, a Playwright test, and `make hub-stack` with a linked project exposing `python3 -m http.server`, through
   Caddy (its internal CA). Docs: `docs/hub.md` (previews, what a proxy in front must do, the certificates, the separate domain),
   `docs/security.md` (agent code served to people's browsers, and why it gets its own origin).

## Open questions

- How a person reaches a `tcp` exposure through the hub: a client on their machine, which needs the CLI to sign in to the hub,
  and which roles may. The stream it needs is the `forward` verb, as it is.

A person may not open a port the agent did not expose, from the hub: the link refuses it. The CLI's `port-forward` still can.

## Alternatives considered

- **A certificate per preview host, on demand** (Caddy's `on_demand_tls`, with the hub answering its `ask` endpoint so only its
  hosts get one): needs only a wildcard DNS record, no DNS API. Rejected: the CA must reach the preview listener from the
  internet (HTTP-01, TLS-ALPN-01), which a hub on a private network cannot offer; Let's Encrypt issues at most 50 certificates
  per registered domain per 7 days, one per new previewed agent; every preview's host name lands in the public CT logs; the
  first open waits for issuance; Traefik and nginx cannot do it.
- **The hub terminating TLS itself** (Go's `autocert` on the preview listener): contradicts "the hub does not terminate TLS",
  with the same limits as on demand.
- **Ports found by the hub** (`/proc/net/tcp` through an exec) instead of declared: needs no tool, but the agent still has no
  address to give, and anything listening (a debugger, a database) would be offered as a preview.
- **Publishing a port** (`docker run -p`): only at creation, so the agent is recreated; an internal network cannot publish; a
  remote hub would need a route to the engine host besides the engine API; the server must listen on `0.0.0.0`.
- **The hub joining the agent's network**: breaks one network per agent, a remote hub cannot, and loopback servers stay
  unreachable.
- **The proxy sidecar as an ingress**: it is on every agent's network, but has no published port either, and loopback servers
  stay unreachable.
- **A path prefix on the hub's own origin**: breaks apps with absolute paths, and runs agent code with the hub's session.
- **A separate preview service**: it would reach the links through the hub anyway, so every byte would still cross the hub.
