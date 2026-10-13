# Roadmap: portable workspaces (engine volumes, no host directories)

Status: decided, to be built. The specs describe the target and fail until it lands.

## Why

egzo core must be operable with **engine access alone**. The hub (`roadmap/hub.md`) runs in a container, may
sit on another machine than the project, and has no access to the project directory. Today `spawn`, `rm`, `down` and
`agent start` still touch the host:

- a flock on `<project dir>/.egzo/lock`;
- workspaces are host directories (`mkdir`, existence checks on worktrees and branches, `RemoveAll` of worktrees,
  bind mounts built from host paths);
- the agent's `prompt:` file is bind-mounted from the host.

Workspaces become **engine volumes**, the prompt travels in the published template, and the lock moves into the engine.
After this, `spawn`, `rm`, `start`, `stop`, `attach`, `message`, `workspace fetch` and `down` need nothing but the engine.
`up` still reads `egzo.yaml`, the vaults and the prompt files on the machine that runs the CLI, as it always did
(the hub never runs `up`).

Decision: **host-bound directories are dropped**, with no escape hatch. One code path.

## What changes for the user

- `path:` is gone from a workspace. It is an error that says what to do instead.
- A workspace is `{}` (an empty directory kept in a volume) or `{git: {url, branch}}`.
- Files go in and out with commands, not with an editor on the host:
  `egzo workspace put|get|ls|rm|fetch`. The primary output channel is still `git push` through the proxy.
- `.gitignore` is no longer touched by `project init`, and `system check` no longer asks for it: egzo leaves nothing in
  the project directory except what the user wrote.
- Lost: an editor on the host seeing the agent's files live, and an agent working in a directory of yours in place.
  Docker Desktop and WSL2 gain (no bind-mount slowness, no `/mnt/c`), as does rootless Podman (no uid mapping of host
  directories).

## Volumes

All carry the project labels, `ai.egzo.kind=workspace`, and `ai.egzo.service=<workspace>`.

| What | Volume | Mounted in an agent at |
|---|---|---|
| directory workspace `NAME` | `<project>_ws.NAME` | `/workspace/NAME` (`:ro` allowed) |
| git workspace `NAME`, the base clone | `<project>_ws.NAME.base` | `/workspace/NAME/.base` |
| git workspace `NAME`, worktree of instance `I` | `<project>_ws.NAME.wt.I` | `/workspace/NAME/I` |

- The dot cannot appear in a project, workspace or instance name, so a workspace volume never collides with another
  egzo volume (`control`, `ca`, `ca-private`, `egress`, `<agent>-home`), whatever the names.
- The base and the worktree are **sibling mounts**, so the relative links `git worktree add --relative-paths` writes
  (`../.base/.git/worktrees/I`) resolve exactly as they did with directories. No subpath mounts are needed, which keeps
  Podman and older Docker working. An agent still never sees another instance's worktree: it is not mounted.
- A worktree volume also carries `ai.egzo.instance=I`, the actor and the template hash, so removing the instance removes
  it and a left-over one is recognisable. The base and directory volumes carry no instance.
- Ownership: the engine creates volumes owned by root. A prep container hands each to the uid:gid recorded by `up`
  (`Published.User`) when it is created, and again before an agent's container starts (the existing `OwnedVolumes`
  step), because the engine gives a volume to root when it is a container's working directory.

## Who does what

| Step | Today | Now |
|---|---|---|
| `up`: directory workspaces | `mkdir` | create the volume (idempotent) |
| `up`: base clone of a listed git workspace | prep container, bind mount | prep container, base volume |
| `spawn`: worktree | prep container, bind mounts | prep container, base + new worktree volume |
| `spawn`: "is the worktree or the branch already there?" | `os.Stat` and reading `packed-refs` | the worktree volume exists (engine); the branch is asked of the base by `prep git info` |
| `spawn`: free automatic name | `os.Stat` per candidate | one `prep git info` per git workspace lists its `egzo/*` branches |
| `rm`, `down`: delete the worktree | `os.RemoveAll` + forget in the base | remove the worktree volume; `prep git forget` removes the base's entry for it |
| `up`: "the base has another url" warning | read `.git/config` | `prep git info` reports the origin url |
| `workspace ls` | reads directories, no engine | lists volumes, needs the engine |
| prompt file | bind mount of the host file | `up` publishes its text in the template; the agent gets it in `EGZO_PROMPT` |
| project lock | flock on the host | an engine-side lease, below |

The git work was already done in prep containers; only the mounts and the checks around them move.

## The lock

A flock cannot be taken from another machine or container. The lock becomes a **lease held in the engine**: a container
`<project>.lock`, created and never started, from the egzo image. Creating a container with a name is atomic, so one
command wins and the others get a conflict. Labels carry the holder (who, host, pid) and the expiry (30 minutes after
it was taken); a command that finds an expired lock removes it and takes its own. The lock is released by removing the
container, in a `defer`, so Ctrl-C and errors release it. A command killed without cleanup leaves the lease until it
expires, or until `egzo project unlock` removes it (it prints who held it).

- `Lock` and `LockWait` take the engine client and the project; every caller that takes the lock already has them.
- The lock container is not part of `ps`, `up`'s plan or `down`'s removals: it is filtered out of what is observed.
  Its name has a dot, which no project, template or instance name can have, so it never collides with one.
- Why not a network or a volume: a network takes a subnet from a limited pool, and a volume create is idempotent, so it
  cannot decide a race.
- `up` pulls the egzo image before it takes the lock, as the lock needs it.

## `down` and data

`down` keeps its promise: it never deletes what holds the user's work.

- `down` removes the infrastructure, the instances, and their worktree volumes (as it removed worktrees).
- It **keeps** the base volumes and the directory volumes, and says so. There is no flag that deletes them
  (`down --volumes` stays unknown: `down` already removed every volume it owns, and workspaces are not its to remove).
- `egzo workspace rm NAME...` (or `--all`) removes workspace volumes: the directory volume, or for a git workspace its
  base. It refuses while an instance exists that mounts it, or a worktree of it is left;
  `egzo workspace rm NAME --worktree INSTANCE...` removes left-over worktree volumes.
- `up` after `down` finds the volumes by name and reuses them.

## Commands

    egzo workspace ls [--json]                      # the declared workspaces, their volumes, state, and the worktrees
    egzo workspace put NAME SRC [DEST]              # copy a file or a directory of the host into a directory workspace
    egzo workspace get NAME [SRC] DEST              # copy a path of a workspace out to the host
    egzo workspace fetch [NAME...]                  # clone or refresh git bases (unchanged)
    egzo workspace rm NAME... | --all [--worktree INSTANCE...]   # see above
    egzo project down [--force]
    egzo project unlock                             # break the lease of a command that was killed

`put` and `get` use the engine's archive API on a created, never-started container that mounts the volume, so they work
the same against a remote engine. `put` hands the files to the agent user. `get` and `put` refuse a path that leaves
the workspace.

## Schema and errors

- `path` on a workspace: `workspace "docs": path was removed: workspaces are engine volumes now; copy files in with
  `egzo workspace put docs ./docs`` (the unknown-key machinery names the file, line and column as for any key).
- The resolved view (`project check --resolved`) prints a workspace as `{git: ...}` or `{}`, and an agent's mounts
  without `host_path`.
- The names `control`, `ca`, `ca-private`, `egress` and `<agent>-home` stay reserved for workspaces: harmless now, but
  they were reserved for a reason that may return, and relaxing a rule is easier than adding one.
- A template's `prompt:` file is limited to 64 KB (it travels in the template and in an environment variable).

## Not changed

Git over https only; the base is writable by every agent that lists the workspace
(`known-issues/git-base-clone-is-trusted-by-every-agent.md`); `:ro` on a git workspace stays an error; instance names;
exit code 17; the branch `egzo/<instance>` outliving the instance; `--reuse` and `--no-fetch`.

## Costs and risks accepted

- Volumes are not browsable from the host. On rootful Docker they are root-owned under `/var/lib/docker`. The commands
  above are the way in and out.
- A prep container per check (`git info`, `forget`) adds roughly a second to a spawn or an `rm` of a project with a
  git workspace.
- Every `rm`/`down` of an instance whose worktree volume is in use fails cleanly (the engine refuses), as it does for
  a running instance without `--force`.
- Podman: volumes and the archive API are used through the compat socket. Not exercised here (the specs run on Docker).

## Order of work

1. Specs first (they go red): schema, workspace volumes, git workspaces on volumes, lock, prompt, `workspace` commands.
2. config: drop `path`, `HostPath`, path checks; resolved view.
3. stack: volume names and labels, workspace volumes in `up`, `spawn`, `rm`, `down`; prompt in the template; the lease.
4. gitprep + `egzo prep git info|forget`.
5. cli: `workspace ls|put|get|rm`, `down --volumes`, `project unlock`; `init`/`system check` stop touching `.gitignore`.
6. Docs (`workspaces.md`, `project-file.md`, `managing-projects.md`, `security.md`, `intro`, `DESIGN.md`, windows-and-macos),
   and `hub.md` (the "portable workspaces" requirement is now met).
