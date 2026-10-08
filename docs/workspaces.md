# Git workspaces

A git workspace is a repository the agents work in. The short version is in [the project file](project-file.md); for the egress side of cloning see
[sandboxing](sandboxing.md);
this page is how the pieces behave.

```yaml
vaults:
  main: { backend: env, secrets: { GITHUB_TOKEN: { from: env:GITHUB_TOKEN } } }
egress:
  default: { services: { github: main/GITHUB_TOKEN } }
workspaces:
  repo:
    git: { url: https://github.com/acme/shop.git, branch: main }
    mode: clone
```

## Who clones, and with what credential

Agents hold no git credential. When you `spawn`, a short-lived **prep container** (with git 2.47 or later, run as
your uid) makes the checkout, on the new instance's own network, so the clone goes through the proxy and the proxy
adds the token. The `github` service injects it for `github.com`. Pushes work the same way: the agent runs plain
`git push` and the proxy authenticates it. Only HTTPS URLs are accepted. A repository on your disk is not a git
source; mount it as a directory (`./path`).

Without a service for the host, a private repository fails to clone, and a public one needs the host in `allow`.

## The three modes

| mode | what an instance gets | when to use |
|---|---|---|
| `clone` (default) | its own independent clone | the safe default: one agent cannot damage another's `.git` |
| `worktree` | a git worktree of one shared base clone, on its own branch `egzo/<instance>` | many agents on a big repo; faster and smaller |
| `shared` | the same checkout as every other instance that lists it | agents that must see each other's files |

Notes:
- With `worktree`, the base `.git` is mounted writable into every agent that uses it, so one agent could damage
  what the others share. It is an accepted trade, which is why `clone` is the default.
- `clone` and `worktree` checkouts belong to the instance and are named after it; `shared` ones belong to the project.
- To see another agent's work, fetch what it pushed. There is no way to mount another agent's checkout.
- A git workspace cannot be `:ro`; git needs to write. For read-only files, use a host directory (`./docs:ro`).
- Changing `mode` does not convert existing checkouts: `up` reports the mismatch and refuses until you remove them.

## Where the files are

`.egzo/workspaces/<name>/<instance>/` in the project directory (`path:` changes the location). Keep `.egzo/` in
`.gitignore`; `egzo init` adds it and `egzo doctor` warns if it is missing. You can open these directories in your
editor while the agent works. Inside containers a checkout is always `/workspace/<name>`.

## Keeping work safe

`egzo rm` and `egzo down` never delete checkouts, because they hold work that exists nowhere else. To delete them:

```console
$ egzo down --workspaces      # looks in each for uncommitted files and unpushed commits, refuses if it finds any
$ egzo down --workspaces --force --yes
```

See [managing projects](managing-projects.md) for `rm` and `down`. Tell agents in their prompt to commit and push their branch when done; `egzo rm` is safe after that.
