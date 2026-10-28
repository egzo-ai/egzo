# The project file

A project is one `egzo.yaml`. Everything is denied unless the file declares it, so the file reads as a list of what
agents may use. It has six parts: **secret_key** (what egzo seals its own secrets with), **vaults** (where secrets come
from), **egress** (what agents may reach), **workspaces** (what they work on), **agents** (the templates) and,
optionally, **proxy**/**name**.

`egzo project check --resolved` prints the file validated and fully resolved: the quickest way to see what a short
file expands to. The project name defaults to the directory name (`name:`, `-p` or `EGZO_PROJECT_NAME` override it);
renaming a project creates a new one. A name is 1 to 63 lowercase letters, digits, `-` and `_`, starting with a letter or
digit, like every name in the file.

## The smallest useful file

```yaml
secret_key: main/EGZO_PROJECT_SECRET_KEY

vaults:
  main:
    backend: env

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

## The secret key

egzo keeps secrets of its own: the private key of the project CA, which every agent trusts, and the ssh identity of the
proxy. They live in a volume only the proxy mounts, sealed with a key derived from the project's **secret key**, which
you keep in a vault like any secret. A copy or a backup of the volume is useless without it.

```yaml
secret_key: main/egzo/shop-key

vaults:
  main: { backend: pass }
```

```console
$ openssl rand -hex 32 | pass insert --multiline egzo/shop-key
```

- **Required.** A file without it is refused, and the error gives the command above for the file's own vault. `egzo
  project init` writes the line, makes a key and adds it to [`.env`](#env).
- **At least 32 bytes**, opaque: egzo does not parse it. `egzo project check` reads it like the other secrets.
- **One per project, or shared**: the keys egzo uses are derived from it and the project's name, so one secret can serve
  several projects without them sharing a key.
- It reaches the proxy with the egress policy, at `egzo project up` and `egzo restart proxy`, and lives in its memory only:
  a proxy restarted any other way opens nothing until the next `up`.
- **Keep it.** egzo cannot tell a wrong key from a right one until it opens what was sealed: with another key, `egzo
  project up` stops and says so. Give the old key back, or run `egzo project rotate-ca`, which makes a new CA sealed with
  the key the file names now and restarts the agents.
- A project made by an egzo from before the secret key is refused by `up`: run `egzo project down`, then `egzo project up`, for a
  new CA (the workspaces are kept).

## Vaults and secrets

A vault is one secret store: a name and a `backend`. A secret is declared where it is used, by its reference
`<vault>/<name>` (a service's secret, an ssh server's key): nothing lists it a second time. The CLI reads the
secrets on your machine at `egzo project up` time. `egzo project check --resolved` lists, under each vault, every
secret the file refers to; a vault nothing refers to is a warning. There are two backends.

### pass

**`pass`** ([the standard unix password manager](https://www.passwordstore.org/)) is the one to use. egzo runs `pass show <name>`, so
unlocking is whatever your `pass` and gpg-agent already do, and the first line of the entry is the secret. Names
are the entry names, slashes included:

```yaml
vaults:
  main:
    backend: pass

egress:
  default:
    services:
      anthropic: main/anthropic/api-key          # the entry anthropic/api-key
      github: main/company/project/deploy-token  # the entry company/project/deploy-token
```

The reference splits at the first `/`: the vault is `main`, the rest is the entry name. The project file cannot
configure pass. To use another store, set `PASSWORD_STORE_DIR` (or `GNUPGHOME`, ...) in your shell,
as you would for pass itself. egzo only reads secrets: create and change them with `pass insert` (or whatever manages
your store).

### env

**`env`** reads the environment variable of the same name from your shell or, as Docker Compose does, from the
`.env` file next to `egzo.yaml`. It works, and egzo warns each time (`Using ENV var based secret backend is not
recommended.`): any process of yours can read an environment variable or a file in clear, and they tend to end up in
shell history, dotfiles and backups. It is fine for a quick try.

```yaml
vaults:
  main:
    backend: env

egress:
  default:
    services:
      anthropic: main/ANTHROPIC_API_KEY   # the variable ANTHROPIC_API_KEY
```

`.env` follows Compose's rules:
- **The shell wins.** A variable of your environment is used even when `.env` has it too, and one set to nothing hides
  the file's (`project check` says so).
- **Compose's syntax**, read with Compose's own parser: `KEY=value`, `export KEY=value`, `#` comments, single quotes
  taken literally, double quotes with escapes, and `${VAR}` filled from your environment, then from the lines above.
- **Only the project's.** The `.env` in the directory of `egzo.yaml`, wherever you run egzo from. No file is no
  variable. A file that cannot be parsed fails the secret it was needed for, with its line, never a value.

egzo never copies `.env` anywhere: it is not in a workspace unless you put it there. Keep it out of git.

`egzo project check` shows, for every secret the file refers to, whether it is set (never the value), and says
when it came from `.env`. Secrets reach the proxy only ([security
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
- egzo keeps nothing in the project directory. `egzo project init` puts the secret key in `.env` and, in a git
  repository, adds `.env` to `.gitignore` unless git ignores it already. It writes no secret to a `.env` git tracks.

## Agents

An agent entry is a **template**; nothing runs until `egzo agent spawn`. See [harnesses](harnesses.md) for `harness:`,
[overriding base images](images.md) for `image:`, and [messaging](messaging.md) for the `inject:` timings.

```yaml
agents:
  coder:
    harness: claude-code          # claude-code | opencode | custom (see harnesses.md)
    image: registry.example/my-claude:1   # optional, see images.md
    # build: ./agent                      # or let `up` build it, instead of image: (images.md)
    workspaces: [repo]
    egress: default               # profile; omitted => default
    model: claude-sonnet-5-5
    prompt: ./prompts/coder.md    # the agent's standing instructions
    resources: { cpus: 2, memory: 4g, pids: 4096 }
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
