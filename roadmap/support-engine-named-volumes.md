# Roadmap: support engine named volumes

Status: proposed, later addition. Nothing is built and there are no specs yet.

## Why

Portable workspaces (`roadmap/portable-workspaces.md`) dropped host-bound directories with no escape hatch. That is
right for almost everyone, but some users need an agent to reach data that lives outside egzo: a NAS share, a large
cache, a media library. The escape hatch is **not** a host path in the project file. It is a reference to a volume that
already exists in the engine.

## The idea

The user creates the volume with Docker, with any driver, options or plugin they like. egzo only references it by name.

    # a host directory or a NAS already mounted on the engine host
    docker volume create -d local -o type=none -o o=bind -o device=/mnt/nas/media nas-media
    # or let the engine mount the share itself (works against a remote engine too)
    docker volume create -d local -o type=nfs -o o=addr=nas,ro -o device=:/export/media nas-media
    docker volume create -d local -o type=cifs -o device=//nas/media \
        -o o=credentials=/etc/egzo/nas.cred,uid=1000,gid=1000 nas-media

Everything about host paths, NFS, CIFS, plugins and credentials stays in that `docker volume create` step, outside egzo.
The project file never contains a host path, so a cloned `egzo.yaml` cannot name `/` or `~/.ssh`: it can only name
volumes that someone with engine access already created.

## In the project file

A new workspace kind, `{volume: NAME}`, next to `{}` and `{git: ...}`:

```yaml
workspaces:
  repo:
    git: { url: https://github.com/acme/shop.git, branch: main }
  media:
    volume: nas-media              # an existing engine volume; egzo never creates or removes it
    readonly: true                 # the volume is mounted read-only in every agent
  cache:
    volume: build-cache
    subpath: shop/gradle           # mount only this directory of the volume

agents:
  coder:
    harness: claude-code
    workspaces: [repo, media, cache]    # /workspace/repo, /workspace/media, /workspace/cache
  reviewer:
    harness: claude-code
    workspaces: [repo, "cache:ro"]      # :ro per agent works as for a directory workspace
```

- `volume`: the name of an engine volume. Required for this kind; exclusive with `git`.
- `subpath`: a relative path inside the volume (engine `VolumeOptions.Subpath`, Docker 26+). No `..`, not absolute.
- `readonly`: makes the volume read-only for every agent. `:ro` on the agent's reference does the same for that agent
  only. Either one wins (read-only if any says so).
- The workspace name is what agents mount: `/workspace/<name>`, as for any workspace.

## What egzo does with it

| Step | Behaviour |
|---|---|
| `up` | checks the volume exists (`VolumeInspect`); an error names the workspace and the volume and says how to create it |
| `spawn` | mounts it with `Type: volume, Source: <name>, ReadOnly, VolumeOptions.Subpath` at `/workspace/<name>` |
| ownership | none: no prep container, no chown. The agent user must already be able to read (and write) it |
| `down`, `rm` | never touch it |
| `workspace ls` | lists it as `external`, with the volume's driver and whether it exists |
| `workspace put|get|rm|fetch` | refuse an external volume, saying it is not egzo's to manage |
| labels | none added; egzo never relabels a volume it did not create |

## Guardrails

- A volume that carries egzo's own labels (`ai.egzo.*`), or whose name is one of egzo's volumes (`<project>_...`), is
  refused: one project must not mount another's workspace, the CA private key or the control state.
- The same external volume may be referenced by several workspaces and projects; that is the user's choice.
- `readonly` is enforced by the engine, so an agent cannot remount it read-write.
- `security.md` states plainly: an external volume is exactly as exposed as the user makes it. A read-write NAS volume
  lets an agent delete or corrupt it. Prefer `readonly`.

## Ownership and permissions (documented, not solved)

egzo does not chown an external volume, so the agent user's uid must be allowed by the volume itself. This is fixed on
the Docker side: CIFS `uid=`/`gid=` options, NFS export settings, host permissions for a bind-backed volume. The docs
say so under "Workspaces" and in `troubleshooting.md`, with the error an agent sees (`Permission denied` on the mount).

## Platform notes

- Verified on Docker 29.1.3: a `type=none,o=bind` volume mounts, honours `readonly` (writes fail with "Read-only file
  system") and `volume-subpath`.
- gVisor (`runsc`): file access goes through the gofer, so it is slower, and inotify over network filesystems behaves
  differently. Specs for this run on `docker-gvisor` too.
- Podman: named volumes and subpath depend on the Podman version; to be checked when Podman specs exist.
- Docker Desktop and a remote engine: the volume is resolved by the engine, so a `bind` source path is a path on the
  engine's host (inside the VM on Desktop). NFS and CIFS volumes avoid that question.

## Not in scope

- Creating, labelling, snapshotting or deleting external volumes from egzo.
- Host paths in the project file, in any form (`path:`, `bind:`, a user-level allow-list). If real demand appears, a
  user-level allow-list of named host mounts is the safer design than a key in the project file.
- Making `:ro` of a git workspace legal (unchanged).

## Order of work

1. Specs first (they go red): schema (`volume`, `subpath`, `readonly`, exclusive with `git`, bad subpath), `up` fails
   on a missing volume, a mounted volume is read-only and shows only its subpath, egzo-labelled volumes are refused,
   `down` and `workspace rm` leave it alone, `workspace ls` shows it as external.
2. config: the `volume` workspace kind, validation, resolved view.
3. stack: the mount in the agent spec (`Subpath`, `ReadOnly`), the existence check in `up`, skipped by ownership,
   `down` and `rm`.
4. cli: `workspace ls` and the refusals.
5. Docs: `workspaces.md`, `project-file.md`, `security.md`, `troubleshooting.md`.
