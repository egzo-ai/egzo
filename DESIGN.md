# egzo — design draft (v0, schema + UX)

## Principles
1. No daemon. The CLI is stateless; the container engine (docker/podman) is the source of truth.
2. Everything is a container: agents, proxy, vault, control plane.
3. Secrets live only in sidecars. Agent containers are never given real credentials, so a compromised agent cannot leak
   what it does not have (it can still use them through the proxy for the hosts its profile allows).
4. Each agent sits on its own internal network with no default route; the proxy is its only egress
   and the control sidecar its only peer. Agents cannot reach each other directly.
5. `specs/` (pytest, black-box, drives the real binary) is the specification.

## UX requirement (top priority, overrides convenience elsewhere)

The Claude Code TUI is today's gold standard and is what users want. Egzo must never degrade it.
Egzo's value is structure around it: several agents, competing harnesses/frameworks side by side,
and secure workflows, all while the user keeps the Claude Code experience they know and love.

Rules:
1. **Minimal fuss to a working Claude Code.** From zero to a sandboxed Claude Code prompt is a
   tiny explicit `egzo.yaml` (scaffolded by `egzo project init`), then `egzo project up` and `egzo agent spawn AGENT --attach`. There is
   no implicit project and no implicit mounting of the user's directory: everything an agent can
   see is declared in the file.
2. **The TUI is untouched.** What the user sees is the real `claude` TUI, not an Egzo
   re-rendering. tmux (or any pty layer) must be invisible: no status bar, no prefix key, no
   mangled keys, mouse, scrollback, clipboard (OSC52), truecolor, Shift+Enter or resize.
   Slash commands, plan mode, `/resume`, skills, hooks and the user's own CLAUDE.md all work.
   A fidelity spec (pexpect) guards this; a regression there is a release blocker.
3. **No first-run friction.** No theme/login/trust/bypass-permission prompts inside the sandbox
   (v1 learned this: seed `~/.claude.json` onboarding + workspace trust, pre-allow the proxy-needed
   hosts). Authentication is a secret in a vault or reuse of the host's login; the
   agent is never given the real credential (the proxy injects it).
4. **Familiar config carries over.** Optionally mount/copy the user's `~/.claude` settings,
   CLAUDE.md, skills and MCP servers into the agent (read-only, explicit opt-in in YAML) so it
   feels like their own Claude Code.
5. **Egzo features are additive.** Multi-agent, messaging, status, policies appear as
   extra tools and `egzo` subcommands, never as changes to how `claude` itself behaves. Anything
   Egzo needs to tell the user goes out-of-band (`egzo ps`, notifications), not into the TUI.
6. **Same experience for other harnesses.** OpenCode/pi/etc. also run as their native TUI, so
   users can compare frameworks by attaching to each in the same way (`egzo agent attach <agent>`).
7. **Progressive disclosure of config.** Level 1: a few lines. Level 2: full schema (vaults,
   egress, workspaces). Everything is denied unless declared.
8. **Bypass mode is the default and a harness-integration requirement.** The sandbox (rootless/
   gVisor, isolated network, proxy, no real secrets in the agent) is the security boundary, not
   the harness's permission prompts, and autonomous agents would otherwise block on a prompt
   nobody sees. So:
   - Every harness integration must pre-configure its native bypass/yolo mode (settings file, env,
     flags) and a harness without one is not eligible for integration.
   - Spec per harness: in a fresh container, a tool-using prompt completes with no permission
     prompt reaching the TUI.
   - Harness-specific gotchas live in the integration, e.g. Claude Code refuses bypass as root
     unless `IS_SANDBOX=1` (v1), so run as uid 1000 and/or set it, and seed its bypass-accepted flag.
   - Containment specs must not depend on the harness: they assume a hostile agent and assert the
     intended limits (no egress except via proxy, no secrets in the agent, no host access beyond what the file mounts).
   - Prompts that remain are real questions to a human (e.g. Claude's AskUserQuestion), surfaced
     via the `ask` path (a message to a person), never permission gates.
   - Opt-out (`permissions: default`) exists per agent for users who want prompts.

Level 1 example:

```yaml
vaults:
  main:
    backend: env
    secrets: [ANTHROPIC_API_KEY]
egress:
  default:                    # the profile agents get when they name none
    allow: [platform.claude.com]
    services:
      anthropic: main/ANTHROPIC_API_KEY   # built-in service + its secret
workspaces:
  repo:
    git: { url: https://github.com/me/repo.git }
agents:
  claude:
    harness: claude-code
    workspaces: [repo]        # single workspace => it is the working directory
```

### Project name resolution (like docker-compose)
`name:` is optional. Precedence: `-p/--project-name` flag > `EGZO_PROJECT_NAME` env > `name:`
in the file > basename of the directory containing `egzo.yaml` . The result is normalized (lowercase, `[a-z0-9_-]`, must start with a letter or digit) and
is the identity: it becomes the `ai.egzo.project` label and the `<project>-...` name prefix, so
renaming a project creates a new one and orphans the old containers. `egzo project check --resolved` always prints
the resolved name. To catch two directories with the same basename, containers also carry
`ai.egzo.project-dir=<abs path>`. If a project of that name already exists from another directory,
every command (not only `up`) fails with a clear error naming the other directory and refuses
to work. There is no override flag; rename the project (`-p`, env or `name:`) or `down` the other.

## Naming (decided)
- Product, CLI and binary: `egzo`. Hub + web UI: `egzo-hub` (one project; the web UI lives inside it,
  not a separately named product). This repo is the real v1: the earlier attempt in `../egzo` was
  never published, so there is nothing to archive or rename.
- One git repository, one Go module `github.com/egzo-ai/egzo`: `cmd/egzo`, `cmd/egzo-hub`, shared
  code under `internal/`, web frontend under `web/`, pytest suite under `specs/`.
- Org and registry: GitHub `egzo-ai`, images under `ghcr.io/egzo-ai/`, domain `egzo.ai`. Trademark,
  domain, org and registry availability still need a real check (EGZOTech exists in another field).
- Images: one all-in-one image `ghcr.io/egzo-ai/egzo` holds the static `egzo` binary and runs every
  sidecar role as a subcommand: `egzo proxy`, `egzo control`, `egzo prep`. The CLI always uses the
  image tagged with its own version, so CLI and sidecars cannot drift. It includes git >= 2.47 for
  the prep role. `proxy.image` stays overridable. Harness images are separate:
  `ghcr.io/egzo-ai/egzo-harness-<name>`.
- `egzo agent run`, `egzo agent browser` and `egzo state` are the in-container roles (pty holder, state reports), the same binary, copied into
  every harness image.
- Config file `egzo.yaml`, env vars `EGZO_*`. egzo keeps nothing in the project directory.
- Labels follow the DNS convention under our domain: `ai.egzo.*` (e.g. `ai.egzo.project`).

## Reconciliation model
Every resource (container, volume, network) gets flat, readable labels (compose-style):

    ai.egzo.project=<name>
    ai.egzo.service=<key>            # e.g. reviewer
    ai.egzo.kind=agent|proxy|vault|control
    ai.egzo.config-hash=<sha256 of the resolved service spec>
    ai.egzo.spec-version=1
    ai.egzo.project-dir=<abs path>
    ai.egzo.created-by=egzo/<version>
    ai.egzo.agent.harness=claude-code   # a few semantic fields so tools can show an agent card
    ai.egzo.agent.workspaces=repo,scratch #   without parsing a spec

Label rules (spec-enforced): plain short strings; no JSON, base64 or compression; no secrets; a
hard maximum value size far below the engine's ~64 KB ceiling. Labels are for selecting and
filtering, not for storing config (Compose stores only a hash; Kubernetes' last-applied-config
annotation is the cautionary tale).

Names: sidecars `<project>-control-1`, `<project>-proxy-1`; agent instances `<project>-<instance>` (see
"Templates and instances"); networks `<project>_<instance>` (one per instance); volumes `<project>_<vol>`.
`egzo project up` = list by label -> diff config-hash -> create/recreate/remove the **infrastructure** and publish the
templates (agent instances are not reconciled: they are spawned, and marked stale when their template moves).
Idempotent. `up` always
returns once converged: there is no foreground mode and no `-d` (agents are TUIs you `attach` to,
nothing streams to the `up` terminal). Preview with `--dry-run` (long form only, no short flag).
Caveat: labels are immutable, so *runtime status* (starting/idle/working/blocked) is NOT stored in labels.
It is held by the `control` sidecar (in-memory + volume), queried via `engine exec`, and written to the sidecar's log as
status lines that a client can follow through the engine's log API (see **Logging** below).
Existence/config = engine; liveness/status = control sidecar. Engine health = container health.

## Example project file (egzo.yaml)

```yaml
version: 1
name: myproj

vaults:
  main:
    backend: pass              # env | pass (see Secrets and vaults)
    secrets:                   # names in that store; pass names are hierarchical
      - anthropic/api-key
      - github/token
      - deploy/token
      - sites/example/password

proxy:                         # infrastructure of the egress sidecar
  image: ghcr.io/egzo-ai/egzo  # optional override; default is the all-in-one egzo image (`egzo proxy`)

egress:                        # named profiles; an agent links to one (omitted => `default`)
  default:                     # undefined `default` = deny everything
    allow: [platform.claude.com, "*.pypi.org"]   # hosts/globs reachable, no credential
                               # allow: ["*"] grants full internet access (still via the proxy)
    services:                  # built-in services (anthropic, github, ...) need only a secret
      anthropic: main/anthropic/api-key
      github: main/github/token
  operator:
    extend: default            # builds on another profile
    services:
      deploy-api:              # an object defines a custom service
        hosts: [api.deploy.example.com]
        inject: { header: Authorization, value: "Bearer {secret}" }
        secret: main/deploy/token
      example-login:           # a secret the agent types: it gets a placeholder (see Placeholders)
        hosts: [app.example.com]
        secret: main/sites/example/password
        placeholder: EXAMPLE_PASSWORD

workspaces:                    # project-scoped; referenced by name from agents
  repo:
    git: { url: https://github.com/me/repo.git, branch: main }   # https only: a base clone, a worktree per agent
  scratch: {}                  # a directory, in an engine volume (egzo workspace put/get)
  docs: {}

agents:                        # templates: nothing runs until `egzo agent spawn <name>`
  coder:
    harness: claude-code       # claude-code | opencode | pi | custom
    image: ghcr.io/egzo-ai/egzo-harness-claude-code
    workspaces: [repo, scratch] # at /workspace/<name>; 2+ => workdir is /workspace
    egress: default            # profile name; omitted => default
    # workdir: repo            # optional override (name under /workspace, or a path)
    model: claude-sonnet-5-5
    prompt: ./prompts/coder.md
    resources: { cpus: 2, memory: 4g, pids: 4096 }   # pids default 4096
    permissions: bypass        # bypass (default) | default
    env: { FOO: bar }          # non-secret only; validated, secret-looking values rejected

  reviewer:
    harness: opencode
    workspaces: ["docs:ro"]   # a directory workspace, read-only (a git workspace cannot be :ro)

control: {}                    # orchestrator MCP + status sidecar (always present); no settings yet

# No bridges and no users here. Chat integrations (Discord, ...) are hub features, and who may
# talk to agents is never declared in project YAML (see Not in core).
```

## CLI UX

Commands are grouped by what they act on. There are no aliases for names that moved: egzo is pre-1.0.

    egzo project init [--harness claude-code]      # scaffold egzo.yaml
    egzo project up [--recreate] [--dry-run]       # reconcile the infrastructure, publish templates, clone the git bases.
                                                   #   --dry-run lists what would change and exits 1 when something would
    egzo project down [--force]                    # remove what egzo made except the workspace volumes; refused while an agent runs
    egzo project check [--resolved]                # the file, the secrets (set, never their values), drift as a warning; exit 1 on error
    egzo project rotate-ca                         # new project CA; restarts agents
    egzo project unlock                            # remove the project lock left by a command that was killed
    egzo agent spawn TEMPLATE [NAME] [-m MSG] [--attach|--wait] [--reuse] [--no-fetch]   # an agent from a template
    egzo agent ls [--json]                         # the instances, without the sidecars
    egzo agent rm NAME... | --stale|--stopped|--older-than D [--dry-run] [--yes] [--force]   # remove instances
    egzo agent start|stop <name>                   # the same as start and stop below, for agents
    egzo agent attach <agent>                      # native TUI; detach key configurable (default Ctrl-])
    egzo message send <agent> "message" [--wait]   # a request from the operator; --wait prints the resolution
    egzo message ls [--kind K] [--state S] [--to X] [--from X] [--all]    # `msg` is an alias of `message`
    egzo message reply <id> <text>                 # answer or close one addressed to a person
    egzo workspace ls [--json]                     # the workspaces, their volumes and states
    egzo workspace put NAME SRC [DEST]             # copy a file or directory of this machine into a directory workspace
    egzo workspace get NAME[/INSTANCE] [SRC] DEST  # copy a path of a workspace (or an instance's worktree) out
    egzo workspace fetch [NAME...]                 # clone or refresh git bases through the proxy
    egzo workspace rm NAME... | --all              # delete workspace volumes (`project down` keeps them)
    egzo system check                              # engine, rootless, default runtime (gVisor?), network, git: no project file needed
    egzo ps                                        # engine state + status from control sidecar
    egzo start|stop|restart <svc>                  # operate on existing containers (no reconcile)
    egzo logs [-f] <svc>
    egzo exec <svc> -- cmd
    egzo events [-f] [--agent A]
    egzo proxy log|rules                           # audit trail

## Decisions
- Language: Go.
- Engine access: Docker Engine API (Go docker client). The engine is whatever `DOCKER_HOST` points at
  (default `/var/run/docker.sock`), like the docker CLI: no probing, no engine setting in egzo.yaml.
  Podman works through its Docker-compat socket (`podman.socket`). egzo never chooses an OCI runtime
  (no `runtime:` key): containers run under the engine's own default, so gVisor is a host setting (`default-runtime` in
  Docker's `daemon.json`), and the `docker-gvisor` platform of the specs is that host. Never mount the socket into agent
  containers. Engine matrix in specs.
- No daemon. Everything runs in containers except the `egzo` CLI.
- Harness control is TUI-first (Scion-style). The native TUI always runs in the agent container
  behind a pty layer (the `Session` backend, see Session backend below); there is never a mode
  switch. ACP was the v1 primary channel and is dropped: it cannot run concurrently with the TUI on
  one session. It may return later as an optional driver for headless agents only.
  - Human -> agent: a message in the control sidecar; a short fixed line naming it is typed into the terminal
    (see "Messages"), and the agent fetches it with a tool.
  - Agent state -> orchestrator: a harness reports its state (`egzo state`, or `POST /v1/state`) to the control sidecar's agent API.
  - Agent -> human: MCP `egzo` tools (`resolve`, `update`, `ask`, `message`, `status`; see "Messages").
  - Attach (CLI and web UI) = `engine exec -it` into the Session backend.
- Specs: pytest in `specs/`. A spec for an unbuilt feature fails (the failing specs are the todo list); strict
  xfail is only for platforms that lack a feature.
- Reuse from v1 (the abandoned first attempt): harness adapter Render logic (pure, golden-tested), proxy/gate
  lessons (todos/0002), Claude onboarding/trust seeding in ~/.claude.json.

## Workspaces (engine volumes)
- Declared at top level and **project-scoped**: one workspace is shared by every template that lists it. An agent
  references them in `workspaces:` as a list of `name[:ro]`; a name that is not declared is an error, and so is a
  path (`./docs`, `/srv/data`), with a message that says to declare it. Nothing of the host is ever mounted: a
  workspace has no `path:` (the key is an error that names `egzo workspace put`), and egzo leaves nothing in the
  project directory. Everything that works on agents needs the engine and nothing else, so it works from another
  machine and from the hub.
- Two kinds, both **engine volumes** (labelled `ai.egzo.kind=workspace`):
  - **Directory** (no `git`): `<project>_ws.<name>`, mounted at `/workspace/<name>` in every agent that lists it. `up`
    creates it empty and owned by the agent user; egzo never deletes it and never looks inside. Files go in and out
    with `egzo workspace put|get`.
  - **Git** (`git: {url, branch}`): the base clone in `<project>_ws.<name>.base` (`up` makes it, last, with no files
    checked out) and one worktree per instance in `<project>_ws.<name>.wt.<instance>` (made by `spawn` on the branch
    `egzo/<instance>`, labelled with the instance so removing the instance removes it).
- The dot in a volume name cannot appear in a project, workspace or instance name, so a workspace volume never has
  the name of another egzo volume (`control`, `ca`, `ca-private`, `egress`, `<agent>-home`).
- Mount point: `/workspace/<name>` for a directory. For a git workspace the container sees
  `/workspace/<name>/<instance>` (the instance's own worktree volume, its working directory when it is the only
  workspace) and `/workspace/<name>/.base` (the base volume), as siblings. Never another instance's worktree: it is
  not mounted, and one agent cannot reach another's work except by what it pushed.
- Working directory: exactly one workspace => its directory (a git workspace: the instance's worktree); two or more
  => `/workspace`. `workdir:` overrides (a workspace name or a path).
- `:ro` on a git workspace is a validation error: git needs write access. For read access use a directory workspace.
- Git sources are HTTPS only: ssh and local paths are validation errors. A repository on the machine is a directory
  workspace filled with `egzo workspace put`, with no git semantics.
- The names `control`, `ca`, `ca-private`, `egress` and `<agent>-home` stay reserved for workspaces.
- Who does the git: a short-lived **prep container** (git >= 2.48, run with the agent uid, the volumes mounted, egress
  via the project proxy only). The CLI creates it through the engine API and removes it afterwards. Reasons: no host
  git, correct ownership under rootless engines, the same behaviour for local and remote engines, and no credentials on
  the CLI: the proxy injects the HTTPS token (the `github` service). Agents push the same way, so no agent holds a git
  credential. Questions about a volume that the CLI cannot read (is the base cloned, what url, which `egzo/*` branches
  exist) are answered by `egzo prep git info` in a prep container; `egzo prep git forget` makes a base forget the
  worktrees of removed instances.
- **`up`** clones each base that an agent lists and that is missing, last and never fatally (a failure is a warning;
  `spawn` tries again, and an empty base volume that a failed clone made is removed). The clone has no instance to speak
  as, so it runs on a network of its own under the reserved identity `prep`, bound in the proxy to the egress profile of
  the first template that lists the workspace. A base that exists is left alone, with a warning when its origin url is
  not the one in the file.
- **`spawn`** fetches the configured branch (the url of the file, never the base's remote, which may be one the proxy
  cannot serve) into a ref of its own, as the instance, and runs `git worktree add --relative-paths -b egzo/<name>`
  from it. A failed fetch warns and uses the base as it is (`--no-fetch` skips it); a missing base is cloned first.
  It refuses a worktree volume or a branch that is already there; `--reuse` continues with them.
- **Relative links.** Git writes the links between a worktree and its base as relative paths, `../.base/.git/...` and
  back, which resolve in every container because the two volumes are mounted side by side under these names. There
  is no worktree to open on the host: `egzo workspace get NAME/INSTANCE PATH DEST` copies files out.
- **Removal.** An instance's worktree is its own: `agent rm` and `project down` delete its volume with the instance,
  with no check for unsaved work (there is none yet; a check and a confirmation can come later), and the base forgets
  it. The branch `egzo/<name>` stays in the base, so what was committed outlives the instance. `project down` keeps the
  base clones and the directory volumes and says so; `egzo workspace rm` deletes them (refused while an instance mounts
  the volume). `up` after `down` finds them again.
- **The project lock** is an engine-side lease, not a file: a container `<project>.lock` created and never started
  (creating a container under a name is atomic), labelled with its holder and its expiry (30 minutes). The command
  that took it removes it when it ends; `egzo project unlock` removes one left by a killed command. It is not
  observed, so `ps`, `up` and `down` never see it. A flock on the project directory would not work for a client that
  has no access to the directory.
- **The prompt** of an agent is read by `up` and published in the template (at most 64 KB); an instance gets it in
  `EGZO_PROMPT`, so a spawn needs no file of the host.
- Harness integrations map extra workspaces to the harness (e.g. Claude Code additional directories) and seed
  workspace trust for each directory.

## Templates and instances (decided, built)

An entry under `agents:` is a **template**: it says how to run an agent and runs nothing. `egzo project up` brings up
the project's infrastructure (networks, volumes, proxy, control), validates the file and **publishes the
resolved templates** to the control volume. An **instance** is an agent made from a template with `egzo agent spawn`.
Spawn does not read `egzo.yaml`: it uses the templates `up` last published, so an instance is always what `up`
applied, and only the CLI's `up` changes templates. A spawn from the CLI, the hub or any other tool is the same
operation (an importable package, not CLI code).

### The experience

    egzo project up                                       # infrastructure + publish templates; starts no agent
    egzo agent spawn coder                              # instance `coder-1` (auto name), returns
    egzo agent spawn coder issue-412 -m "Fix #412"      # named, first message sent
    egzo agent spawn coder issue-412 -m "..." --attach  # ...and attach to it
    egzo agent spawn reviewer pr-88 -m "Review PR 88" --wait [--timeout D]   # exit codes as `send --wait`
    egzo ps                                       # instances, with TEMPLATE, ACTOR, AGE, STALE
    egzo agent attach issue-412 | send | logs | exec | start | stop | restart   # by instance name
    egzo agent rm issue-412 [--force]
    egzo agent rm [--stopped] [--stale] [--older-than D] [--dry-run] [--yes]

There are no parameters: an instance is a template, a name and an optional first message. What differs per task is
said in the message. Anything else per instance belongs in another template.

### Rules
- **Names.** An instance name is unique in the project, follows the same name rules as every other name, and is
  how everything addresses the instance (`agent:<name>`, `attach`, `send`). Omitted, it is `<template>-<n>`
  with the first free `n`. It cannot equal a template name, `control` or `proxy`, or make a container name that
  a sidecar has. Container `<project>-<name>`, network `<project>_<name>`.
- **Spawn refuses a name that exists.** It fails with exit code 17 (`EEXIST`) and says which instance and
  template the name belongs to; no message is sent and nothing changes. A script that retries an event treats 17
  as "already there" and decides whether to `send`. The engine's unique container name makes the check atomic
  when two callers race.
- **Spawn needs the infrastructure** (`up` was run and proxy and control run): otherwise it fails and says so.
  It never starts the infrastructure, and never creates an instance outside what a template says.
- **What spawn does** is the per-agent half of `up`: the instance's network (control and proxy join it), token,
  home volume, workspace preparation (a worktree of each git workspace, named after the instance), the container,
  and two registrations: control learns the agent (one verb to add and one to remove an agent, never a whole
  list) and the proxy binds the instance's token to the template's egress profile. **Spawn never reads a
  secret**: `up` loads the egress profiles, with their secret values, into the proxy; the binding carries only
  the token and the profile's name. That is what lets the hub, which never touches secrets, spawn. Spawn with
  `-m` then sends the message; it waits in the queue until the harness is ready, as every message does.
  A spawn that fails halfway removes what it created, and never a worktree that holds work.
- **`rm`** removes the instance's container, network, home volume and worktrees (the branch stays in the base).
  A running instance needs `--force`.
- **`down`** removes every instance, like every other resource of the project, and their worktrees. It never
  deletes a base clone or a directory workspace.
- **Stale.** An instance whose template changed since it was spawned, or whose template no longer exists, is
  **stale**: its `ai.egzo.template-hash` differs from the hash of the template in `egzo.yaml` (the hash covers
  everything that went into the instance: the agent, its egress profile, the workspaces it lists, its prompt's
  content, its image). `up` refuses before it publishes, so the file, not the last published templates, is what
  an instance is compared with. Nothing restarts a stale instance. `ps` marks it, `agent rm --stale` clears it, and
  `up` refuses while one exists, naming them, so the user stops them first. `up --dry-run` prints `stale: NAME`
  for each and says it would refuse.
- **Reaping.** egzo has no reaper. `agent rm --stopped|--stale|--older-than` removes stopped, stale or old instances (age is the container's
  creation time); `ps --json` gives a script or a timer what it needs to decide. it asks first, unless `--yes`; `--dry-run` lists and changes nothing.
- **Labels.** `ai.egzo.service=<template key>` (so `ps` groups by it), `ai.egzo.instance=<name>`,
  `ai.egzo.actor=<who>` (`operator` from the CLI; `user:<id>` or a service from another tool) and
  `ai.egzo.template-hash=<hash>` on the instance's container, network and home volume. `ai.egzo.instance-id=<ULID>` on its
  container and network only: spawn makes it and it is never reused, so history (the proxy's audit trail, the hub) tells two
  instances of one name apart. A volume can be taken up by the next instance of the name and does not carry it
  ([roadmap/instance-id.md](roadmap/instance-id.md)). `ps --json`, `agent ls --json` and `agent spawn --json` show it as `id`.
- **Workspaces belong to the project; worktrees to the instance.** A worktree is a volume named after the
  instance; the base clone and every directory workspace are volumes shared by every instance that lists them.

### For other tools (hub, broker, scripts)
`agent spawn`, `agent ls`, `agent rm` and `ps` take `--json`. The hub calls the same package; a broker can run the CLI against
the engine. A webhook becomes `spawn` followed by `send` (or `spawn -m`). Retrying a webhook is safe for the
spawn and not for the message. Spawning, stopping and removing instances of declared templates is **operating**,
not creating or changing a project: the hub's rule that only YAML and the CLI create projects holds, because the
templates are declared and published by `up`.

## Control plane (decided)
- The control sidecar holds status, event log, message queue and spec snapshots (volume).
- Agent -> control: HTTP on the container network. MCP over streamable HTTP; state reports and a stdio-MCP
  fallback go through the small static `egzo-agent` binary (also the candidate pty holder).
- One network per agent; control and proxy join each agent's network, agents never share one, so
  agent-to-agent traffic only exists through control and policy. `NO_PROXY` covers the control host.
- Per-agent token (generated by `up`, injected as env) identifies the agent to control. It is an
  agent identity, not a provider secret; compromising an agent only lets it act as itself.
  The same token is the agent's proxy credential (`HTTPS_PROXY=http://agent:<token>@proxy:3128`).
- Control has two listeners: an **agent API** (TCP, token auth, agent verbs only: the message tools,
  status, agents, state ingest) and an **operator API** on a unix socket inside the container,
  reached only via `engine exec` by the CLI/hub (read spec, send and list messages, answer, interrupt).
  Operator verbs are never on the network.
- To verify in a spike: per-agent `internal` networks on rootless podman; why not unix sockets:
  gVisor probably blocks connecting to host-created unix sockets via bind mounts.

## Secrets and vaults (decided)

- A **vault** is one secret store: a name, a `backend`, and `secrets`, a list of the names to read from that
  store. There is no per-secret source (no `from:`): the backend knows where its secrets live, so a vault is
  the only concept and nothing in a secret entry repeats the backend. A secret is referred to as
  `<vault>/<secret>`, split at the first `/`: vault names cannot contain `/`, secret names can when the
  backend has hierarchical names (`main/company/project/test` is the secret `company/project/test` of
  `main`).
- **Two backends exist: `env` and `pass`.** Any other name, whatever it is, is the same validation error
  (`unknown backend "x"`, naming the two that exist). The schema, the errors, the docs and the specs mention
  nothing that does not exist: no removed or planned backends, no reserved names. A backend is added when it
  works, together with its specs.
- **`env`** reads the environment variable of the same name from the shell that runs the CLI. A secret name
  must be a valid variable name. It is accepted but discouraged (every process of the user can read it, and
  it ends up in shell history and dotfiles): every command that loads a project with an `env` vault, except the ones that relay a process's own output (`exec`, `attach`, `logs`), prints
  `Using ENV var based secret backend is not recommended.` on stderr, once, and goes on.
- **`pass`** runs the `pass` program (https://www.passwordstore.org/) directly, `pass show <name>`, with the
  secret name as the entry name, slashes included (a name never starts with `-`, and egzo passes it after `--`,
  so it cannot be taken for an option). The secret is the first line of the entry (pass's own convention; the
  other lines are notes). A name that is a pass directory is not a secret: `pass show` prints a tree for it,
  and egzo reports it as a directory. egzo gives pass the environment and terminal of the CLI untouched,
  so unlocking is whatever the user's pass and gpg-agent already do (a pinentry prompt, or none when the
  agent has the key). **The project file cannot configure pass**: no store path, no gpg home, nothing. Someone
  with another store sets `PASSWORD_STORE_DIR`, `GNUPGHOME` and the like in their shell, as for any use of
  pass. If `pass` is not on `PATH`, a command that needs a secret fails and names it.
- **egzo never writes a secret.** There is no `secrets set` or `secrets rm`: secrets are created, changed and
  removed with the user's own password manager (`pass insert`, an export in the shell). egzo only reads them,
  and `egzo project check` says which ones it can.
- **There is no file backend, and no plain-text source of any kind**: a secret in a file is protected by
  nothing but permissions, which is not a store egzo supports. `env` stays only because a throwaway shell
  export is how people try things; it warns for that reason.
- **When secrets are read.** On the host, by the CLI, in the user's session, once per command, and only by the
  commands that need the values (`up`, `diff` and `restart proxy`, which loads the policy again; `spawn` and
  `rm` never read one). They go to the
  proxy as described under Hardening ("Secrets reach the proxy only through `engine exec` stdin"). A command
  that cannot read a secret fails before it creates anything. `secrets ls` reads nothing it does not have
  to show: it reports `set`, `empty` or `missing` (with the reason pass gave) and never a value.

### Two ways to use a secret

Both keep the value out of the agent: it lives in the proxy and is applied to requests for the hosts of a
service of the agent's profile, and only those.

- **`inject`**: the proxy sets a header on every request to the service's hosts, replacing whatever the
  agent sent. The agent does nothing and never sees anything (API keys, bearer tokens).
- **`placeholder`**: for a secret the agent has to put somewhere itself, such as a password it types into a
  login form or a token it writes into a request body. The agent gets an environment variable holding a
  placeholder, uses it as if it were the secret, and the proxy replaces the placeholder with the secret in
  requests to the service's hosts.

Neither stops the agent from *using* the secret while it runs (see `docs/security.md`), and both have limits
(`known-issues/secret-injection-limits.md`).

### Placeholders

```yaml
egress:
  default:
    services:
      example-login:
        hosts: [app.example.com]
        secret: main/sites/example/password
        placeholder: EXAMPLE_PASSWORD
```

- Every agent whose profile has the service (after `extend`) gets `EXAMPLE_PASSWORD=egzo-ph-<32 hex>` in its
  environment. The name must be a valid variable name that egzo does not set itself, and cannot also be in
  the agent's `env`. A service has `inject` or `placeholder`, not both.
- The placeholder is random, made once per secret reference by `up` and kept with the project state on the
  control volume. It never changes while the project exists (a running agent keeps a working one, and
  rotating the secret does not change it), it is not derived from the value, and it is not a secret: it may
  appear in templates, labels and logs.
- The proxy replaces the placeholder with the secret **only in requests to the hosts of that service, for the
  agents whose profile has it**: in the URL path and query, in header values and in the request body. The
  same text sent to any other host, or by an agent of another profile, goes through unchanged. Replacing is a
  plain text match on the placeholder; it never looks at responses.
- The value is written the way the place needs it: percent-encoded in an `application/x-www-form-urlencoded`
  body or a query string, JSON-escaped in a JSON body, as is elsewhere. `Content-Length` is recomputed.
- Bodies are scanned when they are uncompressed and at most 1 MiB. **A request to a placeholder host with a
  body that cannot be scanned (compressed, or over 1 MiB) is refused**, never forwarded half-handled: the proxy
  answers `413` (too large) or `415` (compressed) with a message that names the reason, and logs the request as
  denied (code `placeholder`) with the same reason. The rule depends only on the request, so a given request always succeeds or always
  fails, and the failure says why. The reason is a fixed sentence: nothing taken from the request is ever put
  in it, since the agent controls the request (it could otherwise put its own placeholder there). Hosts of a placeholder service are meant for logins and API calls, not
  uploads; see `known-issues/secret-injection-limits.md`.
- Hosts of a placeholder service are TLS-intercepted, like those of `inject`, and are audited the same way.

## Egress policy (decided)
- Two top-level concerns: `proxy:` is infrastructure (image, audit); `egress:` is policy.
- `egress:` is a map of named **profiles**. A profile defines all rules and credential injections.
  An agent links to exactly one profile with `egress: <name>`; omitted => `default`. If `default`
  is not defined, it denies everything. There is no `deny` key and no other default: anything not
  allowed is denied.
- Profile keys: `extend` (one parent profile), `allow` (hostnames, `*.domain` globs, or `*`),
  `services` (map).
- `allow: ["*"]` is how full internet access is granted; it is still proxied and audited, and
  credentials are still injected only for hosts of services in the profile.
- A **service** defines *how* to talk to something: `hosts` (required), optional `secret`, and at most
  one way to use the secret: `inject` (`header`, then `value` containing `{secret}`, or `basic_user` for HTTP Basic
  authentication with the secret as the password; with neither the raw secret is the header value) or `placeholder` (an environment variable name, see Placeholders). A service
  with neither is a pure allowlist and cannot have a secret. Optional `inspect: true` audits request paths.
  Rules are by host only: a service has no path or method scope, and keys for them are rejected like any
  other unknown key. They can be added if a real use-case turns up.
- **Service hosts are exact, or a wildcard without a secret.** `hosts` take what `allow` takes (`host`, `*.domain`,
  `*`, folded like any name). A service with `inject` or a `placeholder` may only name exact hosts: a wildcard
  would hand the secret to every subdomain, including attacker-controlled ones, so `egzo project check --resolved` refuses it (a secret
  bound to an inherited wildcard service fails the same way). A wildcard service without a secret is an allowlist entry
  (it counts for reachability and for the harness-provider warning) and, with `inspect`, audits every request to the
  hosts it covers. It can sit next to an exact injecting service on a covered host: the exact one injects, `inspect`
  still applies. `*.domain` does not cover `domain`. There is no key that weakens TLS verification or the Encrypted
  Client Hello rule, and unknown keys stay errors.
- `services` entries have two forms: a **string** `<vault>/<secret>` gives the secret for a service
  that already resolves (inherited via `extend`, or built-in); an **object** defines the service
  (hosts, optional inject, optional secret). There is no null form.
- Name resolution for a service: the profile's own definition, then the `extend` chain, then the
  built-ins. Binding a service that has `inject` or `placeholder` with no secret is a validation error, and
  so is a secret on a service that has neither.
- `extend` merge: `allow` lists are unioned; `services` are merged by name with the child winning:
  a string overrides only the secret of the inherited definition (hosts and inject kept), an
  object replaces the definition entirely. A child cannot remove anything it inherits. One parent
  only; cycles and unknown parents are errors.
- **Built-in services** ship with egzo as data. `anthropic-oauth` is for a Claude subscription token (`claude setup-token`, `sk-ant-oat…`), sent as `Authorization: Bearer`; the agent then gets `CLAUDE_CODE_OAUTH_TOKEN` instead of `ANTHROPIC_API_KEY`. A token bound to `anthropic`, or an API key to `anthropic-oauth`, is refused at `up`. Initially `anthropic`: `api.anthropic.com` +
  `x-api-key`; `github`: `github.com` and `api.github.com` + `Authorization: Basic` with the token as password, which git over HTTPS needs); `opencode`: `opencode.ai` + `Authorization: Bearer`, for the key (a service account's) of an OpenCode Go or Console workspace, and OpenCode agents always get a placeholder `OPENCODE_API_KEY` to make it send one (the model list on `models.opencode.ai` needs no credential), more later
  (openai, google-ai, package registries). An object with a built-in's name replaces it.
  `egzo project check --resolved` prints each profile fully resolved.
- **ssh servers** (`ssh:` in a profile, a map by name) are the ssh analogue of `inject`: the agent runs `ssh <name>`, the proxy
  holds the key and re-originates the connection (see `roadmap/ssh-access.md`). A server has `host` (one exact name or address, no
  wildcard, no port), `port` (default 22), `user` (fixed here: the agent never picks the remote account), `key` (a
  `<vault>/<secret>` holding an unencrypted private key) and `host_key` (required: a `SHA256:` fingerprint or a public key line,
  and there is no value that means trust on first use). The name is a lowercase label (the agent's `Host` alias; not `root`).
  No other key exists, so nothing weakens the checks. `extend` merges ssh servers by name, the child winning. A server opens no HTTPS
  host. `--resolved` shows the key's reference, never the key.
- Reachability for an agent = its profile's `allow` plus the hosts of its services (after extend).
- Validation: unknown profile, unknown parent, cycle, unknown service name (suggest close names),
  unknown vault secret, two services in one profile sharing a host with different injection
  (ambiguous), and a warning when an agent's profile cannot reach its harness's provider host.
- The proxy config is compiled per profile and applied per agent through the per-agent proxy
  credential; changing a profile changes the proxy's config hash (proxy recreated, CA kept).
- Harness integrations may need credential-free hosts (Claude Code: `platform.claude.com`). For now
  these are declared explicitly in `allow`; whether integrations declare them is open.
- Unverified: that GitHub accepts `Authorization: Bearer` for git over HTTPS (spike).

## ssh through the proxy (decided, built)
- A second listener of the proxy (`:2222`, agents reach it as `proxy`) terminates ssh and re-originates it (`internal/proxy/ssh*.go`, `golang.org/x/crypto/ssh`).
  Public key authentication only. The identity of an instance is an ed25519 key **derived from its token** (HKDF), so there is nothing to
  store: `up`/`spawn` bind the public half (`Binding.ssh_key`), `egzo agent run` writes the private half to `~/.ssh`. The decision is taken on the key
  that signed, not on the keys offered (CVE-2024-45337). The ssh *user* the agent asks for is the name of a server in its profile; an unknown
  identity, a server of another profile and an agent without a server get the same refusal.
- Then the proxy dials the server (dial timeout, private addresses allowed because an operator named the host; loopback and cloud metadata
  addresses refused) with the key of the profile and checks the pinned host key. A fingerprint pin cannot say which key type the server has, so
  each type is tried in turn; a public key line fixes it. A failure reaches the agent as ssh itself would show it (stderr, exit status 255) and never
  quotes the key; the audit records the key the server presented.
- Relayed: exec, shell, pty, window-change, env, signal, stderr, exit status, end of input. Refused and audited: direct-tcpip, tcpip-forward, agent and
  X11 forwarding, every subsystem but `sftp`. Bounded: 10 sessions per connection, per-agent and global connection limits, 64 handshakes at once, keepalive both
  ways, one hour idle. Unbinding an agent or changing its identity closes its connections.
- The proxy publishes `ssh_config` (one `Host` per server name of the policy, all the proxy's listener, told apart by the user) and `ssh_known_hosts`
  (its host key under the alias `egzo-proxy`, strict checking) in the shared CA volume whenever a policy is loaded. The agent copies the servers into a
  managed block at the top of `~/.ssh/config` (copied, not included: ssh refuses an included file owned by neither the user nor root).
- ssh needs a passwd entry for its uid and takes `~/.ssh` from its home; egzo gives the agent's user one (see "The agent's user has a passwd
  entry"), so the stock `openssh-client` runs as it is.
- Config: `egress.<profile>.ssh.<name>: {host, port?, user, key, host_key}` (see Egress policy). Keys are unencrypted (no passphrase, no FIDO).

## Egress proxy and TLS (decided)
- MITM for hosts of services with credential injection in the agent's profile. Everything else allowed
  is a CONNECT tunnel with host/SNI match (domain-fronting protection), **except for a client that sends Encrypted
  Client Hello**: the extension can hide the real server name, and GREASE (what Chromium sends to every host through a
  proxy, since it never resolves the destination and so never learns an ECHConfig) has the same format as real ECH
  (RFC 9849), so the proxy cannot tell them apart and cannot strip the extension (the client's `Finished` covers
  its hello). It therefore never tunnels such a hello: if the outer SNI is the approved host it terminates TLS with the
  project CA, replaying the bytes it read (failing closed to interception, never to passthrough); a missing or different
  name is denied as for any hello. This needs no switch and no client change. A terminated-because-ECH connection is
  logged as a `connect` with the mode `intercept` (reason code `ech`) and a `close` with bytes, not per request (a host with `inspect`
  never gets there: it is intercepted for every client from the start and logs each request, with the mode `inspect`). Every ECH codepoint of the drafts and the legacy ESNI extension count as ECH. A client that does not
  trust the project CA fails the handshake and the audit says why and what to do. The handshake has a hard time limit
  that a trickle of bytes cannot extend; the idle timeout counts after it. Hosts are compared lower case with one
  trailing dot trimmed (CONNECT target and Host header alike), and a Host header that is not a plain name is a 400. Clients that send
  no ECH are tunneled as before. Hosts of services with a
  `placeholder` are intercepted too. `inspect: true` on a service intercepts its hosts only to audit them
  (off by default): the audit then has a `request` line (method, path, status; never the query string or a
  body) for every request, which is exactly what an intercepted host with `inject` or `placeholder` already
  gets, so `inspect` matters for the hosts of a service with neither. The price is the interception: a client
  that pins its certificate stops working on an inspected host.
- One CA per project, generated by the proxy on first start (ECDSA P-256), private key only in a
  volume mounted into the proxy; never in an agent, the CLI or the hub. Persistent across proxy
  recreation (regenerating would force agent restarts); `egzo project rotate-ca` is explicit. Leaf certs are
  minted on the fly, short-lived, cached in memory. NameConstraints = later hardening (todo spec).
- Trust distribution: proxy publishes `ca.crt` and a combined bundle (system CAs + project CA) to a
  volume writable by the proxy, read-only for agents. Agents bind-mount the bundle over
  `/etc/ssl/certs/ca-certificates.crt` and get `SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`,
  `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`, `GIT_SSL_CAINFO`. Agents start after the proxy is healthy.
- Per-agent policy comes from the agent's egress profile, keyed by the per-agent proxy credential.
- v1 lessons to port as specs: always HTTP/1.1 toward the agent (Bun rejects an HTTP/2 status line);
  never re-encode bodiless responses (304/204/1xx/HEAD) as chunked; aborted handshakes from
  pre-warmed connections log as `aborted`, not `denied`; `platform.claude.com` allowed w/o injection.
- Pinned-cert hosts cannot be injected (out of scope).

## Browser (decided, `roadmap/browser-access.md`)
- The stock `claude-code` and `opencode` images carry Chromium (installed at build time in `/opt/ms-playwright`), the
  pinned `@playwright/mcp`, and `libnss3-tools`. No flavor, no recipe: a browser is a basic tool.
- `egzo agent browser` (in-container role) builds the Playwright MCP configuration from the agent's environment (the
  proxy URL's credentials as separate fields, loopback bypassing the proxy, headless, isolated profile, no sandbox,
  small `/dev/shm`, output in a temporary directory, never the workspace) and runs `playwright-mcp`. Nothing is configured
  by hand in the client: no ECH policy, no proxy flags.
- The harness integration registers it as a stdio MCP server `playwright` next to `egzo`, only when `playwright-mcp` is on
  the `PATH`. `egzo agent run` imports the project CA into `~/.pki/nssdb` when `certutil` is on the `PATH` (Chromium
  ignores `SSL_CERT_FILE`).
- Reach is the egress profile; the proxy terminates what a browser sends (see Egress proxy and TLS).

## Session backend (decided: the pty holder, built)
The holder is `egzo agent run`, the entrypoint of every egzo harness image (`internal/session`). It runs the
harness on a pty inside the agent container, serves clients on a unix socket in the container, and is a raw
pass-through: no tmux, no screen model, nothing drawn around the TUI, so the user's own terminal keeps its
scrollback, clipboard (OSC 52), hyperlinks, truecolor, Shift+Enter and mouse. What it does add:
- clients are `egzo agent attach` run through `engine exec` with a terminal (`egzo agent attach`): any number of
  read-write clients, and read-only observers (`--read-only`) whose keys are ignored and never count as a
  human typing; the detach key is the client's (default Ctrl-], `--detach-keys`), and the engine's own
  Ctrl-P Ctrl-Q detach is switched off for terminals, because it would swallow keys of a TUI;
- a client starts with the last 64 KB the program wrote, then the program is asked to repaint at the client's
  size (a real size change, or a one-column wobble when the size is the same: the kernel only signals on a change);
- it types injected messages (bracketed paste, then Enter) and the interrupt key, and watches the output for
  quiescence-based harnesses.
The fidelity matrix is `specs/session/test_session_attach.py`, run against a stand-in TUI (`specs/fixtures/fake-tui.sh`) so
every row is observable: all byte values both ways, a 36 KB bracketed paste, resize, truecolor, no alternate
screen, Ctrl-C and Ctrl-D reaching the program, OSC 52 / OSC 8, detach and reattach, two clients, observers.
Known limit: a detach-key byte inside pasted data detaches. tmux was not built: it takes the outer alternate
screen, so native scrollback is lost.

## Messages (decided, replaces pasting message text into the terminal)

Everything people and agents say to each other is a **message**. The terminal only ever gets a short fixed
line announcing a message; the message itself is fetched through the agent's tools (MCP). This keeps the
sender out of anything typed (nothing a message contains can forge an origin or leave the paste), gives
the typed line a user's authority, and makes a retry safe because the announcement carries no content.

### The model
- **Addresses:** `operator` (the CLI), `user:<id>` (a person, via the hub), `agent:<name>`.
- **A message** has an id (`m` + 32 random hex characters, unguessable: it is a capability), `from`, `to`, a
  `kind`, `text` (at most 16 KB), `re` (the id it answers or belongs to), `hops`, and for a question
  `choices` (a list of strings, for quick answers; unused for now).
- **Kinds:** `request` (`message`, or `egzo message send`), `question` (`ask`), `resolution` (what `resolve`
  sends back to the sender: the result of a request, or the answer to a question; it carries an `outcome`),
  `update` (`update`: progress on a request).
- **States** of a request or question: `queued` (written), `announced` (the line was typed into the
  recipient's terminal), `fetched` (the recipient fetched it), `resolved` (with an outcome: `done`,
  `declined` or `failed`; terminal: a requester who is unhappy sends a new message). A resolution is closed
  once fetched. An update is passive: it is never announced, only visible (`list_messages`, `egzo message ls`,
  `egzo message send --wait`). A message to a person is not announced either: it waits in `egzo message ls` until
  answered (`egzo message reply`).
- **Ids are checked, not trusted:** `get_message`, `resolve`, `update` and `ask` work only on messages
  addressed to the calling agent, and `resolve`, `update` and `ask` only after it fetched the message. An
  unknown id and someone else's id answer the same: "no such message".

### The agent's tools (MCP server `egzo`, `http://control:7777/mcp`)

| Tool | What it does |
|---|---|
| `list_messages()` | The agent's own open items: requests and questions it has been announced or has fetched and not resolved, and unread resolutions and updates. After a restart, or when context was compacted and ids were lost, this is how it finds what it owes. Never shows what was not announced yet. |
| `get_message(id)` | Fetch one message addressed to the agent: sender, kind, text, `re`, time. Marks it `fetched`. |
| `resolve(id, text, outcome)` | Close a request or question with a result. Sends a `resolution` to the sender. Terminal. |
| `update(id, text)` | A progress note for the sender of request `id`. Agents are told to send one when they need time. |
| `ask(id, text, [choices])` | Ask the sender of message `id` something. Leaves `id` open; the answer comes back as a `resolution` linked to it. |
| `message(to, text, [re])` | A new request to `operator`, `user:<id>` or `agent:<name>`. It does not resolve anything. This replaces `handoff`: an agent that needs something done sends a message, waits for the resolution, then resolves its own request. |
| `status(text)` | One line on what the agent is doing, shown by `egzo ps`. |
| `agents` | The other agents with their activity and open counts. |

Retired: `say` (use `message` to `operator` or `update`), `ask_user` (`ask`), `get_answer` (the answer is a
message), `check_inbox` (`list_messages`), `handoff` (`message`).

### Announcing
The holder asks the control sidecar when the terminal is ready; control answers with the line to type.
- **Gates:** the agent is `idle`, nothing it was announced is awaiting a fetch, the TUI has taken the terminal and
  drawn its prompt, and no read-write client has typed for `inject.human_quiet` (default 30 s). Read-only
  observers never block. A half-typed draft merges with the line (no screen model); accepted.
- **Mechanics:** one atomic write of `ESC[200~ line ESC[201~`, a pause, then Enter, as before. Only the
  id varies in the line, and it is hex, so there is nothing to sanitize but nothing to trust either.
- **The lines** (control composes them, in one place; one line per kind, joined, when several are pending):

| Kind (and sender) | Typed line |
|---|---|
| request from `operator` or a `user` | `check egzo message <id> and handle the request for me.` |
| request or question from an `agent` | `egzo message <id> from another agent is waiting: fetch it and decide whether it fits your work.` |
| resolution (a result or an answer) | `egzo message <id> is the reply to your earlier request: fetch it.` |
| several of one kind | `check egzo messages <id1>, <id2> and handle each one.` (wording of the kind, ids listed) |

  A message pending when the harness starts is announced the same way once its TUI is ready.
- **Fetching is the acknowledgement.** No fetch within `inject.ack_timeout` (60 s by default) announces
  again, safely, up to three times in all; then the message is `unconfirmed`, shown by `egzo message ls`, and
  still found by `list_messages`. Reporting `working` no longer acknowledges anything.
- **Interrupt:** `egzo message send --interrupt` makes the holder send the harness's interrupt key first, as before.
- **Limits** (a loop between agents is easy to start): at most 20 open requests per recipient, a thread is
  at most 8 messages deep (`re` adds one hop), and a sender may send at most 30 messages a minute.
  Past a limit the tool answers with an error that says which.

### Agent status: what the harness does, and what the agent owes
- **Activity** (from the states a harness reports, the holder and the engine): `starting`, `idle`, `working`, `blocked` (the
  harness is stuck on its own UI: a permission dialog, a login: something only a person at the terminal can
  answer), `stopped` (the container is not running). `working`, `blocked`, `starting` and `stopped` hold back
  announcements; messages for a stopped agent wait and are announced when it is ready again.
- **Overlay**, derived from messages and never a gate: `open: N` (requests and questions fetched and not
  resolved) and `waiting` (it has asked a question that is not answered). An agent that asked something
  usually ends its turn and goes `idle`, so `waiting` must not hold announcements back, or the answer
  could never arrive.
- `egzo ps` shows ACTIVITY, OPEN and WAITING next to the status line.
- Signals: the state a harness reports in egzo's own words (`idle`, `working`, `blocked`, with a `reason` for people that the
  control never reads). Claude Code's hooks run `egzo state <state> --reason <hook>`; the mapping from its hook names (and its
  notification kinds: `idle_prompt` idle, `permission_prompt` and `elicitation_dialog` blocked) lives in `internal/harness/claudecode.go`.
  OpenCode's plugin posts the states itself. Then the holder
  (for harnesses that report no state, quiet output means idle: `inject.idle_signal: quiescence`), the engine.
- If we interrupted, control marks idle itself; a human Esc may not fire Stop.

### The human side (CLI)
- `egzo message send AGENT TEXT [--interrupt] [--wait [--timeout D]]`: a request from the operator. `--wait` prints
  updates to stderr and the resolution's text to stdout, and exits 0 for `done`, 3 for `declined`, 4 for
  `failed`, 5 when the timeout passes (the message stays open).
- `egzo message ls [--kind K] [--state S] [--to X] [--from X] [--all]`: open messages (`--all` includes resolved ones) with id, from, to, kind,
  state and the start of the text. `egzo message ls --kind question` is `egzo message ls` limited to open questions.
- `egzo message reply ID TEXT [--outcome done|declined|failed]`: resolve a message addressed to a person: it
  answers a question, and closes a request an agent made of the operator.
- `egzo events`: the raw typed stream (`message`, `announced`, `fetched`, `resolved`, `unconfirmed`,
  `interrupt`, `interrupted`, `activity`, `state`, `status`, `retired`).

## Spec visibility (decided)
- Labels stay small, flat, readable (see Reconciliation model); observed state (image, env, mounts,
  networks, resources, health) comes from `inspect`: the engine is the truth.
- The full resolved spec is a redacted snapshot, not a label: `up` writes `specs/<hash>.json` to the
  control volume (history, keyed by hash), read via the operator socket. It includes per-service
  hashes, so staleness vs container labels is detectable (display problem only).
- Redaction: vault refs stored as refs; values from `${VAR}` interpolation stored as the original
  `${VAR}` text (the config hash covers the expanded value, so a changed var still recreates); the
  per-agent token never goes in a label or the snapshot.
- Drift detection is CLI-side (`egzo project up --dry-run` / `up --dry-run`): resolve the file, compare per-resource
  config hashes (Compose's model).
- Trade-off accepted: the hub's detailed spec view needs the control container running.

## Still open (design)
- The prep container's uid mapping under rootless Podman (Docker runs it as the invoking user).
- Self-ending instances (a `deadline`, an `on_done` of stop, remove or keep) enforced by the in-container
  supervisor. Not decided, so there is no policy and no default: an instance lives until someone removes it.
- A retried event that repeats its first message (spawn refuses an existing name, `send` is not idempotent).
- Which is the resolved template that spawn reads: it needs the expanded `env` values, which the redacted
  display snapshot does not keep (`${VAR}` text), so the published templates are a separate file.

## Still open (spikes)
Answered: the session backend is the pty holder (it passes the fidelity matrix; tmux was not built); `egzo-agent`
is the egzo binary baked into the harness images; OpenCode reports idle through a plugin (`session.idle`); a
Claude Code `SessionStart` arrives before its first-run dialogs are answered.
1. Per-agent `internal` networks and the CA bundle mount on rootless Podman + gVisor (the specs need a pass there).
2. Claude Code hook behaviour on a user interrupt (does Stop fire?) and the exact Notification texts per release.
3. How a harness behaves with a real model in the loop (reply capture, `say` from the Stop text).
4. Practical label size limits on docker and podman, incl. remote engines.

## specs/ (pytest)

The suite is the status report: `specs/README.md` explains the done / broken / unsupported model.
A run tests egzo on one reference platform (docker, docker-gvisor, podman, podman-rootless: what the host is like, not
what egzo is asked; docker-gvisor is Docker with `runsc` as its default runtime), named with `--engine` or chosen from the machine by `select_platform.py`; CI runs one machine
per platform (known-issues/reference-platform-ci-matrix.md). It builds its own sidecar and agent images, and needs the internet for the specs that
talk to real hosts (they fail when offline) and a registry for the image-name spec (`EGZO_SPEC_REGISTRY`).

    specs/
      spec_status.py       # plugin: per-area table, --spec-json
      select_platform.py   # picks the reference platform on a developer machine
      conftest.py          # egzo binary, project dirs (live_project, shared_project, team), engine matrix, images, cleanup
      support.py           # builders so specs read as the YAML they describe
      clitree.py           # the command tree of the real binary, so a spec's command line is checked against it
      agentapi.py          # an agent as the control sidecar sees it; a team of agents sharing one project
      proxykit.py          # the proxy specs' profiles and probe
      test_*.py            # what needs no engine: schema, egress profiles, secrets, workspaces, init, project name, meta
      project/ agent/ service/ system/ message/ workspace/ session/ proxy/ harness/ browser/
                           # what runs on the engine, laid out like the CLI; one project per module, scenarios that follow a story

`specs/README.md` lists every file and what it covers, and how the specs are written.

Security specs are the differentiator: assert secrets never appear in `engine inspect`,
agent env, agent filesystem, or logs; assert direct egress fails.

## Implementation notes (what exists, and where it differs from the drafts above)

Built and covered by specs (Docker is the engine the specs run against now; Podman works through its
compatible socket but is not exercised by the current suite): `project init|up|down|check|rotate-ca|unlock`
(`up --dry-run`, `check --resolved`), `agent spawn|ls|rm|start|stop|attach`, `message send|ls|reply`,
`workspace ls|put|get|fetch|rm`, `ps` (`--json`), `logs`, `exec`, `start|stop|restart`, `events`, `proxy log|rules`,
`system check`, `version`, plus the in-container roles `egzo control`, `egzo proxy serve`, `egzo agent run|browser`,
`egzo state`, `egzo prep`. Claude Code and OpenCode run as harness images (`harness/<name>/Dockerfile`, built on the
shared `egzo-harness-base` image (`harness/base`: Node, git, the browser, the egzo entrypoint) with `make images`); `custom` runs any image.

Decisions taken while building (reversible; each is covered by specs):
- **`up` makes the infrastructure and spawn makes the agents.** Per-agent tokens are an HMAC of the instance name
  under a random project key kept on the control volume, so they are stable and change only when the control
  volume is deleted; the control sidecar hands them out. Secrets are read before anything is created, so a
  missing secret never leaves half a project behind. `up` leaves the instances alone (it never removes or
  recreates one), re-attaches a recreated sidecar to the networks of the instances, and refuses while one is stale.
  Spawn creates the instance's network and home volume, attaches control and proxy to the network, registers
  the instance with control and binds it in the proxy, makes its worktrees (through the proxy, as the
  instance), then creates the container. If any step fails it removes what it made, in reverse, and never a worktree that holds work.
- **Staleness is judged against what `up` would publish from `egzo.yaml`.** `up` refuses before it publishes,
  so comparing with the last published templates would never find a stale instance. `ps` and `agent rm --stale`
  compute the hashes from the file. A caller without the file (the hub) compares with the published templates.
  Spawn itself always uses the published ones.
- **Published templates** are JSON (`Published`: project, directory, egzo image, the uid:gid of whoever ran `up`,
  and one `Template` per agent: the resolved agent, its egress profile, the workspaces it lists, the absolute
  prompt path and digest, the resolved image and the hash) at `PUT /templates` in the control sidecar. They hold
  no secret, only references. The redacted snapshot below is for display; the templates are what spawn reads.
- **The proxy holds profiles and bindings.** `PUT /policy` carries the egress profiles with their secret values
  (hash-compared, in memory); `PUT /agents/{name}` binds an instance's token to a profile by name and
  `DELETE /agents/{name}` removes it, so spawn and rm never touch a secret and two spawns never race over a list.
  Loading a new policy keeps the bindings. `egzo restart proxy` loads the policy again and re-binds the instances.
- **The control sidecar registers agents one at a time** (`PUT|DELETE /v1/agents/{name}`, a file each under
  `/state/agents`). Unregistering closes every request still open for the agent as failed (so `send --wait`
  returns 4) and forgets its status. Messages to an unregistered name are refused.
- **What is left of an instance counts as the instance.** If its container was removed by hand, or a spawn could
  not undo itself, the network and home volume (labelled with the instance) still make it an instance: `agent rm`
  and a second spawn of the name see it, and removing it clears its registrations too. `project down` unregisters
  every instance from control before it removes anything, and removes the control volume with the rest.
- **An agent must be registered to be heard**, not only hold a valid token: a removed instance that is still
  running, and a name nobody spawned, are refused by the agent API. Tokens are still the HMAC of the name, so a
  token leaked into a shared workspace stays valid for any future instance of that name until the control volume
  is recreated: accepted for now (a per-spawn nonce would fix it).
- **Spawn and rm wait for the project lock** (up to ten minutes, saying so) instead of failing, so several
  spawns can be started at once and take turns; the network, the first thing a spawn creates, decides a race
  between two spawns of one name (the loser exits 17). A project made before instances existed has agent
  containers without an instance label: only `down` sees them.
- **Runtime errors belong to spawn.** An unpullable harness image,
  a program that exits at once and a git mode conflict fail the spawn, and `up` succeeds.
- **Secrets reach the proxy only through `engine exec` stdin** and live in its memory. The policy
  hash covers the secret values, so rotating a secret re-pushes the policy without recreating any
  container; a restarted proxy denies everything until the next `egzo project up` reloads its policy. The
  proxy container's `inspect` never holds a secret. Placeholders travel in the same policy, next to the
  secret each one stands for; the agent environments get only the placeholder.
- **Operator APIs** are unix sockets inside the sidecar, called through `egzo <role> request METHOD
  PATH` run by exec (scratch images have no curl). Control: tokens, spec snapshots, the queue. Proxy: policy, CA rotation.
- **CA distribution is a read-only directory mount** (`/etc/egzo/ca` holding `ca.crt` and
  `ca-bundle.crt`) plus `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`, `GIT_SSL_CAINFO`
  and `NODE_EXTRA_CA_CERTS`, not an overlay of `/etc/ssl/certs/ca-certificates.crt`: a directory
  mount works the same on Docker and Podman. The CA key stays in a volume only the proxy mounts.
  `egzo project rotate-ca` makes a new CA in the proxy, publishes it and restarts the agents.
- **Proxy behaviour.** CONNECT only, port 443 only, agent identified by `Proxy-Authorization`
  (agent name and token). Hosts of services that inject a credential (or set `inspect`) are
  intercepted: TLS is terminated with a project-CA certificate whose name must match the CONNECT
  target, requests for any other host inside the tunnel get 421, the credential replaces whatever
  the agent sent, the agent is always spoken to in HTTP/1.1, responses stream unbuffered. Every
  other allowed host is a tunnel, but the proxy reads the TLS ClientHello first and refuses the
  tunnel when the server name is missing or differs from the approved host (domain fronting), and terminates
  instead of tunneling when the hello carries Encrypted Client Hello with the approved name. A CONNECT with no
  `Proxy-Authorization` at all (how a browser starts: it sends credentials once challenged) is answered 407 and logged as
  the decision `challenge`; credentials that are present and wrong are a `deny` naming the agent the client claimed, with no instance
  or profile. Plain HTTP is refused (405); agents get `HTTP_PROXY`
  too so `http://` fails loudly. Audit lines (JSON, no bodies or query strings) go to the proxy's
  stdout (`egzo proxy log [--agent]`): one event per step of a connection, with the connection's id, its protocol, the
  decision and a reason code from a fixed list ([docs/sandboxing.md](docs/sandboxing.md), [roadmap/proxy-audit.md](roadmap/proxy-audit.md)).
- **Hardening defaults.** Sidecars: read-only root filesystem, all capabilities dropped,
  `no-new-privileges`, tmpfs for `/tmp`. Agents: all capabilities dropped, `no-new-privileges`, an
  init process, writable root filesystem. Agents are always on an internal-only network, with egress only via the proxy. There is no runtime setting: containers run under the engine's default runtime, which is the host administrator's choice (gVisor included).
- **Agents run as the invoking user** (`uid:gid` of whoever runs egzo, on Docker; Podman maps users itself), so
  what they write on the host belongs to that user. Volumes are created by the engine owned by root, so a prep
  container hands each volume an agent writes to (workspaces, its home) to that user after the agent's container
  is created and before it starts: the engine hands a volume to root when it is the container's working
  directory, which would undo an earlier chown.
- **The agent's user has a passwd entry.** An image does not know the invoking user's uid, and ssh, `whoami` and git need an
  entry (ssh also takes `~/.ssh` from the entry's home, not from `$HOME`). After the agent's container is created and before it
  starts, egzo reads its `/etc/passwd` and `/etc/group` through the engine API and writes them back into the container's own
  layer: a uid the image does not know is added as `agent` with `$HOME` as its home; a uid it knows keeps its entry but takes
  `$HOME` as its home, since the home egzo mounts is the agent's home. It works for any image and any engine, including one on
  another machine. The sidecars (read-only root) do not need it.
- **Harness images** carry the egzo binary (`COPY --from=<egzo image>`), `HOME=/home/agent` (a volume
  `<project>_<agent>-home`, so `/resume` and settings survive recreating the agent) and
  `ENTRYPOINT egzo agent run --`. An integration (`internal/harness`) is pure: from the agent's definition it
  produces files (merged into what the harness already keeps in the volume), a command line and the interrupt
  key. Claude Code starts with `--permission-mode bypassPermissions`, never with `permissions.defaultMode` in the settings (which makes it ask on its first start whether to make auto mode the default; nobody is there to answer in an unattended agent, and an injected Enter would). Claude Code: `~/.claude.json` (onboarding done, each workspace trusted, a placeholder API key
  pre-approved, the `egzo` MCP server), `~/.claude/settings.json` (its bypass prompt skipped, any earlier `defaultMode` removed, hooks
  `egzo state <state> --reason <Name>`), `--model`, `--append-system-prompt` with the platform instructions + the agent's prompt,
  `IS_SANDBOX=1`. OpenCode: `opencode.json` (`permission: {"*": "allow"}`, the MCP server, model,
  instructions) and a plugin that reports `SessionStart` when it loads (OpenCode only creates a session on the
  first prompt), `UserPromptSubmit` from `chat.message` and `Stop` from `session.idle`. `permissions: default`
  undoes all of it explicitly, so an opt-out also fixes a home an earlier run configured.
- **Messages and delivery** (`internal/control`, `internal/session`; the model is in "Messages"). The control
  sidecar keeps the messages and their states in memory, rebuilt from the event log, and derives the activity
  (`starting | idle | working | blocked`) from the states the harness reports (Claude Code's hooks run `egzo state`; OpenCode's plugin posts to `/v1/state`). The holder asks
  `POST /v1/claim` when the TUI is ready and nobody has typed (read-write clients only) for `inject.human_quiet`;
  control hands over an interrupt first, otherwise the announcement line for what is queued, but only to an idle
  agent with no announcement still awaiting its fetch. The line is composed in control from fixed words and ids and
  typed as one paste; fetching is the acknowledgement; no fetch by `inject.ack_timeout` announces again, three
  times in all, then `unconfirmed`. Harnesses that report no state (`inject.idle_signal: quiescence`, the default for
  `custom`) are idle when their output has been quiet for `inject.quiescence`. `egzo ps` shows ACTIVITY, OPEN
  and WAITING next to the reported status.
- **Git workspaces** (`internal/stack/git.go`, `internal/gitprep`): the CLI decides the layout (a base volume and a
  worktree volume per instance, mounted side by side as `/workspace/<name>/.base` and `/workspace/<name>/<instance>`)
  and a prep container does the git, on the instance's own network so the clone and the fetch go through the proxy
  as that instance (`up`'s base clone, which has no instance, uses a network and a binding of its own under the
  reserved name `prep`). The relative links `git worktree add --relative-paths` writes resolve in every container that
  has the two volumes. Removing an instance deletes its worktree volume and has a prep container make the base forget
  the entry it keeps about it (only that one: a `git worktree prune` would take the other instances' worktrees,
  which are not mounted there, for gone). `egzo prep git info` answers what the CLI cannot read in a volume.
- **Sidecar image** (`Dockerfile`) is the static binary on Alpine with CA roots and git >= 2.48.
- **Label kinds** in use: `control`, `proxy`, `agent` and `prep` (short-lived containers and the network of `up`'s
  base clones, which never outlive the command).
- **Logging.** egzo makes no choice about logging: every container it creates (agents, control, proxy, the short-lived prep
  containers) gets **the engine's default log driver and options**, because that default is the sysadmin's decision
  (journald is common). egzo sets no `LogConfig`; `egzo.yaml` has no logging key (a Compose-style one may come later).
  Bounding the logs is therefore the engine's configuration, and `egzo system check` says so when the default is a
  `json-file` with no size limit. egzo does read logs back (the git jobs' output, `egzo logs`, and the agents' status,
  which the hub follows), so the default must be a driver the engine can read and follow: `json-file`, `local` and
  `journald` on Docker, `k8s-file` and `journald` on Podman, and Docker's remote drivers through its local cache.
  `egzo system check` classifies the default by name and, when the egzo image is on the engine, tries it with a container
  that writes to both streams, reports `fail` for a driver that cannot be read back (`none`, Podman's `passthrough`), and
  `warn` when it cannot tell (see `known-issues/container-logging-follows-the-engine-default.md`).
  **The control sidecar logs JSON, one object per line, on two streams.** Standard output carries only the agents'
  status: `{"type":"status","v":1,"time","project","reason","interval","agents":[{agent,status,activity,open,waiting,updated}]}`,
  written when something changes (`reason: "change"`, coalesced over 300 ms, and only when what a reader sees changed), when the sidecar
  comes up (`start`), and every `interval` seconds (30 by default, `EGZO_STATUS_INTERVAL` on the sidecar sets it) as a
  `heartbeat` that repeats the whole state, so a reader that joins late has the current state from the last line and can tell a silent sidecar
  from a quiet one. Standard error carries everything else control says, as `{"time","level","msg",...}` with a lowercase
  level. The proxy's audit lines (JSON, on its standard output) are unchanged. A client follows just the status with the
  engine's own log API (`ContainerLogs` with stdout only), with no `exec`.
- **Messaging contract** (control sidecar). Agent API on `http://control:7777` (HTTP basic: agent name and
  its token, in `EGZO_CONTROL_URL`, `EGZO_AGENT` and `EGZO_TOKEN`): `GET|POST /v1/messages`,
  `GET /v1/messages/{id}`, `POST /v1/messages/{id}/resolve|update|ask`, `GET /v1/agents`, `POST /v1/status`,
  `/v1/state` (`{state, reason?}`), `/v1/activity`, `/v1/claim`, and MCP at `/mcp` with the tools `list_messages`, `get_message`,
  `resolve`, `update`, `ask`, `message`, `status` and `agents`. An agent can only act as itself and only sees
  messages addressed to it (an unknown id and someone else's answer alike: "no such message"), and the agent port
  serves no operator verb. Operator API (unix socket, exec only): `GET /events?after=&agent=&follow=1`,
  `POST /messages` (`interrupt: true` asks for the current turn to be stopped), `GET /messages?agent=&kind=&all=1`,
  `GET /messages/{id}`, `POST /messages/{id}/resolve`, `GET /agents`, `PUT|DELETE /v1/agents/{name}`, `PUT|GET /templates`. Event types: `status`,
  `message` (with `data.to`, `kind`, `re`, `hops`), `announced`, `fetched`, `resolved` (with the resolution's text and
  `data.outcome`), `unconfirmed`, `interrupt`, `interrupted`, `activity`, `state`, `retired` (an agent was unregistered: its status is forgotten).
- **Spec snapshot** is stored on the control volume at every `up` that changes it, keyed by hash,
  with the resolved config, the label contract version and the config hash of every container.

Open, in the order they matter:
1. **pi** is accepted as a harness name but has no image or integration. `custom` covers it for now.
2. The engine matrix beyond Docker (rootless and rootful Podman, gVisor) is not part of the current spec run;
   the specs were written for it and need a pass on those hosts. Rootful Podman loses outbound connectivity
   intermittently (`known-issues/rootful-podman-intermittent-egress.md`).
3. `request_secret_access`, the optional mount of the user's own `~/.claude` settings.
4. A real model call is never made by the specs; the end-to-end behaviour of a harness with a model (that a
   reply comes back, that the Stop text reaches `say`) is unverified.

## Not in core: hub, web UI, chat clients

Egzo core does not implement a hub, a web UI or chat integrations (Discord and the like); they are planned as a
separate `egzo-hub` deliverable (see `roadmap/hub.md`). What that means for core:
- The project YAML and the CLI are the only way to create or change a project. Nothing else writes config.
- Secrets are CLI-only. No user identities live in YAML, ever; who may talk to agents is not declared there.
- Core exposes what another client needs: the `ai.egzo.*` label schema, the control sidecar's operator API
  (reached by `engine exec` only) and its status log (followed through the engine's log API), attach as `engine exec`, the message and event contract, and importable Go
  packages (list, inspect, read-spec, start/stop, attach, send) rather than logic inside CLI commands.

## Hardening decisions (code review of the first implementation)

- **Names.** Agents, workspaces: 1 to 63 of `[a-z0-9_-]`, starting with a letter or digit. Reserved: agents
  `control`, `proxy`, `prep`; workspaces `control`, `ca`, `ca-private`, `egress` and `<agent>-home`, which
  would otherwise be egzo's own volumes (a workspace named `control` would hand an agent the key every token
  derives from). Instance names follow the same rule and may not be a template name, `control-1` or `proxy-1`, or end in `-home`. Settings that would do nothing (`proxy.audit`, `control.tools`,
  `agent.tools`) are not in the schema.
- **Git sources** carry no credentials in the URL (the proxy injects them) and a real branch name. Git run by egzo
  ignores hooks and file-system monitors of the repository it works in, which every agent that lists the workspace can write.
- **Proxy.** It connects only to public addresses (checked on the address a name resolved to), caps connections per agent
  and in all, closes idle connections, terminates Encrypted Client Hello instead of tunneling it, keeps bytes sent right behind CONNECT, truncates
  logged values, logs bytes and duration of a tunnel, bounds its certificate cache, and answers 503 "run `egzo project up`" when
  it has no policy; `egzo start|restart proxy` reloads the policy. The CA is only ever replaced by an explicit rotation.
- **Control.** Verbs that write to the log have budgets per agent; state reports are reduced to a few named fields (plus
  the line egzo typed); the log is compacted past 64 MB; the project key is created once, atomically, and never replaced;
  snapshots are verified against their hash and retired past 50; every recipient has at most 20 open requests; the
  holder releases an agent that sits at "working" while its screen is silent (`if: working`), and announces itself with
  `if_unset`, so it never undoes the harness's own first report.
- **Containers.** Sidecars run as 65532 with memory and process limits; every container uses the engine's default logging (egzo sets none, so the sysadmin's choice stands); agents
  have a process limit and, with a memory limit, no extra swap; egzo run as root still gives agents uid 1000; rootless
  Docker is treated like Podman. Agents start only after the sidecars are healthy and attached. One command changes a
  project at a time (the engine-side lease `<project>.lock`).
- **Terminal.** Text an agent wrote is made harmless (`internal/termsafe`) before `ps`, `messages` and `send --wait`
  print it; `attach` turns off the modes a TUI left on when the user detaches.
