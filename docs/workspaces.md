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
  docs: { path: ./docs }                 # a directory of yours
  scratch: {}                            # a directory egzo keeps: ./workspaces/scratch
agents:
  coder: { harness: claude-code, workspaces: [shop, "docs:ro", scratch] }
```

An agent lists declared names only (`name` or `name:ro`); a path in an agent's list is an error that says to declare
it. Every workspace has a `path`, resolved against the project directory (`egzo project check --resolved` prints it); without one it is
`./workspaces/<name>`. Everything is a plain directory on the host, in the project, so you can open it in your editor.
Keep `workspaces/` out of git: `egzo project init` adds it to `.gitignore` and `egzo system check` warns if it is missing.

## Directory workspaces

No `git`: the directory at `path` is mounted at `/workspace/<name>` in every agent that lists it, all of them seeing
the same files. `egzo project up` and `egzo agent spawn` create it, empty, when it is missing. egzo never deletes it, and never looks inside:
it is yours (`:ro` makes it read-only for an agent).

## Git workspaces

`path` holds the repository, laid out for agents that work side by side:

```
workspaces/shop/.base          the base clone, made by `egzo project up`, with no files checked out
workspaces/shop/coder-1        a worktree for the instance coder-1, made by `egzo agent spawn`
workspaces/shop/coder-2
```

Inside an agent the names are kept: it sees its own worktree at `/workspace/shop/coder-1` (its working directory when
it lists only that workspace) and the base at `/workspace/shop/.base`, never another agent's worktree. The links git
keeps between a worktree and its base are relative, so `git status` works on the host in `workspaces/shop/coder-1` too
(git 2.48 or later on the host).

- **`egzo project up`** clones the base last, through the proxy, as the egress profile of the first template that lists the
  workspace. A clone that fails (the host is not allowed, the repository is private and no service has its token) is
  a warning, not an error: `up` finishes, and `agent spawn` tries again. A base that is already there is left as it is, with
  a warning when its url is not the one in the file.
- **`egzo agent spawn`** fetches the configured branch into the base through the proxy, as the new instance, and makes the
  worktree on a branch of its own, `egzo/<instance>`, starting at what was fetched. A fetch that fails only warns and
  the worktree starts from the base as it is; `--no-fetch` skips it. A base that is missing is cloned first.
- **Credentials.** Agents hold none. The `github` service injects the token for `github.com`, so `git push` works
  from inside the agent. Only HTTPS URLs are accepted. A repository on your disk is not a git source: use a directory
  workspace.
- **Removing** an instance (`egzo agent rm`, `egzo project down`) deletes its worktree, with no check. What the agent committed
  stays on its branch in the base clone, `egzo/<instance>`. Tell agents to commit and push when they are done. The
  base clone is never deleted: remove `workspaces/<name>` yourself.
- **Spawning a name again** fails while its worktree directory or its branch is still there. `egzo agent spawn --reuse`
  continues with them (the branch is checked out again in a fresh worktree). A name egzo picks itself
  (`coder-2`) skips the names that left a worktree or a branch behind.
- A git workspace cannot be `:ro`; git needs to write. For read-only files, use a directory workspace.
