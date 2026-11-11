# Using sysbox

[Sysbox](https://github.com/nestybox/sysbox) is an optional layer under egzo. Like gVisor, it is a choice for the engine,
not for egzo: egzo never picks a container runtime, and everything the engine starts, agents and sidecars alike, runs under
the engine's default. Making that runtime sysbox's `sysbox-runc` is a decision for the host, and `egzo system check`
reports which runtime is in use.

Sysbox is supported on **rootful Docker only**. On Podman it does not apply (sysbox is registered as a Docker runtime and
needs root and systemd on the host), and a rootless engine cannot use it.

## Why sysbox

Sysbox is a fork of runc, so a container under it is still a container on the host's kernel. What it changes is that every
container gets a **Linux user namespace** of its own: the container's ids 0 to 65535 are mapped to an unprivileged range on
the host, so root in the container is not root on the host. A process that escaped the container's filesystem and
namespaces would find itself as an unprivileged host user, not root: on the community edition every sysbox container shares
one such range (the enterprise edition gives each its own), and on neither does it own what your account owns.

That matters for the case egzo cannot rule out: an agent that finds an escape from the container itself, through the
engine, a setuid binary in its image, or a mistake in its setup. Under a plain container on a rootful engine, that is root
on your machine. Under sysbox, it is an unprivileged user id.

Sysbox also virtualizes `/proc` and `/sys` inside the container: they are mounted read-write, but the parts that are not
namespaced by the kernel (a lot of `/proc/sys`, `/sys/module`, `/proc/sysrq-trigger`) are emulated by `sysbox-fs` and
cannot change the host. And it allows the `mount` and `umount` system calls inside the container while keeping the
container's own initial mounts immutable, which is what lets a full system (systemd, Docker) run in a container without
`--privileged`. egzo's agents do not need that, and do not get it (below), but it is why the runtime exists.

It is a different trade from gVisor: sysbox keeps the host kernel and adds a user namespace; gVisor replaces the kernel for
the sandbox and intercepts the system calls. [gVisor](gvisor.md) is the stronger boundary against a bug in the host's
kernel, and sysbox the stronger one against a process that becomes root inside its container. Pick one, not both: a host has
one default runtime.

## What an egzo agent gets, and what it does not

egzo runs agents as your uid and gid, not as root, and drops every capability: with sysbox, a container whose init is a
non-root user starts with **no capabilities** (its capability sets are zero, though the bounding set stays full). So the
system-container features sysbox is known for are out of reach in an egzo agent:

- **No Docker or systemd inside the agent.** Both need root in the container. A nested Docker daemon would also need a
  nested user namespace (rootless Docker), which sysbox does not support (`unshare -U` fails inside a sysbox container).
  What you get from sysbox in egzo is the id boundary above, not Docker-in-Docker.
- **No nested user namespaces**, so rootless tools, `bwrap`-style sandboxes (Flatpak, some test sandboxes), rootless
  Podman and similar do not start inside an agent. egzo's browser is not affected: the harness images already run Chromium
  without its own sandbox (`chromiumSandbox: false`), because the container is the boundary.
- **No devices made with `mknod`** (it fails with `operation not permitted`): `/dev/fuse`, `/dev/tun` and `/dev/tap` cannot
  be created, so a tool that needs one (a FUSE filesystem, some VPNs, an emulator) does not work. This is a sysbox
  limitation, not an egzo one.
- **No `binfmt_misc`** (`/proc/sys/fs/binfmt_misc` is refused), so multi-architecture builds that register QEMU do not
  work inside an agent.

egzo's own containers use none of the Docker options sysbox refuses (`--privileged`, `--userns=host`, `--pid=host`,
`--net=host`): agents have their own network, their own pid namespace and no capabilities anyway. Sysbox also ignores
Docker's AppArmor profile and does not support `--security-opt seccomp=<profile>`, which egzo does not use, and it does not
run on a host with SELinux enabled.

## How to use

1. Install the sysbox package for your architecture from its [releases](https://github.com/nestybox/sysbox/releases)
   (Ubuntu and Debian have packages; other distributions build it from source). It needs a host with systemd and root, and
   changes Docker's settings itself on install: it registers `sysbox-runc` as a runtime and adjusts Docker's network
   subnets, then restarts Docker (or sends it `SIGHUP`) if it had to. Check that its service is up:

   ```console
   $ systemctl status sysbox
   ```

2. Make it the engine's default runtime, in `/etc/docker/daemon.json`:

   ```console
   $ sudo jq '. + {"default-runtime": "sysbox-runc"}' /etc/docker/daemon.json > /tmp/daemon.json
   $ sudo mv /tmp/daemon.json /etc/docker/daemon.json
   $ sudo systemctl restart docker
   ```

   egzo never names a runtime per container, so this is what makes every container, agents included, a sysbox container.

3. Check it: `egzo system check` should name `sysbox-runc` as the runtime, and say that the rootful engine already runs a
   sandboxed runtime instead of pointing you at one.

## Requirements

- **Rootful Docker on a supported distribution, with systemd** on the host. This is the installation sysbox documents; a
  rootless engine cannot host it.
- **A kernel with idmapped mounts** (5.12 or later; sysbox recommends 5.19 or later). On an older kernel sysbox falls back
  to Ubuntu's `shiftfs` module, and a **bind mount of the engine's own socket does not work through shiftfs** (sysbox issue
  #404). A [linked project](hub.md) mounts that socket into its link sidecar, so on such a host the link would fail while
  the rest of the project ran. To check, mount the socket into a container and read its owner: the engine's host uid (0)
  means idmapped mounts are working.

  ```console
  $ docker run --rm -v /var/run/docker.sock:/var/run/docker.sock alpine stat -c %u /var/run/docker.sock
  0
  ```
- **On Ubuntu 25.04 and later**, an AppArmor profile for `fusermount3` denies the FUSE mounts `sysbox-fs` makes, and no
  sysbox container starts: `failed to pre-register with sysbox-fs`, with `fusermount3: mount failed: Permission denied` in
  `journalctl -u sysbox-fs`. The fix is pending in sysbox (issue #947); until it lands, allow sysbox in the profile's local
  file and reload it:

  ```console
  $ sudo tee -a /etc/apparmor.d/local/fusermount3 <<'EOF'
  mount fstype=@{fuse_types} options=(nosuid,nodev,rw) -> /var/lib/sysboxfs/**/,
  mount fstype=@{fuse_types} options=(nosuid,nodev,ro) -> /var/lib/sysboxfs/**/,
  umount /var/lib/sysboxfs/**/,
  EOF
  $ sudo apparmor_parser -r /etc/apparmor.d/fusermount3
  $ sudo systemctl restart sysbox
  ```

  `scripts/ci-engine.sh` does this for the sysbox spec platform, which runs on Ubuntu 26.04.
- **Docker's embedded DNS and internal networks** need no setting under sysbox: containers keep a normal network namespace,
  so `127.0.0.11` and the project's internal networks work as they do for any other container. (This is where gVisor needs
  its `--network=host` flag; sysbox does not.)

## The link, the proxy and the rest

Nothing else changes. The [proxy](sandboxing.md) and the [control](messaging.md) sidecars, [workspaces](workspaces.md),
[local ports](project-file.md#ports) and [ssh](sandboxing.md#ssh-to-servers) work as they do on a plain Docker host, under
the same runtime. Volumes are shifted to the container's user namespace and back, so a file an agent writes still has your
uid on the host, as it does on a plain Docker host: `egzo workspace get` hands it to you unchanged.

A [linked project](hub.md) gives its link sidecar the engine's socket. Sysbox idmapped-mounts the socket so that inside the
container it has the host's owner and group (uid 0, the docker group), which is exactly what egzo's link needs: it runs as
`65532:<the socket's group>`. Sysbox's own test suite covers this ("docker host socket mount"), and it is the first thing
to check on a host whose kernel falls back to shiftfs.

## Performance

Sysbox is a runc fork, so system calls go straight to the host kernel: compiling, running tests and reading files cost what
they cost in a normal container. Two things are slower:

- **Container start.** Sysbox allocates a user-id range and sets up its mounts, filesystem shifting and `sysbox-fs` for each
  container, so a project with several sidecars and agents takes a little longer to come up.
- **`/proc` and `/sys` reads and writes** go through `sysbox-fs`, a FUSE daemon, for the parts it virtualizes. Tools that
  scan `/proc` heavily pay a little more than in a plain container; ordinary file access is untouched.

We have not measured these for egzo's workloads. The spec suite has a `docker-sysbox` reference platform for this host; its
CI cell is experimental, like the gVisor one, until it has a clean history.

## If something fails

- `docker run --rm hello-world` failing while `egzo system check` names `sysbox-runc`: the runtime is registered but not
  working on this host. Check `systemctl status sysbox`, `journalctl -u sysbox-fs`, and the kernel and distribution
  requirements above: on Ubuntu 25.04 and later the usual cause is the `fusermount3` AppArmor denial, fixed as
  [Requirements](#requirements) says.
- A project that comes up but whose log is full of `operation not permitted` for `mknod`, `unshare` or `binfmt_misc`: one
  of the sysbox limits above, in the image or a tool the agent runs. Use an image that does not need it, or run that
  workload on a plain Docker host.
- A linked project failing to reach the engine with `permission denied` on `/run/engine.sock`: the socket mount is not
  idmapped (see [Requirements](#requirements)); the project still runs, unlinked, and `egzo ps` shows the link stopped.

[The security model](security.md) says what sysbox, and the sandbox in general, do not cover.
