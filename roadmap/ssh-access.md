# Roadmap: ssh access for agents (built; sftp open)

Built as option B below (the proxy terminates ssh and re-originates it): the design and what it does are in DESIGN.md ("ssh through the proxy") and
[docs/sandboxing.md](../docs/sandboxing.md#ssh-to-servers). Specs: `specs/test_egress_ssh.py` (the config) and `specs/proxy/test_proxy_ssh.py` (end to end, against
a real sshd that the specs start, `specs/sshd.py`).

Still open:
- **sftp / scp.** Subsystems are refused today. Low priority.
- **Short-lived ssh certificates (option C)**, an add-on that needs the server owner to trust an egzo CA.
- **FIDO (`sk-*`) keys and passphrase-protected keys** (golang/go#69904 for the first).
- **IPv6 literal hosts** in `host`.
- **Interactive session recording**: the audit has sessions and exec commands, not what is typed in a shell.
- Keep `golang.org/x/crypto` current and `govulncheck` clean (`make lint`): the proxy now holds an ssh server and client.

## Options that were considered
- **A. ssh-agent signing oracle.** Key in a sidecar; the agent gets `SSH_AUTH_SOCK`. A sign request names no destination host, so no per-host policy.
- **B. The proxy terminates ssh and re-originates it (built).** Key never leaves the proxy, per-host policy, audit, no default route.
- **C. Short-lived ssh certificates** from an egzo CA. Later.
- **Direct default route**: rejected for general use (loses the proxy, the audit and the address protections).

## Drawbacks found when this was researched (2026-10)
- A protocol terminator: anything not relayed does not work. Own dial timeouts, keepalives and idle timeouts were needed (x/crypto/ssh has reports of `Dial` hangs and rekey deadlocks).
- OpenSSH 7.4 rekey/ext-info quirk (golang/go#51808); stdlib `knownhosts` gap for hosts with another key type (the pin is a fingerprint or key line, tried by type).
- A steady stream of x/crypto advisories (Terrapin CVE-2023-48795, CVE-2025-58181, early-2026 fixes): track closely. `PublicKeyCallback` decides on the key that signed (CVE-2024-45337).
- A proxy restart kills live ssh sessions (like sshpiper#515); acceptable, the policy is reloaded by `up`.
- The key being safe does not limit what the agent runs on the server: a dedicated low-privilege account or a forced command.
