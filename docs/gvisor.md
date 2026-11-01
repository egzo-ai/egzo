# Using gVisor

[gVisor](https://gvisor.dev) is an optional layer under egzo. egzo does not choose a container runtime: everything the engine
starts, agents and sidecars alike, runs under the engine's default runtime. Making that runtime gVisor's `runsc` is a decision
for the host, and `egzo system check` reports which runtime is in use.

gVisor is supported on Docker. On Podman it is untested.

## Why gVisor

A normal container shares the host's kernel: every system call an agent makes is answered by the host kernel, so a bug in that
kernel is a way out of the container. gVisor puts a second kernel, written in Go and running in user space, between the agent
and the host. The agent's system calls are answered by gVisor, and only a small, filtered set reaches the host kernel.

For egzo this matters because an agent runs code that nobody reviewed: what the model writes, what a dependency installs, what a
web page tells it to run. egzo's own barriers (an internal network per agent, the proxy as its only way out, a workspace the
agent cannot leave) are enforced by the engine and the host kernel. gVisor adds a barrier against an escape through the kernel
itself. It reduces that risk and does not remove it; see [the security model](security.md).

## Performance impact

System calls are interpreted by gVisor instead of going straight to the kernel, so anything that makes many of them is slower:

- **File access** goes through gVisor's file proxy (the gofer). Large builds, package installs, `git status` on a big tree, and
  file watchers are noticeably slower, and inotify over mounted directories behaves differently from a normal container.
- **Networking** has some overhead, and a lot more of it if gVisor's own network stack is used (egzo needs the host network
  option below, which avoids part of that cost).
- **CPU-bound work** (compiling, running tests once the files are read) runs at nearly full speed.
- **Start-up** of each container takes a little longer.

We have not measured these numbers for egzo's workloads. If your agents spend their time on large trees or many small files,
try one before you commit to it.

Some terminal behaviours differ too (Ctrl-D at the start of a line, `tty` in `docker exec -it`);
[`known-issues/gvisor-terminal-quirks.md`](../known-issues/gvisor-terminal-quirks.md) lists them.

## How to use

1. Install `runsc`. Use a recent release: the OpenCode terminal needs `release-20261012` or later.
2. Register it with Docker, with two flags:

   ```console
   $ sudo runsc install -- --network=host --host-uds=open
   ```

   This adds the `runsc` runtime to `/etc/docker/daemon.json`.
3. Make it the engine's default runtime: add `"default-runtime": "runsc"` to the same file, then restart Docker
   (`sudo systemctl restart docker`).
4. Check it: `egzo system check` should name `runsc` as the runtime.

Without both flags egzo starts, but things fail in the two ways described next.

## Why the flags are required

**`--network=host`: names must resolve.** egzo finds its sidecars by name (`control`, `proxy`) on each project's network. On a
Docker network the name is answered by Docker's embedded DNS server, which listens on `127.0.0.11` inside the container's network
namespace. gVisor's default network (`sandbox`) runs a network stack of its own inside the sandbox, which cannot reach that
listener: every lookup is refused, and the sidecars could not resolve the hosts the proxy dials either. The gVisor FAQ describes
the problem ("DNS doesn't work on a Docker user defined bridge network") and the issue is open
([google/gvisor#7469](https://github.com/google/gvisor/issues/7469)). Tried and not working: `--reproduce-nat` (needs the legacy
iptables interface; on an nftables host no container starts) and `--reproduce-nftables` (containers start, names still do not
resolve). With `--network=host` the sandbox uses the host kernel's network stack, names resolve, and the syscall
interception stays. What is lost is gVisor's own network stack as a second barrier against bugs in the host's network stack;
gVisor documents this mode as decreasing the isolation from the host. egzo's isolation does not rest on it: each agent is alone
on an internal network with the proxy as its only way out, enforced by Docker and the kernel as for any other runtime.

**`--host-uds=open`: the link must reach the engine.** A [linked project](hub.md) runs a small sidecar, the link, that talks to
the engine through its socket (`/var/run/docker.sock`), bind-mounted into the container. By default gVisor does not let a sandboxed
process connect to a Unix socket of the host: `connect()` fails with "connection refused" even though the file is there and its
permissions are right, so the link reports "Cannot connect to the Docker daemon". `--host-uds=open` lets processes in the sandbox
connect to host sockets that are mounted into it (it does not let them create sockets on the host). Only a container that is given
a host socket is affected, which here is the link alone. The link holds the engine's socket either way, which is as much as root
on the host, so what gVisor could add around it is small. A project that is not linked does not need this flag.
