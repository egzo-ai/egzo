# Rootless Podman forwards a published port to one of a container's networks, chosen at random

Status: **worked around in egzo** (found 2026-11-01, Podman 5.7.0). A Podman behaviour, not an egzo bug.

A container on two networks gets one forwarder for all its published ports, and `GetRootlessPortChildIP` in Podman's `libnetwork/slirp4netns` picks the address it
forwards to: the first IPv4 address of a Go map, so a random network, at each start of the container. It does not skip internal networks. Docker always delivers
to the network a port was published on, and rootful Podman works too.

Reproduced without egzo: a container on an internal network and a normal one, listening on one address only, answered on 3 of 10 starts. The other starts connected
and got no answer. Listening on every address and reporting where a connection arrived showed 2 of 8 on the internal network. Podman's `main` still has the loop.

## What egzo does

The relay of a [local port](../docs/project-file.md#local-ports) is on the agent's internal network and a normal one, so it listens on both, and closes or drops whatever
comes from the agent's address. On rootless Podman a forwarded connection comes from the relay's own address, on either network, so the relay checks the agent's
address and never the network. The agent can connect to the relay on its own network, and reaches nothing through it (`docs/security.md`).

## Upstream

Podman means ports for a non-internal network. A maintainer said so on [podman#29792](https://github.com/podman-container-tools/podman/issues/29792), and
[container-libs@45771a1](https://github.com/containers/container-libs/commit/45771a120ef7dcd86bed2c3846160c1587d6f5d1) fixed it for the `pasta` forwarder
(`rootless_port_forwarder`), from Podman 6.2. That was read from the source and not tested. `rootlessport`, the default, was not changed, and we found no issue about it. If it
is fixed upstream, the relay can keep listening on both: it depends on neither.
