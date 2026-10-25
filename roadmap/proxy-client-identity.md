# Roadmap: who a proxy connection is from

**Status.** Being built: the specs are the status. Extends [proxy-audit.md](proxy-audit.md) (the event schema) and
[browser-access.md](browser-access.md) (how Chromium gets through the proxy). No backward compatibility: a wrong-credentials event
no longer has an `agent`.

## The problem

- **Chromium's own requests are anonymous.** The browser of an agent sends everything through the proxy, but only page traffic
  answers the proxy's `407` with the agent's credentials. Chromium's background services stop at the challenge, so the audit
  trail shows `challenge` events with no agent, no address, nothing to say where they came from. A person reading it sees
  requests to `android.clients.google.com` from nobody.
- **`agent` does not always mean authenticated.** On wrong credentials the proxy logs the name the client claimed in `agent`.
  A filter on `agent` counts failed attempts to use an agent's name as that agent's own traffic; only `reason.code` tells them
  apart.
- **A failed ssh sign-in says nothing.** It is logged without the server asked for or an address. A client that connects and
  leaves (a port scan) is logged as an identity refusal. Two paths close a signed-in connection without any event.

## What was tested (2026-10-25, Docker, Chromium 155.0.8059.12, the `opencode` harness image)

Chromium launched by `playwright-core` with egzo's launch options, behind a proxy that answers like egzo's (`407` without
credentials, then `403`), and a net-log:

| Request | From | Credentials |
|---|---|---|
| `https://example.com/` | the page | after the `407` |
| `https://www.google.com/` | a preconnect | yes, from the page's |
| `https://android.clients.google.com/checkin` | GCM (push messaging), at 4 s and 26 s | never |
| `https://update.googleapis.com/service/update2/json` | an update check | never |

Playwright answers the `407` for the browser's pages only (it is told to, per page); Chromium's own services run in a network
context nobody answers for. Playwright already launches Chromium with `--disable-background-networking`,
`--disable-component-update`, `--disable-sync` and `OptimizationHints`, `NetworkTimeServiceQuerying` disabled: Chromium sends
these anyway.

## Decisions

**The browser's credentials are added by egzo, not Chromium.** `egzo agent browser` runs a forwarder on `127.0.0.1` (a random
port) for as long as the browser server runs, and points Chromium at it with no credentials. For each `CONNECT` the forwarder
dials the proxy of `HTTPS_PROXY`, sends the same `CONNECT` with the agent's `Proxy-Authorization` and the client's `User-Agent`,
and then passes the bytes both ways: the proxy's answer, the TLS, everything. Anything but `CONNECT` gets `405`, as from the
proxy. Every request Chromium makes then reaches the proxy authenticated, whichever part of Chromium made it, and is decided by
the agent's profile like any other (Chromium's background calls show up as `deny`, `not-in-profile`, from the agent). The
credentials never reach Playwright. Chromium is not customized: it is given a proxy, as before.

What it opens: any process in the agent's container can use the agent's egress through that port without knowing the
credentials. Every process there runs as the agent's user and has `HTTPS_PROXY` already, or can read it from the wrapper's
environment, so it is no access the container did not have. Nothing outside the container reaches the port (loopback), and a web
page cannot send a `CONNECT` (browsers never let a page choose that method), so a page that reaches the port, by DNS rebinding
or otherwise, only gets `405`.

**`agent`, `instance`, `profile` mean authenticated, always.** They are set once the proxy has checked the credentials (https)
or the signature (ssh), and never otherwise.

**`claimed_agent` is the name the client sent** in `Proxy-Authorization`, on every event of a connection that sent one, whatever
the outcome: on an authenticated one it equals `agent`; on wrong credentials it is the only name. A name that is not an
instance name is not logged, so a token typed into the user name never is. https only: an ssh client sends no name.

**`client` is the address the connection came from**, on every event, https and ssh. It is what the proxy saw, never mapped to an
agent: each agent has its own network, so a reader can tell which agent's network it came from, but a log field that says "agent"
must mean credentials.

**`user_agent` is what the client says it is**: the `User-Agent` of the `CONNECT` (`curl/8.11.1`, `Mozilla/5.0 … Chrome/155…`),
or an ssh client's identification string (`SSH-2.0-OpenSSH_9.6p1`). Declared by the client: a hint, never proof.

**ssh.** A failed sign-in logs the `server` that was asked for (the ssh user, when it is a server name), the `client` and the
`user_agent`. A client that leaves or times out before it signs in is `error`, `aborted`, as for https. The connections that were
closed without an event (the policy changed between the sign-in and the session) are logged as `deny`, `identity`.

**The hub.** The Network view shows `client` and `user_agent` in a connection's steps, and the claimed name of a connection that
is not authenticated, marked as claimed. The groups of denials do not change (no new columns: the hub's database has no
migrations before 1.0): a group the proxy could not attribute is shown as unauthenticated.

## Rejected

- **Filling `agent` from the client's address.** Each agent's network is its own, so the address does say which agent's network a
  connection came from; but an `agent` that is sometimes credentials and sometimes an address is the ambiguity this removes.
- **Turning Chromium's background services off with flags.** It ignores the ones it is given already, and a later version can
  add another service.
- **Authenticating by address** ([browser-access.md](browser-access.md), "Related ideas"): it would make the forwarder
  unnecessary, but an address is not a credential, and the forwarder needs nothing of the engine.
- **`claimed_agent` for ssh.** The nearest thing is the agent whose public key was offered. A public key is not a secret, and
  x/crypto asks about a key before any signature, so a failed sign-in cannot tell "offered" from "proved".
- **`client_banner`** for the field: in ssh, the banner is the server's message before sign-in (RFC 4252); what a client sends
  is its identification string (RFC 4253). `user_agent` is the name people know.

## Specs

- https: a challenge has the `client` and the `user_agent` and no agent; wrong credentials have a `claimed_agent` and no `agent`;
  an authenticated connection has all four, on every event.
- ssh: a sign-in has the `client` and the `user_agent`; a refused one has the `server` asked for; one that leaves before signing
  in is `aborted`.
- The browser: no challenge comes from an agent's browser, and what it sends reaches the proxy as the agent, with Chromium's
  `user_agent`.
- The audit's own rules: `client` is an address, `user_agent` and `claimed_agent` are clipped like every value an agent
  influences (`Event.Check`).
