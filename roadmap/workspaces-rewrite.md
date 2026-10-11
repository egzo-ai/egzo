# Roadmap: workspaces rewrite

Today a workspace has a `mode` (`clone`, `worktree`, `shared`), sources of three kinds (git, host path, no source =
engine volume), hidden paths under `.egzo/workspaces/`, a checkout registry, and an unsaved-work scan on `rm` and
`down`. This replaces all of it with one small model. The CLI side (`workspace`, `agent rm`, `project down`) is in
[cli-refactor.md](cli-refactor.md); do this plan first, the CLI plan builds on it.

## The model

Top-level `workspaces:` are **project-scoped and shared**. Any number of agent templates may list one. An agent lists
only declared names; inline paths (`./docs:ro` in an agent) go away. There are two kinds, both on the host, both
visible in the project directory. Engine volumes are not used for workspaces.

```yaml
workspaces:
  foo: {}                                   # = { path: ./workspaces/foo }, filled in by the resolver
  docs: { path: ./docs }                    # a directory of yours (relative to the project, or absolute)
  shop:                                     # git
    git: { url: https://github.com/acme/shop.git, branch: main }   # path defaults to ./workspaces/shop
agents:
  coder: { workspaces: [shop, docs:ro, foo] }
```

| | directory (no `git`) | git (`git:`) |
|---|---|---|
| `path` | the directory itself; default `./workspaces/<name>` | the directory that holds the repo; default `./workspaces/<name>` |
| created by | `up` or `spawn`/`start`, empty, if missing (as the user) | `up` clones the base, last, non-fatal on failure |
| per agent | nothing; every agent sees the same directory | its own worktree |
| deleted by | nothing, ever | `agent rm` deletes the agent's worktree; `project down` deletes nothing |
| `:ro` | allowed | rejected (git has to write) |

The resolver fills in the missing `path`, so `project check --resolved` shows it.

### Git layout

```
workspaces/shop/.base          the base clone, made by `up` with --no-checkout
workspaces/shop/coder-1        one worktree per instance
```

In the container the names are kept; only the base and the agent's own worktree are mounted:

```
/workspace/shop/.base
/workspace/shop/coder-1        the agent's working directory
```

- Worktree links are **relative** (`worktree.useRelativePaths`, Git 2.48 or later): `../.base/...` in the worktree,
  `../../../coder-1/.git` in the base. They resolve the same on the host and in the container, so `git status` works
  in the editor on the host as well. Nothing is remapped inside the container, so there is no back-link to repair and
  no `git worktree lock`.
- Other agents' worktrees are not mounted. The base is writable (an accepted trade, as today).
- **Branch:** `spawn` creates `egzo/<instance>` from the freshly fetched `branch` (`git worktree add -b`). The base is
  cloned `--no-checkout`, so no branch is checked out in it and nothing clashes. `agent rm` removes the worktree and
  leaves the branch, so committed work stays in the base. `spawn` fails when the worktree directory or the branch
  already exists; `--reuse` continues with them.
- **Fetch:** `spawn` fetches `url`/`branch` first, through the proxy, using the declared `url` and not the base's
  configured remote (a base that is a user's own clone may have an SSH remote the proxy cannot serve). A failed fetch
  warns and continues from the base as it is; a missing base is cloned, and if that fails the spawn fails.
  `spawn --no-fetch` skips it.
- **Names:** instance names cannot start with a dot (so none is `.base`); check that this is already enforced.
- `egzo init` adds `workspaces/` to `.gitignore`.

## What is removed

- `mode` (`clone`, `worktree`, `shared`), the `shared` checkout, the clone mode, "existing checkouts block a mode
  change", and `.egzo/workspaces/`.
- Workspaces without a source as engine volumes (`scratch: {}`). A plain workspace is a directory.
- The `.egzo-checkouts` registry, and the unsaved-work scan (uncommitted files, unpushed commits) with its
  `--force`/`--yes` handling, in `internal/stack/git.go`, `internal/gitprep` and the CLI.
- `down --workspaces`, `down --volumes` for workspaces, `rm --workspaces`.
- The workaround that hands root-owned volumes to the agent's user, for workspaces (home volumes keep it).
- Host-path mounts written inline in an agent's `workspaces:`.

The home volume of an agent stays an engine volume, deleted by `agent rm`.

## Steps

1. **Spike, in the scratchpad.** Check with the real prep image, on Docker and Podman:
   - `git clone --no-checkout`, then `git worktree add -b egzo/<i> <dir> <ref>` with `worktree.useRelativePaths`
     gives the links above, and git inside the container (status, commit, `worktree list`, `gc`) and on the host
     both work at the mirrored paths;
   - a host git older than 2.48 against such a repo: what exactly breaks (it should refuse the unknown
     `relativeWorktrees` extension), so `system check` can say so;
   - the harnesses' assumption of `/workspace/<name>` as the working directory (`workdir:` in the agent) and what
     changes when it is `/workspace/<name>/<instance>`.
2. **DESIGN.md and docs first:** DESIGN.md (the workspace sections, "Workspaces outlive agents", the label kinds,
   uid handling), `docs/workspaces.md`, `docs/project-file.md`, `docs/managing-projects.md`, `docs/sandboxing.md`
   (cloning goes through the proxy during `up` and `spawn`), `docs/intro.md`.
3. **Specs next** (they fail until the code lands): rewrite `specs/test_git_workspaces.py` and
   `specs/test_workspaces.py`; touch `test_up_down.py`, `test_instances.py`, `test_spawn.py`, `test_operations.py`,
   `test_schema.py`, `test_init.py`, `test_labels.py` where they name modes, volumes, `rm --workspaces` or
   `down --workspaces`. New specs:
   - the resolver fills `path`; two kinds only; `mode` and `scratch` rejected with a message that says what replaces
     them;
   - `up` clones the base last and a failure is non-fatal; a second `up` leaves it alone; a base that does not match
     the file (url, branch) prints a non-fatal error;
   - `spawn` fetches, creates the worktree and the branch; fails on an existing directory or branch; `--reuse`;
     `--no-fetch`; a failed fetch warns and continues;
   - container layout (`/workspace/<name>/.base`, `/workspace/<name>/<instance>`), relative links, git works on both
     sides, other agents' worktrees are not visible;
   - `agent rm` deletes the worktree and the home volume and keeps the branch; `project down` deletes nothing under
     `workspaces/`; a plain directory is never deleted; a missing one is created empty, owned by the user;
   - `:ro` on a git workspace is rejected.
4. **Implement**, in this order: config types and resolver (`path`, remove `mode`), the base clone in `up`, the
   worktree in `spawn`, the mounts and `workdir`, `rm` and `down`, then delete the dead code of the list above.
5. **`system check`** reports git in the prep image and on the host (see cli-refactor.md).

## Open points

- The working directory moves to `/workspace/<name>/<instance>`; the harness code and the images may assume the old
  path (step 1).
- A failed clone leaves the project up and the workspace unusable until `egzo workspace fetch` succeeds; `spawn` of
  an agent that lists it should fail with that hint.
- Concurrent spawns of the same git workspace share one base: they already run under the project lock, which has to
  cover the fetch.
- Changing `url` or `branch` of an existing git workspace leaves the old base in place (only a message); there is no
  command that deletes a base, the user removes the directory.
