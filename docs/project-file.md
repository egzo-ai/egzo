# The project file

A project is one `egzo.yaml`. Everything is denied unless the file declares it, so the file reads as a list of what
agents may use. It has five parts: **vaults** (where secrets come from), **egress** (what agents may reach),
**workspaces** (what they work on), **agents** (the templates) and, optionally, **proxy**/**name**.

`egzo project check --resolved` prints the file validated and fully resolved; `egzo project check --resolved` is the quickest way to see what a short
file expands to. The project name defaults to the directory name (`name:`, `-p` or `EGZO_PROJECT_NAME` override it);
renaming a project creates a new one.

## The smallest useful file

```yaml
vaults:
  main:
    backend: env
    secrets: [ANTHROPIC_API_KEY]

egress:
  default:
    allow: [platform.claude.com]
    services:
      anthropic: main/ANTHROPIC_API_KEY

workspaces:
  repo:
    git: { url: https://github.com/me/repo.git }

agents:
  claude:
    harness: claude-code
    workspaces: [repo]
```

`egzo project up`, then `egzo agent spawn claude --attach` ([managing projects](managing-projects.md)). With one workspace, its directory is the agent's working directory (for a git workspace, the agent's own worktree).

## Vaults and secrets

A vault is one secret store: a `backend` and the names of the secrets to read from it. The CLI reads them on your
machine at `egzo project up` time. There are two backends.

**`pass`** ([the standard unix password manager](https://www.passwordstore.org/)) is the one to use. egzo runs `pass show <name>`, so
unlocking is whatever your `pass` and gpg-agent already do, and the first line of the entry is the secret. Names
are the entry names, slashes included:

```yaml
vaults:
  main:
    backend: pass
    secrets:
      - anthropic/api-key
      - company/project/deploy-token
```

A secret is referred to as `<vault>/<name>`: `main/anthropic/api-key`, `main/company/project/deploy-token`. The
project file cannot configure pass. To use another store, set `PASSWORD_STORE_DIR` (or `GNUPGHOME`, ...) in your shell,
as you would for pass itself. egzo only reads secrets: create and change them with `pass insert` (or whatever manages
your store).

**`env`** reads the environment variable of the same name from your shell. It works, and egzo warns each time
(`Using ENV var based secret backend is not recommended.`): any process of yours can read an environment
variable, and it tends to end up in shell history and dotfiles. It is fine for a quick try.

```yaml
vaults:
  main:
    backend: env
    secrets: [ANTHROPIC_API_KEY, GITHUB_TOKEN]
```

`egzo project check` shows which are set (never the values). Secrets reach the proxy only ([security
model](security.md)). They are never in an agent's environment, in labels or in `docker inspect`.

## Egress profiles

A profile lists the hosts an agent may reach and the secrets injected into them. An agent uses the profile named by
its `egress:` key, or `default` if it names none. Anything not listed is denied. See [sandboxing
agents](sandboxing.md).

## Workspaces

Declared once, project-wide, listed by name in agents. They appear at `/workspace/<name>`. [Workspaces](workspaces.md)
covers volumes, worktrees, credentials and cleanup.

```yaml
workspaces:
  repo:
    git: { url: https://github.com/acme/shop.git, branch: main }   # a git repository: each agent gets a worktree
  docs: {}                     # a directory egzo keeps

agents:
  coder:
    harness: claude-code
    workspaces: [repo, "docs:ro"]   # two or more: the working directory is /workspace
```

- Every workspace is an engine volume: nothing of it is on your machine, and there is no `path:`. Files go in and out
  with `egzo workspace put` and `get`.
- A **directory** workspace is shared by every agent that lists it. `egzo project up` creates it, empty, and egzo never
  deletes it on its own. `:ro` makes it read-only for an agent.
- A **git** workspace holds a base clone made by `egzo project up` and a worktree per instance, made by `egzo agent spawn`
  on its own branch `egzo/<instance>`. It cannot be `:ro`. `egzo agent rm` deletes the instance's worktree; what it
  committed stays on its branch in the base.
- Git sources are HTTPS only. The clone and the fetch go through the proxy, which injects the token (the `github`
  service), so agents hold no git credential and push their own work.
- egzo keeps nothing in the project directory, so there is nothing to add to `.gitignore`.

## Agents

An agent entry is a **template**; nothing runs until `egzo agent spawn`. See [harnesses](harnesses.md) for `harness:`,
[overriding base images](images.md) for `image:`, and [messaging](messaging.md) for the `inject:` timings.

```yaml
agents:
  coder:
    harness: claude-code          # claude-code | opencode | custom (see harnesses.md)
    image: registry.example/my-claude:1   # optional, see images.md
    workspaces: [repo]
    egress: default               # profile; omitted => default
    model: claude-sonnet-5-5
    prompt: ./prompts/coder.md    # the agent's standing instructions
    resources: { cpus: 2, memory: 4g, pids: 4096 }
    runtime: runsc                # gVisor, if registered with the engine
    permissions: bypass           # bypass (default) | default (the harness asks)
    env: { LOG_LEVEL: debug }     # plain settings; secret-looking values are rejected
```

## One file, several harnesses

```yaml
agents:
  coder:
    harness: claude-code
    workspaces: [repo]
    prompt: ./prompts/coder.md
  reviewer:
    harness: opencode             # compare frameworks side by side on the same repo
    workspaces: [repo]
    prompt: ./prompts/reviewer.md
```

The complete example (three agents, one repo, one profile) is in the [README](../README.md).
