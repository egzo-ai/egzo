# Roadmap: monitoring agents' internet access

**Status.** The audit event schema is built (code, specs, unit tests). The hub's Network view is in progress. Needs
[instance-id.md](instance-id.md), which is built. Split out of [hub.md](hub.md), where the proxy audit log was listed on the
project page and never built. No backward compatibility: the old `action` field is gone.

## The problem

The most frustrating part of running agents in a sandbox is finding out why one cannot reach something. The proxy knows: it
answers `403 egzo: registry.npmjs.org is not allowed by this agent's egress profile`. But tools turn that into
`ERR_TUNNEL_CONNECTION_FAILED`, `ECONNRESET` or a timeout, the agent reports "a network error", and the person reads JSON lines
from `egzo proxy log`. The hub does not show the audit trail at all.

The audit trail itself was hard to build on:

- **One field, four jobs.** `action` held the protocol (the `ssh-` prefix), the step (`open`, `close`, `request`), the decision
  (`allow`, `deny`, `error`) and how TLS was handled (`inject`, `inspect`, `intercept`): fifteen values. `deny` was logged from
  eleven places, `ssh-deny` from six.
- **Free-text reasons.** Some were built from parts (`"request refused: " + type`, `err.Error()`). A tool that wants to explain a
  denial, or say how to fix it, would match on strings that change with the wording.
- **No connection id.** `allow`, `request` and `close` could not be tied together; two tunnels to one host were ambiguous.
  Terminated hosts (`inject`, `inspect`) had no `close` at all.
- **No profile.** A denial did not say which profile refused, so a fix ("add the host to profile `default`") had to be looked up.
- **The name only.** A name can come back; see [instance-id.md](instance-id.md).

## The audit event

One JSON object per line, on the proxy's standard output, as before:

```json
{"time": "…", "conn": "5f0c3a9e1b2d4c6f", "protocol": "https", "event": "connect", "decision": "deny",
 "agent": "agent-3", "instance": "01JBX…", "profile": "default", "host": "registry.npmjs.org",
 "reason": {"code": "not-in-profile", "text": "not allowed by the agent's egress profile"}}
```

| field | what it is |
|---|---|
| `time` | when |
| `conn` | the connection: every event of one connection has the same, and no two connections share one |
| `protocol` | `https` or `ssh` |
| `event` | the step: `connect`, `request`, `close`; for https also `handshake`; for ssh also `upstream`, `exec`, `shell`, `sftp`, `channel` |
| `decision` | `allow`, `deny`, `error`, or `challenge` (below); a `close` has none |
| `mode` | on an allowed https `connect`: `tunnel`, `inject`, `inspect` or `intercept` |
| `agent`, `instance`, `profile` | who, once the proxy credentials are checked: the name, the instance's id, the profile that decided |
| `host`, `server` | the host; for ssh, `server` is the server's name in the profile |
| `method`, `path`, `status` | on a `request` (never the query string) |
| `command` | on an ssh `exec` |
| `reason` | `{code, text}`: `code` from the closed list below, for tools; `text` for people, which may change |
| `bytes_up`, `bytes_down`, `duration_ms` | on a `close` |

The steps:

- **https.** A CONNECT is one connection. Its `connect` has the decision: `allow` with a `mode` once the tunnel is up, or
  once the proxy decided to terminate TLS; `deny` when the policy refuses it; `error` when it fails (the upstream does not answer,
  the client leaves before its hello). A terminated connection whose TLS handshake with the proxy fails has a `handshake` error.
  An allowed connection always ends with a `close`. A terminated host (`inject`, `inspect`) has a `request` per request, allowed
  (with the status), denied (a request for another host, a placeholder that cannot be swapped) or failed (the upstream).
- **ssh.** An agent's connection to the ssh listener is one connection. `connect` is the sign-in (`allow` names the `server`;
  `deny` when the identity is not one of a profile's, or too many connections). `upstream` is an `error` when the proxy cannot
  reach the server or open a session on it. `exec`, `shell` and `sftp` are what the agent ran; `channel` and `request` are refused
  channels, forwarding and other requests. `close` carries the bytes, and the reason `idle` when the proxy closed it.
- **`challenge`.** A connection that arrives without proxy credentials gets a 407 and `decision: challenge`: browsers start that
  way and send the credentials once asked. It is not a denial and has no agent. A tool that never sends credentials shows up as
  nothing but challenges.

The reason codes:

| code | decision | meaning |
|---|---|---|
| `no-policy` | deny | the proxy was restarted and has no policy: `egzo restart proxy` or `egzo project up` |
| `no-credentials` | challenge | no proxy credentials yet |
| `credentials` | deny | wrong proxy credentials |
| `port` | deny | only port 443 is allowed |
| `not-in-profile` | deny | the host is not in the agent's profile |
| `private-address` | deny | the host is a private address |
| `limit` | deny | too many connections, the agent's or the proxy's |
| `client-hello` | deny | the client's TLS hello could not be read |
| `sni-mismatch` | deny | the TLS server name differs from the CONNECT host |
| `host-header` | deny | a request's `Host` header is malformed |
| `host-mismatch` | deny | a request names another host than its connection |
| `placeholder` | deny | a placeholder could not be swapped (a compressed or too large body, a secret that cannot go in a header) |
| `aborted` | error | the client left before the handshake (clients open connections ahead of time) |
| `dial` | error | the upstream did not answer (for ssh too, a cloud metadata address included) |
| `tls-handshake` | error | the client's TLS handshake with the proxy failed: it does not trust the project CA |
| `upstream` | error | a request to the upstream failed |
| `ech` | allow | why the mode is `intercept`: the client sent Encrypted Client Hello |
| `identity` | deny | ssh: no valid identity for a server of a profile |
| `channel` | deny | ssh: a channel that is not a session (port forwarding, X11) |
| `forwarding` | deny | ssh: a global request (remote forwarding) |
| `request` | deny | ssh: a session request that is not relayed (agent forwarding, a subsystem other than sftp) |
| `host-key` | error | ssh: the server presented another key than the pinned one, or the pin is not usable |
| `key` | error | ssh: the profile's key is not usable |
| `auth` | error | ssh: the server refused the key |
| `session` | error | ssh: the server did not open a session |
| `config` | error | ssh: the listener has no host key |
| `idle` | (close) | ssh: closed after being idle |

## The hub's Network view

Where it shows: a **Network** tab on the project page and on the agent page (filtered to that agent), with two parts.

- **Denied**: the denials and errors, grouped by `(instance, protocol, host, reason.code)` with a count and when each was first and
  last seen. Each group says why in plain words, which profile decided, and how to fix it: for `not-in-profile`, the YAML to add
  to that profile and `egzo project up`. A group that a later `allow` for the same instance and host follows is marked as fixed.
  `aborted` and `challenge` are not shown there.
- **Log**: the raw events, read from the engine when the tab is open (the last lines, then followed), with filters (agent,
  denied only, a host). One row per connection, from its `conn`.

**History.** The hub keeps the groups only, not the events. It follows each project's proxy log, as it already follows the control
sidecar's, and writes the groups to its database: one row per distinct denial, bounded per project and dropped 30 days after it
was last seen. After the hub was down it reads again from the time of the last event it saw. The raw log is the engine's, and
how far back it goes is the sysadmin's log configuration (see `known-issues/container-logging-follows-the-engine-default.md`);
it ends with the proxy's container. Keeping every event would make the hub a log store, a second copy of hosts and paths with a
retention of its own to answer for; long-term audit belongs to the sysadmin's log pipeline.

**Who sees it.** Administrators and owners of the project see every agent's network events; a user sees those of the agents they
spawned (the `actor` label), as for the agents themselves.

The Agents list shows how many hosts were denied to each agent in the last hour.

## Later

- An MCP tool of the control sidecar that tells an agent its own recent denials, so it can say "egzo denied registry.npmjs.org"
  instead of guessing, and one to ask for access, which the hub shows to the people who can change the profile.
- A temporary grant from the hub (one agent, one host, an hour). It is a change of policy outside the project file and needs its
  own design.
- Sampling repeated denials in the proxy itself (see "Audit sampling" in [review-leftovers.md](review-leftovers.md)).
- Direct connections a tool attempts because it ignores `HTTPS_PROXY`: the proxy never sees them.
