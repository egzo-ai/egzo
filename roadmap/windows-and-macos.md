# Roadmap: macOS and Windows (WSL2) hosts

Today egzo supports a Linux host with Docker or Podman. The goal: also work on **macOS** and on **Windows through
WSL2**, assuming Docker is already installed through **Docker Desktop** (or a similar VM-based engine such as Colima,
Rancher Desktop or OrbStack). Agents and sidecars stay Linux containers.

**Not a goal: a native Windows `egzo.exe`.** On Windows, egzo is the Linux binary running inside a WSL2 distro. That
keeps one code path and avoids ConPTY, named pipes, `C:\` path translation and Windows file locking. Anyone running
Docker Desktop on Windows already has WSL2 (its default backend), and a serious user of a Linux tool there will use it.

Nothing here is built or tested. Items come from reading the code, not from a port attempt.

## What stays the same

The sidecars (`proxy`, `control`, `prep`) and the harness images are Linux containers, and run inside the Docker
Desktop VM unchanged. The egress model (internal networks, the proxy) is engine-side. The CLI is the existing Linux
build.

## Windows via WSL2

The CLI runs in a WSL2 distro (Ubuntu, for example) and talks to Docker Desktop through its WSL integration, which
exposes the engine socket inside the distro. Because the CLI is a Linux process on a Linux filesystem, almost all of the
macOS list below does not apply: `flock`, `SIGWINCH`, terminal raw mode, uid and gid and git semantics are all
real.

The rules to document and check:
- **Keep the project in the WSL filesystem** (`~/src/shop`), not under `/mnt/c`. Bind-mounting from `/mnt/c` goes
  through the Windows file-sharing layer: slow, with odd ownership and permissions, and git behaves differently.
  `egzo doctor` should warn when the project directory is under `/mnt/`.
- **Docker Desktop's WSL integration** must be enabled for the distro (Settings, Resources, WSL integration). `doctor`
  should say so when it finds no engine socket in a WSL distro.
- **Docker Engine installed inside the distro** (instead of Desktop) is just the Linux case, and could register
  gVisor. Worth stating as the alternative.
- gVisor is not available on Docker Desktop; `runtime:` is refused as it is on Podman.

Work: document it, run the quick start and the specs on a real WSL2 distro with Docker Desktop, add the two `doctor`
hints, and decide how the platform is named in the matrix. Likely small.

## macOS

The CLI is native (`darwin/arm64`, `darwin/amd64`), so this is where the code has to change.

**1. Engine connection** (`internal/engine`)
- The socket is often `~/.docker/run/docker.sock` or comes from a Docker context, not `/var/run/docker.sock`. Read the
  active Docker context as well as `DOCKER_HOST`.
- `egzo doctor` should report what it found (Desktop, Colima, OrbStack).

**2. Files the CLI writes and mounts**
- **Host paths** (`./docs:ro`, `path:` for checkouts, `.egzo/workspaces`) are bind-mounted through the VM's file
  sharing (virtiofs or gRPC FUSE), and the directory must be one Docker Desktop may share.
- **File ownership.** Agents run as the invoking user's uid and gid (`os.Getuid()` in `internal/stack/up.go`). On macOS
  these differ from the VM's, and Desktop's file sharing maps ownership itself, so "run as your uid" must become "run
  as whatever makes the shared files writable".
- **Git checkouts** are made by the prep container into the shared directory. Check that `--relative-paths` worktrees,
  symlinks and executable bits survive the sharing layer, and that case-insensitive filesystems do not break a
  repository with names that differ by case.
- **Performance.** A bind-mounted checkout can be several times slower than native, which hurts `git` and build tools in
  big repositories. `clone` into a named volume instead of a host directory may be the better default there (and
  `worktree` with its relative links needs a look). Decide after measuring.

**3. The CLI itself**
- The project lock uses `syscall.Flock` (`internal/stack/lock.go`), which exists on macOS. Terminal raw mode and
  `SIGWINCH` (`internal/cli/daily.go`, `internal/session/client.go`) work too. A `GOOS=darwin` build and the attach
  fidelity specs (detach key, paste, resize) on macOS terminals will tell.

**4. Distribution**
- Release binaries for `darwin/arm64` and `darwin/amd64`, with checksums, and a Homebrew tap. This extends the install
  wrapper in [`initial-release.md`](initial-release.md).
- Harness images are multi-arch (`linux/amd64` and `linux/arm64`), since Apple silicon runs arm64 Linux containers.
  Check that the harnesses (Node, `claude`, `opencode`) and the egzo binary build for arm64. This helps Linux arm64 too.

**5. Isolation differences to document**
- On Docker Desktop the engine's "host" is a VM: an extra boundary, but shared by all your containers. The
  rootless-engine advice in [`docs/security.md`](../docs/security.md) does not map directly.
- Internal networks must work on Desktop (`egzo doctor` already tests that the engine makes them).
- Egzo never mounts the engine socket into an agent, and that stays so.

## Testing

- Add a `GOOS=darwin` build and `go vet` job to CI as the first, cheap step.
- Running the specs needs a Docker engine on the runner. Hosted macOS runners generally have none (no nested
  virtualization on Apple silicon), so this needs a self-hosted Mac with Docker Desktop, Colima or OrbStack, or a remote
  engine through `DOCKER_HOST`. For WSL2, a self-hosted Windows machine, or a hosted Windows runner if WSL2 with Docker
  works there. They join the [CI test matrix](ci-test-matrix.md) as reference platforms such as `docker-desktop-macos`
  and `docker-desktop-wsl2`.
- The specs are Python and assume a POSIX host in places (bind-mount paths, `chown`); check them on macOS.
- Add platform notes to `specs/README.md` and its platform table.

## Work to do

- [ ] WSL2: try the quick start and the specs on WSL2 with Docker Desktop; document the setup and the `/mnt/c` rule;
      add the two `doctor` hints.
- [ ] macOS: cross-compile for `darwin`; engine discovery through Docker contexts and Desktop sockets.
- [ ] Decide the uid/gid and ownership story on Desktop, and the default workspace storage (host bind vs named volume),
      after measuring on both macOS and WSL2.
- [ ] Multi-arch images; macOS release binaries and a Homebrew tap.
- [ ] Reference platforms and runners in the CI matrix.
- [ ] Update `docs/install.md` (engine and platform table), `docs/security.md` and `docs/workspaces.md` (host-path
      caveats) when it lands.
