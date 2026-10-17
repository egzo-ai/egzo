# gVisor on Docker: container names do not resolve unless runsc shares the host network stack

Status: **works with a setting that weakens network isolation** (found 2026-10-17). A gVisor limitation, not an egzo bug.

egzo finds its sidecars by name (`control`, `proxy`) on each project's and agent's network. On a Docker user-defined network
the name is answered by Docker's embedded DNS server, which dockerd binds on `127.0.0.11` inside the container's network
namespace and reaches through NAT rules (`127.0.0.11:53` redirected to a random port). gVisor's default network mode
(`network=sandbox`) runs its own network stack inside the sandbox, which neither reads those rules nor reaches the listener in
the container's namespace. Inside a gVisor container, every lookup fails (`Connection refused` from `127.0.0.11`,
`getent hosts control` exits 2), and the sidecars could not resolve an upstream host either.

What the project says, in its own words:

- The gVisor FAQ, "DNS doesn't work on a Docker user defined bridge network, including Docker Compose" (<https://gvisor.dev/docs/user_guide/faq/>):
  Docker's user-defined bridge uses an embedded DNS server bound to the loopback interface, and "runsc network is isolated from the
  host and cannot access the DNS server on the host network". Its workarounds: the default bridge with `--link`, a custom
  `resolv.conf` (public names only), `--network=host` in runsc ("uses the host network stack and is less secure"), or IP addresses.
- The open issue, google/gvisor#7469 (<https://github.com/google/gvisor/issues/7469>): Docker's netfilter rules are not loaded at
  boot, and a maintainer doubts that copying them in would work "because the redirect targets the sentry's loopback, while dockerd
  listens in the container namespace's loopback". Not fixed.
- The network modes (<https://gvisor.dev/docs/user_guide/networking/>): in host mode the sandbox uses the host's network stack,
  "including the loopback", and "this mode decreases the isolation to the host".

Tried on Ubuntu 26.04, Docker 29, runsc release-20260921 and release-20261012:

| `runsc` registered with | result |
|---|---|
| nothing (`network=sandbox`) | containers start, no name resolves |
| `--reproduce-nat` | reads the legacy iptables interface, which an nftables host does not have: on the CI runner no container starts (`failed to write NAT blob: failed to call IPT_SO_GET_INFO`); on a host that has it, names still do not resolve |
| `--reproduce-nftables` | containers start, names still do not resolve |
| `--network=host` | names resolve, the internet is reachable, `/proc/version` says gVisor |

So a Docker host that wants egzo's agents under gVisor registers it with `--network=host`
(`runsc install -- --network=host`, then `"default-runtime": "runsc"`). The syscall interception stays; what is lost is gVisor's own
network stack as a second barrier against bugs in the host kernel's network stack. egzo's isolation does not rest on it: each agent is
alone on an internal network with the proxy as its only way out, enforced by Docker and the kernel as with any other runtime. This is
the platform `docker-gvisor` of the specs.

Not an option for egzo: addressing sidecars by IP instead of by name would avoid the DNS, but the sidecars also need DNS for the
hosts the proxy dials, which gVisor's sandbox network cannot do on a user-defined network either.
