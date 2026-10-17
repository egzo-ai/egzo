# Rootful Podman: intermittent loss of outbound connectivity

Status: **diagnosed 2026-10-16, no workaround in egzo** (found 2026-10-04). A netavark bug, not an egzo bug; very likely
[containers/netavark#1497](https://github.com/containers/netavark/issues/1497) (fix proposed in
[#1498](https://github.com/containers/netavark/pull/1498)): netavark leaves a `/run/sysctl.d/10-netavark-<bridge>.conf` with
`forwarding = 0` behind when an internal network is removed, bridge names are reused, and `systemd-sysctl` applies the stale file
to the next network that gets the name, racing with netavark's own setup. To confirm on a failing runner: list
`/run/sysctl.d/10-netavark-*.conf` and compare the names with the bridges at `forwarding=0`.

## Diagnosis (2026-10-16, GitHub runner, Ubuntu 26.04, Podman 5.7.0, netavark 1.16.1)

A proxy that cannot dial out is on a bridge whose IPv4 forwarding is off. Netavark switches
`net.ipv4.conf.<bridge>.forwarding` off on the bridges of **internal** networks and on for the others. When
several networks and containers are set up at once, some **non-internal** bridges are left at `0`: the packets
of the containers on them arrive on the bridge (`tcpdump` shows the SYN on `podmanN`) and are never forwarded
(not counted in the `FORWARD` chain, no NAT, no reply). Everything else is in order in that state: routes,
addresses, `aardvark-dns` answers, netavark's isolation and masquerade rules.

- A failing proxy, for example: `podman1..podman5` with `forwarding=0` except `podman2`; the proxy's `eth0` on
  `podman3` (an egress network, not internal).
- Reproduces without egzo: six `podman network create X; podman run -d --network X ...` started at the same
  time left three of the six bridges at `forwarding=0` and three containers without egress. The same sequence
  one at a time (four rounds of an internal network, an external network and a container on both) left every
  external bridge at `1`.
- Serializing egzo's engine calls on Podman was tried and removed: it lowered the failures and did not remove them, and
  egzo must behave the same on every engine. The races it was aimed at (parallel network setup) were probably not the cause.
- `docker` on the same host, the host firewall's `FORWARD` policy (DROP, set by Docker) and the order of the
  nameservers were checked and are not the cause: with the bridge forwarding on, Podman containers reach the
  internet through the same rules.

What egzo does: nothing. An `up` after a `down` on rootful Podman can hit this by itself; the fix is upstream.

## Earlier notes (before the diagnosis)

## Symptom

On rootful Podman (spec engine `podman`), some proxy specs fail intermittently:

- `test_the_audit_log_records_allowed_and_denied_connections[podman]`
- `test_what_the_agent_sends_never_overrides_the_injected_credential[podman]`
- `test_rotating_a_secret_updates_the_proxy_without_recreating_anything[podman]`

The proxy's first outbound dial fails after 10s. The audit log shows
`action: error, reason: dial: dial tcp 104.20.23.154:443: i/o timeout`. The failure rate was
about 40% in a loop of the audit-log spec (5 of 12 runs).

A project that comes up broken stays broken (checked for 40s); one that comes up working works
immediately. Other engines (docker, docker-gvisor, podman-rootless) did not show it in the runs made.

## What was ruled out

- **Not egzo's parallel apply.** A build forced to run strictly serial failed 5 of 12, the same as
  the parallel build. Serializing network-touching calls on Podman did not help either (reverted).
- **Not the subnet or bridge name.** Identical layouts (for example `control=podman1/10.89.0.0/24`,
  `egress=podman2/10.89.1.0/24`) both failed and passed.
- **Not the destination.** Both addresses of example.com answer from the host, from a rootful
  container and from a rootless container.
- **Not a missing or duplicate default route.** In a failing run the proxy had one default route
  (`default via 10.89.0.1 dev eth0`), so netavark#1146 (random default gateway on multi-network
  containers) does not apply.
- **Reproduces without egzo.** `podman --remote --url unix:///run/podman/podman.sock network create`
  plus `run --network <it> ... curl https://example.com/` failed on the first of ten attempts. The
  default `podman` network (6 of 6) worked.

## Environment

- Podman 5.7.0, netavark 1.16.1, aardvark-dns 1.16.0, iptables-nft backend.
- Docker runs on the same host (rootful), plus rootless Podman.
- The host firewall was already fixed once so rootful Podman containers could reach the network at all
  (before that, even a raw IP timed out). Podman's default bridge is 10.88.0.0/16; egzo's networks get
  10.89.x.0/24 and up.

## Known issues that look related

- Docker plus rootful Podman: with netavark's nftables driver, Docker's forwarding block leaves Podman
  containers without external connectivity. Fix: an accept rule for Podman's traffic, or set netavark's
  firewall driver back to iptables in `containers.conf`.
  [Fedora: Changes/NetavarkNftablesDefault](https://fedoraproject.org/wiki/Changes/NetavarkNftablesDefault),
  [GoLinuxCloud: Podman No Internet](https://www.golinuxcloud.com/podman-container-no-internet/)
- Reloading the firewall deletes netavark's rules and rootful containers lose connectivity until
  `podman network reload` restores them (same guide).
- Egress broken after a `dockerd` restart, with the NAT/MASQUERADE rules gone from nftables:
  [BetterFleet#890](https://github.com/zelytra/BetterFleet/issues/890).
- Checked, does not match: [netavark#1146](https://github.com/containers/netavark/issues/1146) (random
  default gateway, closed "not planned"); [netavark#1508](https://github.com/containers/netavark/issues/1508)
  (missing mark-masquerade rule for DNS on internal networks) is unverified against this host.

No upstream issue matches the exact behavior.

## Next steps (need root) (partly done by the diagnosis above)

1. In a failing state, compare `sudo nft list ruleset` and `sudo iptables -S FORWARD` with a working
   run. A missing masquerade or forward rule for the failing network's bridge confirms rule loss.
2. In a failing state, run `sudo podman network reload --all`. If connectivity returns, the rules
   were lost.
3. Check which firewall driver netavark uses; try `firewall_driver = "iptables"` in `containers.conf`.
4. Find what else on the host rewrites firewall rules (Docker events, a firewall service, ufw).

## Effect on the specs

The rootful `podman` engine is required, so these proxy specs fail on this host when the flake hits.
Everything else in the agent, project and proxy specs passed on it (38 passed, 3 failed, 4 skipped, 1 xfail in one run, before
the specs were reorganised: the three proxy specs above are now scenarios of `proxy/`).
