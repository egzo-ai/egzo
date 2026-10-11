# Roadmap: simplify the CLI

There are too many top-level commands, they are not grouped, workspaces have no tooling, and `down` carries both
`--volumes` and `--workspaces`. This regroups them under a few nouns. egzo is pre-1.0: **no aliases for the old
names**, no compatibility. The workspace model it relies on is in [workspaces-rewrite.md](workspaces-rewrite.md);
do that plan first (`agent rm`, `project down` and `workspace` depend on it).

## Shape

```
egzo project   init | up | down | check | rotate-ca
egzo agent     spawn | ls | rm | start | stop | attach
egzo message   send | ls | reply            (alias: msg)
egzo workspace ls | fetch
egzo system    check
top level      ps, logs, start, stop, restart, exec, events, proxy, version, completion
```

## Mapping

| Current | New | Behavior change |
|---|---|---|
| `init` | `project init` | Also adds `workspaces/` to `.gitignore`. |
| `up` | `project up` | Also clones the git workspaces, last, non-fatal on failure. Still starts no agent. |
| `up --dry-run`, `--recreate` | `project up --dry-run`, `--recreate` | `--dry-run` takes over `diff` (exit 1 when something would change). |
| `diff` | removed | `project up --dry-run`. |
| `down` | `project down` | **Blocks while agents run**; `--force` removes them first (container, network, home volume, worktrees). Deletes the worktrees of the agents it removes, nothing else on the host. No `--volumes`, `--workspaces`, `--yes`. |
| `config` | `project check --resolved` | Merged. |
| `secrets ls` | `project check` | Merged, no command of its own. |
| `doctor` | `system check` | Engine and isolation, plus git (below). Needs no project file. |
| `ca rotate` | `project rotate-ca` | None. |
| `spawn T [NAME]` | `agent spawn T [NAME]` | Fetches, makes the worktree and branch. `--reuse` continues with an existing worktree and branch; `--no-fetch`. Creates missing directory workspaces. |
| `attach` | `agent attach` | None. |
| `rm NAME...` | `agent rm NAME...` | Also deletes the agent's worktrees, with no check. No `--workspaces`, no `--yes`. Keeps `--force` for a running agent. |
| `prune --stale/--stopped/--older-than` | `agent rm --stale/--stopped/--older-than` | Merged; with no names at least one filter is required; `--dry-run`, `--yes` and `--json` stay. |
| (none) | `agent ls` | New: agents only, the columns of `ps`; `--json`. |
| (none) | `agent start`, `agent stop` | Aliases of `start` and `stop`; `start` also creates missing directory workspaces. |
| `send INSTANCE MSG...` | `message send AGENT MSG...` | None (`--wait`, `--interrupt`, `--timeout`, exit codes 0/3/4/5). |
| `messages` | `message ls` | Filters replace `--agent` and `--all`: `--kind question\|request\|reply\|update`, `--state pending\|resolved`, `--to X`, `--from X`. |
| `questions` | `message ls --kind question --state pending` | Merged. |
| `answer ID TEXT...` | `message reply ID TEXT...` | None (`--outcome`). |
| (none) | `workspace ls` | New: each workspace, its resolved path, kind, and for git the base, branch, head and the agents with a worktree. Reads directories only, so it works with the project down. |
| (none) | `workspace fetch [NAME...]` | New: refreshes git bases, and clones a missing one (the retry for a failed clone at `up`). Needs the project up for the proxy's credentials. |
| `down --workspaces`, `rm --workspaces` | removed | Gone, with the unsaved-work scan. |
| `ps`, `logs`, `start`, `stop`, `restart`, `exec`, `events` | unchanged | Top level; they take an agent or a sidecar. |
| `proxy log`, `proxy rules` | unchanged | Top level. |
| `version`, `completion`, `help` | unchanged | None. |
| hidden `control`, `prep`, `prepgit`, `ops` | unchanged | Internal, run inside containers. |

## Behaviors

- **`project check`** exits 1 on any error and 0 otherwise. Errors: an invalid `egzo.yaml`; a secret that cannot be
  read (missing); an empty value; a missing key. Drift (what `up` would change) is **not** an error for now: it is
  shown, not counted. It never prints a secret value. `--resolved` prints the configuration with defaults filled in
  (for example every workspace `path`).
- **`system check`** needs no project file. It reports the engine and its isolation, as `doctor` does, and git:
  - git in the prep image, and whether it is 2.48 or later (relative worktree links);
  - git on the host, with a warning (not an error) below 2.48, because an older git refuses a repository that uses
    relative worktrees, so host-side `git status` would fail. Host-side git is desirable, not essential.
- **`project down`** exits non-zero with the list of running agents and removes nothing when there are any.
  `--force` removes them first, the way `agent rm` does, then the sidecars, networks and the project's own volumes
  (control, CA). Home volumes go with their agent. It never deletes anything under a workspace `path`.
- **`agent rm`** deletes the agent's container, network, home volume and worktrees, and closes what was asked of it as
  failed, as `rm` does today. The branch `egzo/<instance>` stays in the base.
- **`message ls`** states: `pending` is everything not resolved (queued, announced, fetched, unconfirmed),
  `resolved` the rest; the finer state is a column. `--to` and `--from` take an agent name or `operator`.

## Steps

1. **Docs and DESIGN.md first:** `docs/managing-projects.md`, `docs/messaging.md`, `docs/project-file.md`,
   `docs/intro.md`, `docs/install.md`, `docs/troubleshooting.md`, `docs/security.md`, `README.md`, DESIGN.md (the CLI
   sections and its command list), every error message and hint that names a command (`egzo up`, `egzo doctor`,
   `down --workspaces`, `egzo spawn`, `egzo answer`, the hint in `internal/stack/git.go`).
2. **Specs next** (red until implemented): all specs invoke the CLI through `specs/support.py`, so the command names
   change in one place plus the specs that assert on help text or hints. New specs:
   - `project check`: exit codes for each error, drift is not an error, no secret value ever printed;
   - `system check` with no project file, and its git findings;
   - `project down` blocks with running agents and `--force` removes them and their home volumes;
   - `agent rm` filters, `agent ls`, `agent start`/`stop` aliases;
   - `message ls` filters, `message reply`, the `msg` alias;
   - removed commands fail as unknown commands (`up`, `spawn`, `rm`, `prune`, `diff`, `doctor`, `config`, `secrets`,
     `ca`, `send`, `messages`, `questions`, `answer`).
3. **Implement**, keeping each step green on `go build` and the unit tests:
   1. the command tree (`internal/cli/root.go` and the new group files), moving the existing command bodies;
   2. merge `rm` and `prune`, `messages` and `questions`;
   3. `project check` (folding in `config`, `secrets` and the drift part of `diff`) and `system check`;
   4. `project down` blocking and `--force`;
   5. `workspace ls` and `workspace fetch` (after the workspaces rewrite);
   6. delete the removed commands and their flags.
4. Run the full suite once on `docker-gvisor` (about 20 minutes on this host) before tagging.

## Open points

- The names `project`, `agent`, `message` stay as long nouns while `spawn` and `attach` are used all day; if that
  hurts, add top-level shortcuts later, not now.
- `start`, `stop`, `restart`, `logs`, `exec` and `events` take a service name that is a sidecar or an agent; the
  names must stay unambiguous (an agent cannot be called `proxy` or `control`; check it is already refused).
- Whether `project check` should have a flag that makes drift an error (for CI) is deferred.
