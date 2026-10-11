# The base clone of a git workspace is trusted by every agent that lists it

Status: **accepted** (written 2026-10-11). Mitigation possible, not built.

A git workspace is one base clone (`workspaces/<name>/.base`) and one worktree per agent. A worktree is private to its
agent, but it is a view of the base: its `.git` is a file that points into the base, so every agent that lists the
workspace has the base's `.git` mounted **read-write** (`/workspace/<name>/.base`). Git needs that to commit, create
branches and move the index of a worktree.

What one agent can do to the others through it:

- **Damage shared history.** Delete or rewrite the branches `egzo/<other agent>`, the refs, or the object store. The
  work an agent committed lives on its branch in the base, so this is also how committed work can be lost.
- **Plant a git setting that runs code later.** egzo runs git with `core.hooksPath=/dev/null` and
  `core.fsmonitor=false`, so a hook or a file-system monitor in the base never runs. It cannot disable every setting
  the repository's own `.git/config` or `.git/info/attributes` can carry: a `filter.<name>.smudge` or `.process`
  command plus an attribute line that selects it, `core.sshCommand`, `credential.helper`, `url.<base>.insteadOf`. Git
  uses the base's config when egzo makes the next worktree (`git fetch` and `git worktree add`, which checks files
  out), and then the planted command runs:
  - in the prep container of the **next spawn**, which is on that instance's network, with its proxy token and its
    egress profile: agent A's code gets agent B's identity and egress for as long as the job runs;
  - on **your host**, as you, when you run git in a worktree from your editor or shell, because the worktree's links
    are relative and resolve to the same base on the host.

The code that runs is code an agent wrote, so the issue is about agents that do not trust each other, or an agent
that has been steered by a prompt injection and shares a workspace with agents holding more access.

## Why it is accepted

- Sharing the base is what makes worktrees cheap (one object store, one fetch) and keeps the relative links working
  in every container and on the host. A private clone per agent costs the disk and the time that worktrees save.
- Agents that list the same git workspace work on the same repository, for the same person: they already share
  what they push.
- The proxy decides what an identity can reach, so the worst case is bounded by the egress profiles of the agents
  that share the workspace, not by anything outside the project.

Until it is mitigated, list a git workspace only for agents that you trust with each other's work and access, and
give agents that need more access (a deployment token) a workspace of their own.

## Possible mitigations

- **Validate the base before egzo runs git in it**: read `.git/config` against an allowlist (the standard `core.*`,
  `remote.origin.*`, `branch.*` keys; no `include`, `filter`, `sshCommand`, `credential`, `url`, `fsmonitor`,
  `hooksPath`) and empty `info/attributes`, and refuse the spawn with the offending key named. This protects the
  prep container and the next spawn; it does not protect the host, which runs the user's own git.
- **Run egzo's git with a config the base cannot change**: `GIT_CONFIG_GLOBAL`/`GIT_CONFIG_SYSTEM` pointing at an
  egzo file, `-c` for the dangerous keys, and `GIT_ATTR_NOSYSTEM`; the repository's own config still applies, so this
  only narrows the list above.
- **Mount the base read-only and give each worktree its own object store** (`git worktree` with `objects/info/alternates`
  pointing at a read-only base, or `git clone --local` per agent, which hardlinks objects). Agents would then write
  only their own `.git`. It changes the layout, costs disk, and the relative links no longer resolve to one base.
- **Keep the base out of the agent's reach and fetch for it**: the agent sees a worktree whose `.git` is a private
  directory, and egzo moves refs between it and the base. A larger design, and it makes `git push` from the agent
  depend on egzo.

The host half needs the user's git to ignore the base's config, which egzo cannot arrange; a warning from
`egzo project check` when the base's config carries one of the dangerous keys is the realistic part of it.
