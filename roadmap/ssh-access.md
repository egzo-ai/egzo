# Roadmap: ssh access for agents (idea, not decided, not built)

Need: an agent that can ssh to a remote server (or git over ssh) without a default route and without ever
holding the private key. Today the proxy is CONNECT-only on port 443 and git sources are https-only.

Options considered:
- **A. ssh-agent signing oracle.** Key in a sidecar; `egzo-agent` exposes `SSH_AUTH_SOCK` inside the agent
  container and relays over TCP (host-created unix sockets are doubtful under gVisor). Small, but a sign request
  names no destination host, so no per-host policy, and a compromised agent can sign while the sidecar runs.
  Still needs raw TCP to port 22 through the proxy.
- **B. The proxy terminates ssh and re-originates it (preferred).** The ssh analogue of TLS `inject`. The agent
  ssh's to a proxy listener, authenticated by a per-instance identity key that `spawn` generates and binds in the
  policy (an identity, not a secret). The proxy dials the real server with the vault key and a **pinned host key**
  (required, no trust on first use) and relays channels (shell, exec, sftp). Port and agent forwarding refused.
  Key never leaves the proxy, per-host policy, audit of sessions and exec commands, no default route, no CONNECT
  change. Sketch: `egress.<profile>.ssh.<name>: {host, user, key: <vault>/<secret>, host_key}`.
- **C. Short-lived ssh certificates** from an egzo CA. Needs the server owner to trust the CA. Add-on, later.
- **Direct default route** (`Internal: false`): rejected for general use. Loses the proxy, the audit log and the
  private-address and metadata protection. Only acceptable as an explicit "trusted agent" template option.

Library: `golang.org/x/crypto/ssh` (server and client halves, raw channels). New direct dependency. Reference for
the relay (read, not vendored): `tg123/sshpiper` (MIT), also Teleport's forwarding node (not yet read).

Drawbacks found (research, 2026-10):
- A protocol terminator: anything not relayed does not work. We must add our own dial timeouts, keepalives and
  idle timeouts; x/crypto/ssh has open reports of `Dial` hangs and rekey deadlocks.
- No FIDO (`sk-*`) private keys in x/crypto/ssh (golang/go#69904), so file keys only.
- OpenSSH 7.4 rekey/ext-info quirk (golang/go#51808); stdlib `knownhosts` gap for hosts with another key type.
- Steady stream of x/crypto advisories (Terrapin CVE-2023-48795, CVE-2025-58181, early-2026 fixes for
  PartialSuccessError and agent-constraint handling). Adds an ssh server to the component holding the keys, so
  track x/crypto closely and run `govulncheck`. `PublicKeyCallback` must compare against the bound instance key only
  (CVE-2024-45337).
- Proxy restart kills live ssh sessions (same as sshpiper#515); acceptable, the policy is already in memory.
- The key being safe does not limit what the agent runs on the server: use a dedicated low-privilege remote user
  or a forced command.

Open: whether sshpiper relays sftp and `window-change`/`signal`/half-close correctly; keepalive support in
x/crypto/ssh. Read sshpiper's relay code before building. No specs yet: add them when it is scheduled, so the
suite does not go red for a feature nobody has started.
