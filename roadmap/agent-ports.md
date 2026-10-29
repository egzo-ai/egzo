# Roadmap: opening an agent's dev server from outside (port forward, hub previews)

**Status.** Draft, not decided. Nothing is built. The session cookie fix (below) is needed before previews and is worth doing on
its own.

Need: an agent builds a web app and starts a demo server in its sandbox (`npm run dev`, `python3 -m http.server`, the hub
itself). A person wants to open it in their own browser, from their machine, to try it. Today they cannot:

- an agent is on an internal network of its own, shared only with the control sidecar and the proxy; nothing is published on
  the host, and an internal network cannot publish a port;
- the hub never joins those networks, and it may run on another machine: it reaches an engine only through the engine API
  (unix socket, tcp+TLS, ssh);
- dev servers listen on `127.0.0.1` inside the container by default (Vite does), so even a neighbour on the network could not
  reach them.

## How: a byte stream through `engine exec`

The terminal already works this way: the hub runs `egzo agent attach` in the agent's container and bridges the exec stream to
the browser. A port is the same, without a terminal:

1. `POST /containers/{id}/exec` with `egzo agent forward 3000`, stdin and stdout attached, no TTY.
2. `POST /exec/{id}/start` hijacks the HTTP connection into a raw stream, over whatever carries the engine API.
3. In the container, `egzo agent forward` connects to `127.0.0.1:3000` and copies bytes between that socket and its stdin and
   stdout. End of stdin shuts down the socket's write side; the server closing ends the command.
4. The client copies bytes between its end of the stream and whoever wants the port.

```
browser ──HTTP──▶ hub (machine A) ──engine API over ssh/TLS──▶ dockerd (machine B) ──exec──▶ agent container
                  reverse proxy                                 one exec per connection      egzo agent forward 3000
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

- **`egzo agent forward <port>`**, an in-container role of the same binary, like `egzo agent attach`. Loopback only (`127.0.0.1`,
  then `::1`). A port where nothing listens is an exit status and one line on stderr that the client shows ("nothing listens on
  port 3000 in coder-1").
- **`egzo agent port-forward <agent> [[address:]local:]port`** on the host, like `kubectl port-forward`: listens on
  `127.0.0.1:<local>` (default: the same number) of the machine where it runs, and opens one exec per connection it accepts. With
  `DOCKER_HOST=ssh://box` on a laptop, the port is on the laptop. Ctrl-C ends it; it ends by itself when the agent stops.
- **`egzo agent ports <agent>`** (`--json`): the TCP ports listening in the container, read from `/proc/net/tcp*` through an exec,
  with the process when it can be read. The agent does nothing to be found. An MCP tool for the agent to name its demo ("the
  shop, on 5173") is a later addition, not a requirement.
- Specs: a server in an agent reached through `port-forward` (bytes both ways, a WebSocket upgrade, a large body, the server
  closing first, the client closing first), a server bound to `127.0.0.1` only, nothing listening, a stopped agent, `ports`
  listing what listens. On every reference platform, gVisor included.

## The hub: previews

### Shape: one binary, a second listener, a boundary for later

The preview proxy needs the engine credentials (to open the exec streams) and the users and roles (to decide who may open what).
Splitting it into another service does not take those away:

| | Shape | Engine credentials | For the user | What it gives |
|---|---|---|---|---|
| A | one process, a second listener (`EGZO_PREVIEW_LISTEN`) | the hub | one more port | simplest |
| B | the same image, a second compose service (`egzo-hub preview`) sharing the data volume and `EGZO_SECRET_KEY` | both | one more service in the shipped compose file | restarts, upgrades, limits and crashes apart; previews survive a hub redeploy |
| C | a preview service without engine credentials, asking the hub for a stream to (agent, port, user); the hub splices the exec | the hub | like B | only the hub reaches engines, but every byte still goes through it |
| D | a separate binary and image with its own engine access and auth | both | a second image, version skew, a second set of settings | nothing over B |

The workload is not new: a terminal is already a long-lived exec stream bridged to a WebSocket, and a preview is the same behind
`httputil.ReverseProxy`, with more connections. What differs is volume and an untrusted server on the other side, which limits
handle (below).

**Proposed: A, built with the boundary of B and C.** The preview code is its own package (`hub/internal/preview`) that depends
on two things only: authorizing a request (cookie to user, agent and port) and `DialPort(engine, project, agent, port)
(net.Conn, error)` on the `Engine` seam (faked in tests with `net.Pipe`). Moving to B, or to C if isolating the credentials ever
matters, is then a change to `main.go` and the compose file.

### Addresses: one origin per preview, on a domain of its own

A preview is code the agent wrote, served to people's browsers. On the hub's origin it would run with the viewer's hub session.
So every preview has its own origin, `<id>.<EGZO_PREVIEW_DOMAIN>`, and the hub serves it on the preview listener only.

- **The id is opaque and stable**: a random id the hub keeps per (engine, project, instance id, port), never the agent's or the
  project's name. Stable, so a restart of the agent or the hub keeps the address (and the certificate, below); keyed by the
  instance id, because a name can come back (`roadmap/instance-id.md`) and a new instance must not inherit the old one's origin,
  with its cookies and storage. Opaque, because a certificate's names are public (Certificate Transparency logs): a host name
  must not tell the world the project and agent names.
- **Recommend a separate registrable domain** for previews (`*.egzo-preview.example.net`, not `*.preview.example.com` next to
  the hub at `hub.example.com`). A sibling subdomain is the same *site* as the hub: `SameSite` cookies do not separate them, and a
  page there can set a cookie on `.example.com` that the hub then receives (cookie tossing). The fix below closes that for the
  session, but a separate domain removes the whole class, and gives previews their own certificate rate-limit budget (below).
  The docs say so; a sibling subdomain works, with the fix.
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

### Signing in on the preview origin

The hub's cookie never reaches a preview host, and must not. **Open** on the agent page asks the hub for a one-time ticket (short
expiry, bound to the user, the preview and the browser's session) and goes to `https://<id>.<domain>/_egzo/enter?ticket=...`. The
preview listener checks it, sets its own host-only cookie for that preview and redirects to the app.

- Every request is checked: the user is enabled and may operate the agent (the same rule as the terminal), and the agent still
  is that instance. A disabled user or a removed role loses previews at the next request.
- `/_egzo/` is the hub's on every preview host and never reaches the app. The preview cookie is stripped before the request goes
  on; the rest (the app's own cookies, `Authorization`) is the app's.
- Sharing a preview with someone who has no role on the project is not in this item.

### The proxy

- `httputil.ReverseProxy` with a transport that dials through `DialPort`. WebSocket upgrades pass (Vite's hot reload).
- `Host` is rewritten to `localhost:<port>`: dev servers refuse unknown host names (Vite's `allowedHosts`). `X-Forwarded-Host`
  and `X-Forwarded-Proto` say the real ones.
- Limits, because the server on the other side is untrusted: open streams per preview and per hub, response header size and
  time, idle time. Going over is a 503 that says which limit, never a hub that slows down.
- Redeploying the hub drops open previews, as it drops terminals today. A reload brings them back.
- The audit log records who opened which preview (once per sign-in on the preview, not per request).

### The web app

A **Ports** panel on the agent page: what listens (`egzo agent ports`), each with **Open**, refreshed while the panel is shown.
No preview domain configured: the panel says what to set, and shows the `egzo agent port-forward` command for the CLI.

## Certificates for the preview domain

The hub does not terminate TLS (`docs/hub.md`); the reverse proxy in front does. A wildcard certificate needs the ACME DNS-01
challenge, which needs the reverse proxy to hold credentials for the DNS provider's API: a pain to set up, and not possible with
every provider. Options, to document as recipes:

1. **One certificate per preview host, on demand** (Caddy's `on_demand_tls`). Caddy asks for a certificate at the first TLS
   handshake for a host it has none for, with HTTP-01 or TLS-ALPN-01. Needs only a wildcard DNS *record* pointing at the reverse
   proxy, which any DNS provider can make by hand.
   - The hub answers Caddy's `ask` endpoint (`GET /_egzo/preview/ask?domain=<host>` on the preview listener, 200 or 404): only a
     host the hub made gets a certificate, so nobody can make the proxy request certificates for random names. It must answer in
     milliseconds, from memory.
   - **It needs the preview listener reachable from the internet** on 80 or 443: the CA has to reach it to validate. That
     contradicts "the hub is meant for a private network". Only the preview hosts need to be public, not the hub, but they are
     public: the ticket and the per-request role check are then what protect them.
   - **Rate limits.** Let's Encrypt issues at most 50 certificates per registered domain per 7 days (refilling one every 202
     minutes), and 5 for the same exact name per week. A certificate per (instance, port) means one per new agent that is
     previewed: a busy team can hit 50. Hence stable ids (an agent's restart costs nothing) and a registrable domain of its own
     (the budget is not shared with the company's other sites). Caddy falls back to ZeroSSL by default, which helps.
   - The first open of a new preview waits for issuance (seconds).
   - Traefik and nginx have no on-demand issuance; this is a Caddy recipe.
2. **A wildcard certificate with DNS-01**: one certificate, nothing public, no rate limit concern, no host names in CT logs. The
   setup cost the user pays once. `acme-dns` (a CNAME for `_acme-challenge` delegated to a small DNS server) avoids giving the
   proxy credentials for the main zone; Caddy and lego support it.
3. **A private CA on a private network**: Caddy's `tls internal` (on demand works with it too) or the team's own CA. No public
   exposure, no rate limits; every viewer's browser must trust the root once.
4. **Plain http on a private network**: works, but some browser APIs need a secure context (clipboard, service workers,
   `crypto.subtle`), and the hub's cookies lose the `__Host-` prefix, so the preview domain must not be a subdomain of the hub's
   site.

Proposed: the docs lead with 1 for a hub that faces the internet and with 2 or 3 for a private one, and `hub/compose.yaml` gets an
optional Caddy service with the on-demand recipe, commented out. The hub terminating TLS itself (Go's `autocert` on the preview
listener) would remove the reverse proxy for previews, but contradicts "the hub does not terminate TLS"; not now.

## Order of work

1. The `__Host-` cookies in the hub, with the unit test. Independent of the rest.
2. Core: `egzo agent forward`, `egzo agent port-forward`, `egzo agent ports`, with specs and docs (`docs/managing-projects.md`,
   `docs/security.md`).
3. Hub: `DialPort` on the seam (Docker and fake), `hub/internal/preview` (hosts, tickets, proxy, limits, `ask`), the setting
   `EGZO_PREVIEW_DOMAIN` and `EGZO_PREVIEW_LISTEN`, the Ports panel. Unit tests with the fake engine, a Playwright test,
   `make hub-integration` with a real agent running `python3 -m http.server`, and the on-demand recipe in `make hub-stack`
   (Caddy with its internal CA).
4. Docs: `docs/hub.md` (previews, the certificate recipes, the separate domain), `docs/security.md` (agent code served to
   people's browsers, and why it gets its own origin).

## Open questions

- Who may open a preview: whoever may operate the agent (proposed), or anyone with a role on the project?
- Ports found by the hub only, or also declared by the agent through an MCP tool?
- Previews for people without a role (a link to share with a client): not now; it needs expiring links and its own audit.

## Alternatives considered

- **Publishing a port** (`docker run -p`): only at creation, so the agent is recreated; an internal network cannot publish; a
  remote hub would need a route to the engine host besides the engine API; the server must listen on `0.0.0.0`.
- **The hub joining the agent's network**: breaks one network per agent, a remote hub cannot, and loopback servers stay
  unreachable.
- **The proxy sidecar as an ingress**: it is on every agent's network, but has no published port either, and loopback servers
  stay unreachable.
- **A path prefix on the hub's own origin**: breaks apps with absolute paths, and runs agent code with the hub's session.
- **A separate preview binary and image** (D above): costs a second image and its settings, and gives nothing over B.
