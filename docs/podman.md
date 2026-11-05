# Using Podman

egzo works with **rootless Podman**, a supported platform. **Rootful Podman** is not supported for now, because of a netavark bug
that makes outbound connections fail at random (below); whether it is supported in 1.0.0 is decided in
[#2](https://github.com/egzo-ai/egzo/issues/2).

## Rootless Podman

Start Podman's user socket and point `DOCKER_HOST` at it:

```console
$ systemctl --user enable --now podman.socket
$ export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock
```

gVisor on rootless Podman is untested.

Rootless Podman forwards a published port to one of a container's networks, chosen at random at each start. egzo's relay for
[local ports](project-file.md#local-ports) handles it: it listens on both its networks and checks the agent's address, never the network.

A proxy that cannot resolve or reach a host has been seen twice on rootless Podman in CI
(`dial tcp: lookup github.com: i/o timeout`), which looks like the rootful bug below, since rootless Podman also puts the proxy on a
netavark bridge. Not confirmed: nobody has looked at the bridge's `forwarding` on a failing rootless host yet.

## Rootful Podman: outbound connections fail at random

A netavark bug, not an egzo one: very likely [containers/netavark#1497](https://github.com/containers/netavark/issues/1497) (fix proposed in
[#1498](https://github.com/containers/netavark/pull/1498)). Netavark leaves a `/run/sysctl.d/10-netavark-<bridge>.conf` with
`forwarding = 0` behind when an internal network is removed, bridge names are reused, and `systemd-sysctl` applies the stale file to the
next network that gets the name, racing with netavark's own setup.

**What you see.** The project's proxy cannot dial out: `egzo proxy log` shows `decision: error`, `reason: dial … i/o timeout`. A project
that comes up broken stays broken; one that comes up working keeps working. An `up` after a `down` can hit it by itself. egzo does nothing
about it: the fix is upstream.

**To confirm** on a failing host: list `/run/sysctl.d/10-netavark-*.conf` and compare the names with the bridges whose
`net.ipv4.conf.<bridge>.forwarding` is `0`.

**The diagnosis** (2026-10-16, Ubuntu 26.04, Podman 5.7.0, netavark 1.16.1, iptables-nft). A proxy that cannot dial out is on a
bridge whose IPv4 forwarding is off. Netavark switches forwarding off on the bridges of **internal** networks and on for the others; when
several networks and containers are set up at once, some **non-internal** bridges are left at `0`. The packets arrive on the bridge
(`tcpdump` shows the SYN) and are never forwarded (not counted in the `FORWARD` chain, no NAT, no reply), while routes, addresses,
`aardvark-dns`, and netavark's isolation and masquerade rules are all in order.

- It reproduces without egzo: six `podman network create X; podman run -d --network X ...` started at the same time left three of the
  six bridges at `forwarding=0` and three containers without egress. The same sequence one at a time left every external bridge at `1`.
- Serializing egzo's engine calls on Podman lowered the failures and did not remove them, and was removed: egzo behaves the same on every
  engine.
- Docker on the same host, the host firewall's `FORWARD` policy and the order of the nameservers are not the cause: with the bridge
  forwarding on, Podman containers reach the internet through the same rules.

Other problems that look similar but are not this one: Docker next to rootful Podman with netavark's nftables driver (Docker's forwarding
block leaves Podman containers without connectivity: set netavark's `firewall_driver = "iptables"` in `containers.conf`, or add an accept
rule), and a firewall reload, which deletes netavark's rules until `podman network reload`.

## Rootful Podman on AppArmor hosts: `exec` cannot reach the container's processes

Host-level (seen on Ubuntu 26.04, Podman 5.7.0, crun, AppArmor profile `containers-default-0.66.0`), not an egzo bug as far as we can
tell; tracked in [#144](https://github.com/egzo-ai/egzo/issues/144).

Once a container has `no-new-privileges` (egzo sets it on every container), the processes `podman exec` starts and the container's own
processes are in different AppArmor contexts: `containers-default-0.66.0` for init and the container's processes,
`containers-default-0.66.0//&crun (mixed)` for an exec'd one. They cannot ptrace or signal each other, even with the same uid:

```console
$ sudo podman run --rm --init --user 1001:1001 --security-opt no-new-privileges alpine:3 \
    sh -c 'ls -l /proc/1/exe; echo x > /proc/1/fd/1'     # Permission denied on both
```

Without `no-new-privileges`, or with `--security-opt apparmor=unconfined`, both work. Docker and rootless Podman are not affected.

**What breaks.** Processes the agent's own tree starts are in one context and work. What breaks is a process started through `exec` (an
operator's `egzo agent exec`) acting on one the container started: reading another process's working directory, writing to
`/proc/1/fd/1`, and the browser (`kill EACCES` in the MCP client).

**Workaround.** Set `apparmor_profile = "unconfined"` in `containers.conf`. egzo's CI does this for its rootful Podman cell
(`scripts/ci-engine.sh`).
