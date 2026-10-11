# Workspaces

A workspace is something the agents work in. It is declared once, at the top of the project file, and belongs to the
project: any number of agents may list it. There are two kinds. The short version is in
[the project file](project-file.md); for the egress side of cloning see [sandboxing](sandboxing.md).

```yaml
vaults:
  main: { backend: env, secrets: [GITHUB_TOKEN] }
egress:
  default: { services: { github: main/GITHUB_TOKEN } }
workspaces:
  shop:                                  # a git repository
    git: { url: https://github.com/acme/shop.git, branch: main }
  docs: {}                               # a directory egzo keeps
agents:
  coder: { harness: claude-code, workspaces: [shop, "docs:ro"] }
```

An agent lists declared names only (`name` or `name:ro`); a path in an agent's list is an error that says to declare
it.

**A workspace lives in an engine volume, never on your machine.** egzo leaves nothing in the project directory except
what you wrote in it, and every command that works on agents (`spawn`, `rm`, `start`, `attach`, `send`, `workspace
...`) needs only the engine: it works the same against a Docker or Podman on another machine, and from a tool that
has no access to your files, such as the hub. The price is that you cannot open an agent's files in your editor as they
change; the commands below copy files in and out, and agents put their work back with `git push`.

```console
$ egzo workspace ls
NAME  KIND       VOLUME                STATE   WORKTREES
docs  directory  myproj_ws.docs        exists
shop  git        myproj_ws.shop.base   cloned  coder-1,coder-2
```

## Directory workspaces

No `git`: one volume, `<project>_ws.<name>`, mounted at `/workspace/<name>` in every agent that lists it, all of them
seeing the same files (`:ro` makes it read-only for an agent). `egzo project up` creates it, empty and owned by the
user the agents run as. egzo never looks inside it and never deletes it on its own.

```console
$ egzo workspace put docs ./notes             # a directory of this machine, into the workspace as notes/
$ egzo workspace put docs ./spec.pdf          # a file, as spec.pdf
$ egzo workspace put docs ./notes .           # the contents of ./notes, into the root of the workspace
$ egzo workspace get docs out ./results       # a path of the workspace, out to this machine
$ egzo workspace get docs ./everything        # all of it
```

`put` and `get` work with the project down, and refuse a path that leaves the workspace. They go through the engine, so
they work on a remote engine too.

## Git workspaces

Two kinds of volume hold the repository, laid out for agents that work side by side:

```
myproj_ws.shop.base            the base clone, made by `egzo project up`, with no files checked out
myproj_ws.shop.wt.coder-1      the worktree of the instance coder-1, made by `egzo agent spawn`
myproj_ws.shop.wt.coder-2
```

Inside an agent they are mounted next to each other, so the links git keeps between a worktree and its base (relative
paths) resolve: it sees its own worktree at `/workspace/shop/coder-1` (its working directory when it lists only that
workspace) and the base at `/workspace/shop/.base`, never another agent's worktree, which is not mounted at all.

- **`egzo project up`** clones the base last, through the proxy, as the egress profile of the first template that lists the
  workspace. A clone that fails (the host is not allowed, the repository is private and no service has its token) is
  a warning, not an error: `up` finishes, and `agent spawn` or `egzo workspace fetch` tries again. A base that is already
  there is left as it is, with a warning when its url is not the one in the file.
- **`egzo agent spawn`** fetches the configured branch into the base through the proxy, as the new instance, and makes the
  worktree on a branch of its own, `egzo/<instance>`, starting at what was fetched. A fetch that fails only warns and
  the worktree starts from the base as it is; `--no-fetch` skips it. A base that is missing is cloned first.
- **Credentials.** Agents hold none. The `github` service injects the token for `github.com`, so `git push` works
  from inside the agent. Only HTTPS URLs are accepted. A repository on your disk is not a git source: copy it into a
  directory workspace with `egzo workspace put`.
- **Looking at an agent's files.** `egzo workspace get shop/coder-1 src ./coder-src` copies a path of that instance's
  worktree out. What an agent should keep, it commits and pushes.
- **Removing** an instance (`egzo agent rm`, `egzo project down`) deletes its worktree volume, with no check. What the
  agent committed stays on its branch in the base clone, `egzo/<instance>`. Tell agents to commit and push when they are
  done.
- **Spawning a name again** fails while its worktree volume or its branch is still there. `egzo agent spawn --reuse`
  continues with them (the branch is checked out again in a fresh worktree). A name egzo picks itself
  (`coder-2`) skips the names that left a worktree volume or a branch behind.
- A git workspace cannot be `:ro`; git needs to write. For read-only files, use a directory workspace.

## What removes what

| | directory volume | base clone | worktree volume |
|---|---|---|---|
| `egzo agent rm` | kept | kept | removed |
| `egzo project down` | kept | kept | removed |
| `egzo workspace rm NAME` | removed | removed | refused while one is left |
| `egzo workspace rm NAME --worktree INSTANCE` | | | removed |
| `egzo workspace rm --all` | removed | removed | removed (if no instance uses it) |

`down` says which volumes it keeps. `egzo project up` after a `down` finds them again, so a clone is not made twice. All
of these refuse a volume that an instance still mounts.

## Moving an existing project

Workspaces used to be directories on your machine (`path:`, `./workspaces/<name>`). `path:` is gone, with an error that
says so. Copy what you need into the new volumes (`egzo workspace put NAME ./the-directory .`); a git workspace is
cloned again by `egzo project up`, and the branches `egzo/<instance>` your old base held can be pushed from it
before you delete it. The `workspaces/` and `.egzo/` entries `egzo project init` added to `.gitignore` can go.
