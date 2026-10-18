# Roadmap: proxy protocol structure

The egress proxy (`internal/proxy`) speaks two protocols: HTTP and TLS interception on `:3128`, and an ssh relay on
`:2222` (see [ssh access](ssh-access.md)). They share one `Server` type in one flat package, with no handler or plugin
layer. That was a deliberate choice for two protocols; this entry records when to revisit it and what to do.

## Today

- `run.go` starts both listeners. `ServeHTTP` and `ServeSSH` are methods of the same `*Server`.
- What the protocols share: the policy and its profiles, the per-agent bindings (`BindAgent`, `UnbindAgent`,
  `authenticate`), the audit record, the connection limits, and the rule that unbinding an agent closes its connections.
- What is specific to one: the CA, leaf certificates, SNI, placeholders and the intercept handler (HTTP); the identity
  derived from the token, the upstream dial, host-key pinning and the session relay (ssh).
- Policy stays typed per protocol: `Profile.Services` for HTTP, `Profile.SSH` for ssh.
- `server.go` is about 650 lines and mixes the HTTP handler with the state both protocols use. The ssh files add their
  own fields and tracking maps to the same struct.

## Trigger

A third protocol (a database wire protocol, git over ssh, an SMTP relay). With two protocols any interface is a guess;
a third shows what they really share. Until then, do not add an abstraction.

## What to do then

1. **Extract the core.** Move auth, bindings, policy lookup, the audit sink and the limits out of `server.go` into a type
   the protocols depend on, and shrink what a protocol needs from it to a small interface.
2. **One package per protocol.** `internal/proxy/http`, `internal/proxy/ssh`, and the new one, each depending on the core.
   The core never imports a protocol.
3. **Keep policy typed.** Each protocol owns its section of the profile and its validation in `internal/config`. Do not turn
   `Profile` into a generic map: strict `egzo.yaml` parsing is a feature.
4. **Wiring stays explicit.** `run.go` names the listeners it starts; no registration at import time.
5. **Specs do not change.** The behaviour specs go through the sidecar, so a pure refactor leaves them as they are. They
   are the check that it was one.

## Not in scope

A loadable plugin mechanism. Protocols are compiled in; the point is a clear boundary, not third-party extension.
