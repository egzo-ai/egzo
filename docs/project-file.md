# The project file

A project is one `egzo.yaml`. Everything is denied unless the file declares it, so the file reads as a list of what
agents may use. It has five parts: **vaults** (where secrets come from), **egress** (what agents may reach),
**workspaces** (what they work on), **agents** (the templates) and, optionally, **proxy**/**name**.

`egzo config` prints the file validated and fully resolved; `egzo config` is the quickest way to see what a short
file expands to. The project name defaults to the directory name (`name:`, `-p` or `EGZO_PROJECT_NAME` override it);
renaming a project creates a new one.

## The smallest useful file

```yaml
vaults:
  main:
    backend: env
    secrets:
      ANTHROPIC_API_KEY: { from: env:ANTHROPIC_API_KEY }

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

`egzo up`, then `egzo spawn claude --attach` ([managing projects](managing-projects.md)). With one workspace, its directory is the agent's working directory.

## Vaults and secrets

A vault names secrets and where each is read from, at `up` time, by the CLI. Two sources work today:

```yaml
vaults:
  main:
    backend: env
    secrets:
      ANTHROPIC_API_KEY: { from: env:ANTHROPIC_API_KEY }   # read from your shell
      GITHUB_TOKEN:      { from: file:~/.secrets/gh }      # read from a file
```

`egzo secrets ls` shows which are set (never the values); `egzo secrets set vault/NAME` stores a file-backed one.
Secrets reach the proxy only ([security model](security.md)). They are never in an agent's environment, in labels or in `docker inspect`.

## Egress profiles

A profile lists the hosts an agent may reach and the secrets injected into them. An agent uses the profile named by
its `egress:` key, or `default` if it names none. Anything not listed is denied. See [sandboxing
agents](sandboxing.md).

## Workspaces

Declared once, listed by name in agents. They appear at `/workspace/<name>`. [Git workspaces](workspaces.md) covers the
modes, credentials and cleanup.

```yaml
workspaces:
  repo:
    git: { url: https://github.com/acme/shop.git, branch: main }
    mode: worktree        # clone (default) | worktree | shared
  scratch: {}             # no source: a volume shared by every agent that lists it

agents:
  coder:
    harness: claude-code
    workspaces: [repo, scratch, ./docs:ro]   # two or more: the working directory is /workspace
```

- `clone` (default): each instance gets its own independent clone, so one agent cannot corrupt another's `.git`.
- `worktree`: each instance gets a git worktree of one shared base clone. Faster, and the base `.git` is writable by
  all of them.
- `shared`: one checkout used by every instance that lists it.
- `./path` or `/abs/path`, with optional `:ro`, mounts a host directory. Nothing from the host is mounted unless the
  file says so. A git workspace cannot be `:ro`.
- Git sources are HTTPS only. The clone goes through the proxy, which injects the token (the `github` service), so
  agents hold no git credential and push their own work.
- Checkouts live in `.egzo/workspaces/<name>` (`path:` changes it). `egzo down` never deletes them;
  `egzo down --workspaces` does, after checking each for unpushed work. Keep `.egzo/` out of git.

## Agents

An agent entry is a **template**; nothing runs until `egzo spawn`. See [harnesses](harnesses.md) for `harness:`,
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
