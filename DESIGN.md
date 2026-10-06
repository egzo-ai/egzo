# egzo — design draft (v0, schema + UX)

## Principles
1. No daemon. The CLI is stateless; the container engine (docker/podman) is the source of truth.
2. Everything is a container: agents, proxy, vault, control plane.
3. Secrets live only in sidecars. Agent containers never hold real credentials.
4. Each agent sits on its own internal network with no default route; the proxy is its only egress
   and the control sidecar its only peer. Agents cannot reach each other directly.
5. `specs/` (pytest, black-box, drives the real binary) is the specification.

## UX requirement (top priority, overrides convenience elsewhere)

The Claude Code TUI is today's gold standard and is what users want. Egzo must never degrade it.
Egzo's value is structure around it: several agents, competing harnesses/frameworks side by side,
and secure workflows, all while the user keeps the Claude Code experience they know and love.

Rules:
1. **Minimal fuss to a working Claude Code.** From zero to a sandboxed Claude Code prompt is a
   tiny explicit `egzo.yaml` (scaffolded by `egzo init`), then `egzo up` and `egzo spawn AGENT --attach`. There is
   no implicit project and no implicit mounting of the user's directory: everything an agent can
   see is declared in the file.
2. **The TUI is untouched.** What the user sees is the real `claude` TUI, not an Egzo
   re-rendering. tmux (or any pty layer) must be invisible: no status bar, no prefix key, no
   mangled keys, mouse, scrollback, clipboard (OSC52), truecolor, Shift+Enter or resize.
   Slash commands, plan mode, `/resume`, skills, hooks and the user's own CLAUDE.md all work.
   A fidelity spec (pexpect) guards this; a regression there is a release blocker.
3. **No first-run friction.** No theme/login/trust/bypass-permission prompts inside the sandbox
   (v1 learned this: seed `~/.claude.json` onboarding + workspace trust, pre-allow the proxy-needed
   hosts). Authentication is a one-time `egzo secrets set` or reuse of the host's login; the
   agent never holds the real credential (proxy injects it).
4. **Familiar config carries over.** Optionally mount/copy the user's `~/.claude` settings,
   CLAUDE.md, skills and MCP servers into the agent (read-only, explicit opt-in in YAML) so it
   feels like their own Claude Code.
5. **Egzo features are additive.** Multi-agent, messaging, status, policies appear as
   extra tools and `egzo` subcommands, never as changes to how `claude` itself behaves. Anything
   Egzo needs to tell the user goes out-of-band (`egzo ps`, notifications), not into the TUI.
6. **Same experience for other harnesses.** OpenCode/pi/etc. also run as their native TUI, so
   users can compare frameworks by attaching to each in the same way (`egzo attach <agent>`).
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
   - Containment specs must not depend on the harness: they assert the sandbox holds even if the
     agent is fully compromised (no egress except via proxy, no secrets, no host access).
   - Prompts that remain are real questions to a human (e.g. Claude's AskUserQuestion), surfaced
     via the `ask` path (a message to a person), never permission gates.
   - Opt-out (`permissions: default`) exists per agent for users who want prompts.

Level 1 example:

```yaml
vaults:
  main:
    backend: env
    secrets:
      ANTHROPIC_API_KEY: { from: env:ANTHROPIC_API_KEY }
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
renaming a project creates a new one and orphans the old containers. `egzo config` always prints
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
- `egzo agent` and `egzo hook` are the in-container roles (pty holder, hooks), the same binary, copied into
  every harness image.
- Config file `egzo.yaml`, env vars `EGZO_*`, per-project directory `.egzo/`.
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
`egzo up` = list by label -> diff config-hash -> create/recreate/remove the **infrastructure** and publish the
templates (agent instances are not reconciled: they are spawned, and marked stale when their template moves).
Idempotent. `up` always
returns once converged: there is no foreground mode and no `-d` (agents are TUIs you `attach` to,
nothing streams to the `up` terminal). Preview with `--dry-run` (long form only, no short flag).
Caveat: labels are immutable, so *runtime status* (starting/idle/working/blocked) is NOT stored in labels.
It is held by the `control` sidecar (in-memory + volume) and queried via `engine exec`.
Existence/config = engine; liveness/status = control sidecar. Engine health = container health.

## Example project file (egzo.yaml)

```yaml
version: 1
name: myproj

vaults:
  main:
    backend: env               # env | file | sops | pass | 1password (pluggable)
    secrets:
      ANTHROPIC_API_KEY: { from: env:ANTHROPIC_API_KEY }
      GITHUB_TOKEN:      { from: file:~/.secrets/gh }

proxy:                         # infrastructure of the egress sidecar
  image: ghcr.io/egzo-ai/egzo  # optional override; default is the all-in-one egzo image (`egzo proxy`)

egress:                        # named profiles; an agent links to one (omitted => `default`)
  default:                     # undefined `default` = deny everything
    allow: [platform.claude.com, "*.pypi.org"]   # hosts/globs reachable, no credential
                               # allow: ["*"] grants full internet access (still via the proxy)
    services:                  # built-in services (anthropic, github, ...) need only a secret
      anthropic: main/ANTHROPIC_API_KEY
      github: main/GITHUB_TOKEN
  operator:
    extend: default            # builds on another profile
    services:
      deploy-api:              # an object defines a custom service
        hosts: [api.deploy.example.com]
        inject: { header: Authorization, value: "Bearer {secret}" }
        secret: main/DEPLOY_TOKEN

workspaces:                    # declared like compose volumes; referenced by name from agents
  repo:
    git: { url: https://github.com/me/repo.git, branch: main }   # https only
    # path: ./.egzo/workspaces/repo   # where clones/worktrees live on the host (default shown)
    # mode: clone              # git only: clone (default, one independent clone per agent)
                               #   | worktree (optimization, see Workspaces) | shared (one checkout)
  scratch: {}                  # no source: a shared system volume

agents:                        # templates: nothing runs until `egzo spawn <name>`
  coder:
    harness: claude-code       # claude-code | opencode | pi | custom
    image: ghcr.io/egzo-ai/egzo-harness-claude-code
    workspaces: [repo, scratch] # at /workspace/<name>; 2+ => workdir is /workspace
    egress: default            # profile name; omitted => default
    # workdir: repo            # optional override (name under /workspace, or a path)
    model: claude-sonnet-5-5
    prompt: ./prompts/coder.md
    resources: { cpus: 2, memory: 4g, pids: 4096 }   # pids default 4096
    runtime: runsc             # OCI runtime, as in Compose (e.g. gVisor); engine default if omitted
    permissions: bypass        # bypass (default) | default
    env: { FOO: bar }          # non-secret only; validated, secret-looking values rejected

  reviewer:
    harness: opencode
    workspaces: [./docs:ro]    # host dir, read-only (a git workspace cannot be :ro)

control: {}                    # orchestrator MCP + status sidecar (always present); no settings yet

# No bridges and no users here. Chat integrations (Discord, ...) are hub features, and who may
# talk to agents is never declared in project YAML (see Future: egzo-hub).
```

## CLI UX

    egzo init [--harness claude-code]      # scaffold egzo.yaml
    egzo config                            # resolved/validated YAML (like compose config)
    egzo up                                # reconcile the infrastructure, publish templates; --recreate; --dry-run. No -d: always returns
    egzo spawn TEMPLATE [NAME] [-m MSG] [--attach|--wait]   # an agent from a template (see Templates and instances)
    egzo rm NAME... | prune                # remove instances
    egzo diff                              # file vs running (same algorithm as up --dry-run)
    egzo down [--volumes]
    egzo start|stop|restart <svc>          # operate on existing containers (no reconcile)
    egzo ps                                # engine state + status from control sidecar
    egzo attach <agent>                    # native TUI; detach key configurable (default Ctrl-])
    egzo logs [-f] <svc>
    egzo send <agent> "message" [--wait]   # a request from the operator; --wait prints the resolution
    egzo messages | answer <id> <text>     # open messages; answer or close one addressed to a person
    egzo exec <svc> -- cmd
    egzo secrets ls|set|rm                 # vault management, never prints values
    egzo proxy log|rules                   # audit trail
    egzo ca rotate                         # new project CA; restarts agents
    egzo doctor                            # engine, rootless, gVisor, network checks

## Decisions
- Language: Go.
- Engine access: Docker Engine API (Go docker client). The engine is whatever `DOCKER_HOST` points at
  (default `/var/run/docker.sock`), like the docker CLI: no probing, no engine setting in egzo.yaml.
  Podman works through its Docker-compat socket (`podman.socket`); its API cannot select a container's
  `runtime:`, so egzo refuses to start an agent that asks for one there (known-issues/podman-gvisor-unsupported.md). Never mount the socket into agent
  containers. Engine matrix in specs.
- No daemon. Everything runs in containers except the `egzo` CLI.
- Harness control is TUI-first (Scion-style). The native TUI always runs in the agent container
  behind a pty layer (the `Session` backend, see Session backend below); there is never a mode
  switch. ACP was the v1 primary channel and is dropped: it cannot run concurrently with the TUI on
  one session. It may return later as an optional driver for headless agents only.
  - Human -> agent: a message in the control sidecar; a short fixed line naming it is typed into the terminal
    (see "Messages"), and the agent fetches it with a tool.
  - Agent state -> orchestrator: harness hooks call the control sidecar's agent API.
  - Agent -> human: MCP `egzo` tools (`resolve`, `update`, `ask`, `message`, `status`; see "Messages").
  - Attach (CLI and web UI) = `engine exec -it` into the Session backend.
- Specs: pytest in `specs/`. A spec for an unbuilt feature fails (the failing specs are the todo list); strict
  xfail is only for platforms that lack a feature.
- Reuse from v1 (/home/cedric/egzo): harness adapter Render logic (pure, golden-tested), proxy/gate
  lessons (todos/0002), Claude onboarding/trust seeding in ~/.claude.json.

## Workspaces (decided)
- Declared at top level, Compose-style; an agent references them in `workspaces:` as a list of
  `name[:ro]`. An undeclared bare name is an error (as in Compose). Declared with no source = a
  shared system volume (`<project>_<name>`), shared by every agent that lists it.
- Inline host paths, Compose short-syntax style: `./path[:ro]` (relative to the egzo.yaml
  directory) and `/abs/path[:ro]`. These are host-bound: the hub warns on remote engines.
  Nothing is ever mounted from the host unless the file says so.
- Mount point is uniform for all agents: `/workspace/<name>` (declared name; basename for inline
  paths, collision = error).
- Working directory: exactly one workspace => that workspace's directory; two or more => `/workspace`.
  `workdir:` on the agent overrides (a workspace name or a path). Agents with none: nothing mounted.
- Git workspaces (`git: {url, branch}`) have `mode`: `clone` (default; each agent gets its own
  independent clone, so one agent cannot corrupt another's `.git`), `worktree` (optional
  optimization: per-agent worktrees of a shared base repo; the base `.git` is then mounted
  read-write into agents, so shared-`.git` risk is accepted explicitly), `shared` (one checkout used
  by every agent that lists it).
- `:ro` on a git-defined workspace is a validation error: git needs write access. For read access to
  files use a host path (`./path:ro`) or a `shared` workspace; to see another agent's work, fetch what it pushed.
- Whose a workspace is follows its mode (see "Templates and instances"): `clone` and `worktree` belong to the
  instance; `shared`, system volumes and host paths belong to the project. There is no way to mount another
  agent's checkout: a template cannot name an instance that does not exist yet, and agents share only what a
  workspace declares shared. Workspaces outlive agents; `down --volumes` removes volumes, never host binds.
- Git sources are HTTPS only (like Scion): `git: {url, branch}` with an `https://` URL; ssh and
  local paths are validation errors. A local repository on the host is not a git source: it is just
  a directory, mounted with the inline `./path` form (no clone, no git semantics).
- Location: each git workspace has an optional `path:` (relative to the egzo.yaml directory, or
  absolute) = the host directory under which egzo creates `<agent>/` (clone or worktree) or
  `shared/`. Default: `.egzo/workspaces/<name>` inside the project directory, so work lives with
  the project. `path:` is host-bound (the hub warns on remote engines). When the project directory
  is itself a git repo, `.egzo/` must be git-ignored (`egzo init` scaffolds that; `egzo doctor`
  warns).
- Who clones: the CLI decides layout and policy (resolves paths, decides which agents need which
  clone, skips what exists); a short-lived **prep container** (pinned image with git >= 2.47, run
  with the agent uid, the target directories bind-mounted, egress via the project proxy only) does
  the git work. The CLI creates it through the engine API and removes it afterwards. Reasons: no
  host git or version dependency, correct file ownership under rootless engines, same behaviour
  for local and remote engines, and no credentials on the CLI: the proxy injects the HTTPS token
  (the `github` service bound to a vault secret). Agents push the same way, so no agent holds
  a git credential. Finished work is pushed by the agent itself, as in Scion.
- When: during `egzo up`, before creating an agent that needs it, only if its target does not
  exist. Idempotent; an existing clone is never modified. `mode` changes do not convert existing
  clones (`up` reports the mismatch and refuses until the directory is removed).
- Modes in detail: `clone` = `<path>/<agent>/`, one independent clone per agent (optimization
  later: a same-filesystem base mirror + `git clone --local`, which hardlinks objects, keeps objects
  independent and avoids alternates path coupling). `worktree` = one base clone plus a worktree per
  agent created with `git worktree add --relative-paths` (needs git >= 2.47); the agent mounts only
  its own worktree and the base, in a layout that keeps the relative link valid (never the whole
  workspace directory, which would expose every agent's worktree). `shared` = `<path>/shared/`.
- Safety: `down` never deletes workspace directories (they hold unpushed work). Removal is explicit
  (`down --workspaces`, with confirmation) and first checks each clone for uncommitted or
  unpushed work using the prep container.
- Harness integrations map extra workspaces to the harness (e.g. Claude Code additional
  directories) and seed workspace trust for each directory.

## Templates and instances (decided, not built)

**Dropped by this model:** `depends_on` (nothing starts at `up`, so there is nothing to order), `egzo up AGENT...`
(there are no agents to select) and the cross-agent workspace reference `agent/workspace:ro` (a template cannot name
an instance). All three are removed from the schema, the CLI and the specs, not rejected.

An entry under `agents:` is a **template**: it says how to run an agent and runs nothing. `egzo up` brings up
the project's infrastructure (networks, volumes, proxy, control), validates the file and **publishes the
resolved templates** to the control volume. An **instance** is an agent made from a template with `egzo spawn`.
Spawn does not read `egzo.yaml`: it uses the templates `up` last published, so an instance is always what `up`
applied, and only the CLI's `up` changes templates. A spawn from the CLI, the hub or any other tool is the same
operation (an importable package, not CLI code).

### The experience

    egzo up                                       # infrastructure + publish templates; starts no agent
    egzo spawn coder                              # instance `coder-1` (auto name), returns
    egzo spawn coder issue-412 -m "Fix #412"      # named, first message sent
    egzo spawn coder issue-412 -m "..." --attach  # ...and attach to it
    egzo spawn reviewer pr-88 -m "Review PR 88" --wait [--timeout D]   # exit codes as `send --wait`
    egzo ps                                       # instances, with TEMPLATE, ACTOR, AGE, STALE
    egzo attach issue-412 | send | logs | exec | start | stop | restart   # by instance name
    egzo rm issue-412 [--workspaces] [--force]
    egzo prune [--stopped] [--stale] [--older-than D] [--dry-run] [--yes]

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
  home volume, workspace preparation (a clone or worktree of its own, named after the instance), the container,
  and two registrations: control learns the agent (one verb to add and one to remove an agent, never a whole
  list) and the proxy binds the instance's token to the template's egress profile. **Spawn never reads a
  secret**: `up` loads the egress profiles, with their secret values, into the proxy; the binding carries only
  the token and the profile's name. That is what lets the hub, which never touches secrets, spawn. Spawn with
  `-m` then sends the message; it waits in the queue until the harness is ready, as every message does.
  A spawn that fails halfway removes what it created, and never a checkout.
- **`rm`** removes the instance's container, network and volumes, never the checkouts. With `--workspaces` it
  also removes them, after the same inspection `down --workspaces` does (unpushed commits, stashes, detached
  work), and refuses without `--force` when something would be lost. A running instance needs `--force`.
- **`down`** removes every instance, like every other resource of the project, and leaves workspace
  directories alone.
- **Stale.** An instance whose template changed since it was spawned, or whose template no longer exists, is
  **stale**: its `ai.egzo.template-hash` differs from the hash of the template in `egzo.yaml` (the hash covers
  everything that went into the instance: the agent, its egress profile, the workspaces it lists, its prompt's
  content, its image). `up` refuses before it publishes, so the file, not the last published templates, is what
  an instance is compared with. Nothing restarts a stale instance. `ps` marks it, `prune --stale` clears it, and
  `up` refuses while one exists, naming them, so the user stops them first. `up --dry-run` prints `stale: NAME`
  for each and says it would refuse.
- **Reaping.** egzo has no reaper. `prune` removes stopped, stale or old instances (age is the container's
  creation time); `ps --json` gives a script or a timer what it needs to decide. `prune` asks first, as
  `down --workspaces` does, unless `--yes`; `--dry-run` lists and changes nothing.
- **Labels.** `ai.egzo.service=<template key>` (so `ps` groups by it), `ai.egzo.instance=<name>`,
  `ai.egzo.actor=<who>` (`operator` from the CLI; `user:<id>` or a service from another tool) and
  `ai.egzo.template-hash=<hash>` on the instance's container, network and home volume.
- **Workspaces follow their mode.** `clone` and `worktree` checkouts belong to the instance (a directory
  named after it); `shared` checkouts, system volumes and host paths belong to the project and are shared by
  every instance that lists them.

### For other tools (hub, broker, scripts)
`spawn`, `ps`, `rm` and `prune` take `--json`. The hub calls the same package; a broker can run the CLI against
the engine. A webhook becomes `spawn` followed by `send` (or `spawn -m`). Retrying a webhook is safe for the
spawn and not for the message. Spawning, stopping and removing instances of declared templates is **operating**,
not creating or changing a project: the hub's rule that only YAML and the CLI create projects holds, because the
templates are declared and published by `up`.

## Control plane (decided)
- The control sidecar holds status, event log, message queue and spec snapshots (volume).
- Agent -> control: HTTP on the container network. MCP over streamable HTTP; hooks and a stdio-MCP
  fallback go through the small static `egzo-agent` binary (also the candidate pty holder).
- One network per agent; control and proxy join each agent's network, agents never share one, so
  agent-to-agent traffic only exists through control and policy. `NO_PROXY` covers the control host.
- Per-agent token (generated by `up`, injected as env) identifies the agent to control. It is an
  agent identity, not a provider secret; compromising an agent only lets it act as itself.
  The same token is the agent's proxy credential (`HTTPS_PROXY=http://agent:<token>@proxy:3128`).
- Control has two listeners: an **agent API** (TCP, token auth, agent verbs only: the message tools,
  status, agents, hook ingest) and an **operator API** on a unix socket inside the container,
  reached only via `engine exec` by the CLI/hub (read spec, send and list messages, answer, interrupt).
  Operator verbs are never on the network.
- To verify in a spike: per-agent `internal` networks on rootless podman; why not unix sockets:
  gVisor probably blocks connecting to host-created unix sockets via bind mounts.

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
- A **service** defines *how* to talk to something: `hosts` (required), optional `inject`
  (`header`, `value` containing `{secret}`; without `value` the raw secret is the header value),
  optional `secret`. A service without `inject` is a pure allowlist.
- `services` entries have two forms: a **string** `<vault>/<secret>` gives the secret for a service
  that already resolves (inherited via `extend`, or built-in); an **object** defines the service
  (hosts, optional inject, optional secret). There is no null form.
- Name resolution for a service: the profile's own definition, then the `extend` chain, then the
  built-ins. Binding a service that has `inject` with no secret is a validation error.
- `extend` merge: `allow` lists are unioned; `services` are merged by name with the child winning:
  a string overrides only the secret of the inherited definition (hosts and inject kept), an
  object replaces the definition entirely. A child cannot remove anything it inherits. One parent
  only; cycles and unknown parents are errors.
- **Built-in services** ship with egzo as data. `anthropic-oauth` is for a Claude subscription token (`claude setup-token`, `sk-ant-oat…`), sent as `Authorization: Bearer`; the agent then gets `CLAUDE_CODE_OAUTH_TOKEN` instead of `ANTHROPIC_API_KEY`. A token bound to `anthropic`, or an API key to `anthropic-oauth`, is refused at `up`. Initially `anthropic`: `api.anthropic.com` +
  `x-api-key`; `github`: `github.com` and `api.github.com` + `Authorization: Bearer`), more later
  (openai, google-ai, package registries). An object with a built-in's name replaces it.
  `egzo config` prints each profile fully resolved.
- Reachability for an agent = its profile's `allow` plus the hosts of its services (after extend).
- Validation: unknown profile, unknown parent, cycle, unknown service name (suggest close names),
  unknown vault secret, two services in one profile sharing a host with different injection
  (ambiguous), and a warning when an agent's profile cannot reach its harness's provider host.
- The proxy config is compiled per profile and applied per agent through the per-agent proxy
  credential; changing a profile changes the proxy's config hash (proxy recreated, CA kept).
- Harness integrations may need credential-free hosts (Claude Code: `platform.claude.com`). For now
  these are declared explicitly in `allow`; whether integrations declare them is open.
- Unverified: that GitHub accepts `Authorization: Bearer` for git over HTTPS (spike).
- Later: scope a service by path or method (e.g. token only for `/repos/acme/*`).

## Egress proxy and TLS (decided)
- MITM only for hosts of services with credential injection in the agent's profile. Everything else allowed
  is a CONNECT tunnel with host/SNI match (domain-fronting protection). Optional `inspect: true` on a
  service intercepts it to audit paths (off by default).
- One CA per project, generated by the proxy on first start (ECDSA P-256), private key only in a
  volume mounted into the proxy; never in an agent, the CLI or the hub. Persistent across proxy
  recreation (regenerating would force agent restarts); `egzo ca rotate` is explicit. Leaf certs are
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

## Session backend (decided: the pty holder, built)
The holder is `egzo agent run`, the entrypoint of every egzo harness image (`internal/session`). It runs the
harness on a pty inside the agent container, serves clients on a unix socket in the container, and is a raw
pass-through: no tmux, no screen model, nothing drawn around the TUI, so the user's own terminal keeps its
scrollback, clipboard (OSC 52), hyperlinks, truecolor, Shift+Enter and mouse. What it does add:
- clients are `egzo agent attach` run through `engine exec` with a terminal (`egzo attach`): any number of
  read-write clients, and read-only observers (`--read-only`) whose keys are ignored and never count as a
  human typing; the detach key is the client's (default Ctrl-], `--detach-keys`), and the engine's own
  Ctrl-P Ctrl-Q detach is switched off for terminals, because it would swallow keys of a TUI;
- a client starts with the last 64 KB the program wrote, then the program is asked to repaint at the client's
  size (a real size change, or a one-column wobble when the size is the same: the kernel only signals on a change);
- it types injected messages (bracketed paste, then Enter) and the interrupt key, and watches the output for
  quiescence-based harnesses.
The fidelity matrix is `specs/test_session.py`, run against a stand-in TUI (`specs/fixtures/fake-tui.sh`) so
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
- **Kinds:** `request` (`message`, or `egzo send`), `question` (`ask`), `resolution` (what `resolve`
  sends back to the sender: the result of a request, or the answer to a question; it carries an `outcome`),
  `update` (`update`: progress on a request).
- **States** of a request or question: `queued` (written), `announced` (the line was typed into the
  recipient's terminal), `fetched` (the recipient fetched it), `resolved` (with an outcome: `done`,
  `declined` or `failed`; terminal: a requester who is unhappy sends a new message). A resolution is closed
  once fetched. An update is passive: it is never announced, only visible (`list_messages`, `egzo messages`,
  `egzo send --wait`). A message to a person is not announced either: it waits in `egzo messages` until
  answered (`egzo answer`).
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
  again, safely, up to three times in all; then the message is `unconfirmed`, shown by `egzo messages`, and
  still found by `list_messages`. The `UserPromptSubmit` hook no longer acknowledges anything.
- **Interrupt:** `egzo send --interrupt` makes the holder send the harness's interrupt key first, as before.
- **Limits** (a loop between agents is easy to start): at most 20 open requests per recipient, a thread is
  at most 8 messages deep (`re` adds one hop), and a sender may send at most 30 messages a minute.
  Past a limit the tool answers with an error that says which.

### Agent status: what the harness does, and what the agent owes
- **Activity** (from hooks, the holder and the engine): `starting`, `idle`, `working`, `blocked` (the
  harness is stuck on its own UI: a permission dialog, a login: something only a person at the terminal can
  answer), `stopped` (the container is not running). `working`, `blocked`, `starting` and `stopped` hold back
  announcements; messages for a stopped agent wait and are announced when it is ready again.
- **Overlay**, derived from messages and never a gate: `open: N` (requests and questions fetched and not
  resolved) and `waiting` (it has asked a question that is not answered). An agent that asked something
  usually ends its turn and goes `idle`, so `waiting` must not hold announcements back, or the answer
  could never arrive.
- `egzo ps` shows ACTIVITY, OPEN and WAITING next to the status line.
- Signals: harness hooks (Claude Code: SessionStart/Stop idle, UserPromptSubmit and the tool hooks working,
  Notification blocked unless it says the agent waits for input; OpenCode through its plugin), the holder
  (for harnesses without hooks, quiet output means idle: `inject.idle_signal: quiescence`), the engine.
- If we interrupted, control marks idle itself; a human Esc may not fire Stop.

### The human side (CLI)
- `egzo send AGENT TEXT [--interrupt] [--wait [--timeout D]]`: a request from the operator. `--wait` prints
  updates to stderr and the resolution's text to stdout, and exits 0 for `done`, 3 for `declined`, 4 for
  `failed`, 5 when the timeout passes (the message stays open).
- `egzo messages [--agent A] [--all]`: open messages (`--all` includes resolved ones) with id, from, to, kind,
  state and the start of the text. `egzo questions` is `egzo messages` limited to open questions.
- `egzo answer ID TEXT [--outcome done|declined|failed]`: resolve a message addressed to a person: it
  answers a question, and closes a request an agent made of the operator.
- `egzo events`: the raw typed stream (`message`, `announced`, `fetched`, `resolved`, `unconfirmed`,
  `interrupt`, `interrupted`, `activity`, `hook`, `status`).

## Spec visibility (decided)
- Labels stay small, flat, readable (see Reconciliation model); observed state (image, env, mounts,
  networks, resources, health) comes from `inspect`: the engine is the truth.
- The full resolved spec is a redacted snapshot, not a label: `up` writes `specs/<hash>.json` to the
  control volume (history, keyed by hash), read via the operator socket. It includes per-service
  hashes, so staleness vs container labels is detectable (display problem only).
- Redaction: vault refs stored as refs; values from `${VAR}` interpolation stored as the original
  `${VAR}` text (the config hash covers the expanded value, so a changed var still recreates); the
  per-agent token never goes in a label or the snapshot.
- Drift detection is CLI-side (`egzo diff` / `up --dry-run`): resolve the file, compare per-resource
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
what egzo is asked), named with `--engine` or chosen from the machine by `select_platform.py`; CI runs one machine
per platform (known-issues/reference-platform-ci-matrix.md). It builds its own sidecar and agent images, and needs the internet for the specs that
talk to real hosts (they fail when offline) and a registry for the image-name spec (`EGZO_SPEC_REGISTRY`).

    specs/
      spec_status.py       # plugin: per-area table, --spec-json
      select_platform.py   # picks the reference platform on a developer machine
      conftest.py          # egzo binary, project dirs, engine matrix, images, cleanup
      support.py           # builders so specs read as the YAML they describe
      test_meta.py         # the status model itself
      test_schema.py       # egzo.yaml validation, no users in files, no secrets in output
      test_project_name.py # name resolution, same-name-other-directory refusal
      test_workspaces.py   # mounts, workdir rule, https git, modes, paths, cross-agent refs
      test_egress.py       # profiles, services, built-ins, extend, reachability warnings
      test_init.py         # egzo init
      test_labels.py       # ai.egzo.* contract, label size/format, no secrets in resources
      test_up_down.py      # up/down, --dry-run, no -d, idempotency, workspace dirs survive down
      test_agents.py       # one container per agent, own internal network, isolation, workspaces
      test_proxy.py        # injection, deny unless allowed, auth, per-agent policy, CA, audit
      test_snapshot.py     # spec snapshot on the control volume
      test_daily.py        # logs, exec (with a terminal), start/stop/restart, proxy log
      test_messaging.py    # status, say, questions, queue and inbox, hooks, typed event stream

    test_session.py      # attach and the session fidelity matrix (P0 rows block release)
    test_injection.py    # states, human-quiet rule, header ack, no blind retry, queue combine, interrupt
    test_harnesses.py    # Claude Code and OpenCode images: bypass, first-run state, hooks, MCP, real TUIs
    test_git_workspaces.py # prep container: clone/shared/worktree, idempotency, down --workspaces safety
    test_operations.py   # secrets, doctor, diff, ca rotate, proxy rules
    test_spawn.py        # spawn: templates, names, exit code 17, first message, --wait, labels, errors
    test_instances.py    # ps, rm, prune, stale instances, down, registration in control and proxy
    test_images.py       # harness image names and pulling them from a registry

Security specs are the differentiator: assert secrets never appear in `engine inspect`,
agent env, agent filesystem, or logs; assert direct egress fails.

## Implementation notes (what exists, and where it differs from the drafts above)

Built and covered by specs (Docker is the engine the specs run against now; Podman works through its
compatible socket but is not exercised by the current suite): `init`, `config`, `up` (`--dry-run`,
`--recreate`), `spawn`, `rm`, `prune`, `diff`, `down` (`--volumes`, `--workspaces`), `ps` (`--json`), `logs`, `exec`, `attach`, `start|stop|restart`,
`send` (`--interrupt`), `events`, `questions`, `answer`, `secrets ls|set|rm`, `proxy log|rules`, `ca rotate`,
`doctor`, `version`, plus the in-container roles `egzo control`, `egzo proxy serve`, `egzo agent run|attach`,
`egzo hook`, `egzo prep`. Claude Code and OpenCode run as harness images (`harness/<name>/Dockerfile`, built on
the egzo image with `make images`); `custom` runs any image.

Decisions taken while building (reversible; each is covered by specs):
- **`up` makes the infrastructure and spawn makes the agents.** Per-agent tokens are an HMAC of the instance name
  under a random project key kept on the control volume, so they are stable and change only when the control
  volume is deleted; the control sidecar hands them out. Secrets are read before anything is created, so a
  missing secret never leaves half a project behind. `up` leaves the instances alone (it never removes or
  recreates one), re-attaches a recreated sidecar to the networks of the instances, and refuses while one is stale.
  Spawn creates the instance's network and home volume, attaches control and proxy to the network, registers
  the instance with control and binds it in the proxy, makes its git checkouts (through the proxy, as the
  instance), then creates the container. If any step fails it removes what it made, in reverse, and never a checkout.
- **Staleness is judged against what `up` would publish from `egzo.yaml`.** `up` refuses before it publishes,
  so comparing with the last published templates would never find a stale instance. `ps` and `prune --stale`
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
- **The control sidecar registers agents one at a time** (`PUT|DELETE /agents/{name}`, a file each under
  `/state/agents`). Unregistering closes every request still open for the agent as failed (so `send --wait`
  returns 4) and forgets its status. Messages to an unregistered name are refused.
- **Runtime errors belong to spawn.** A Podman project that asks for an OCI runtime, an unpullable harness image,
  a program that exits at once and a git mode conflict fail the spawn, and `up` succeeds.
- **Secrets reach the proxy only through `engine exec` stdin** and live in its memory. The policy
  hash covers the secret values, so rotating a secret re-pushes the policy without recreating any
  container; a restarted proxy denies everything until the next `egzo up` reloads its policy. The
  proxy container's `inspect` never holds a secret.
- **Operator APIs** are unix sockets inside the sidecar, called through `egzo <role> request METHOD
  PATH` run by exec (scratch images have no curl). Control: tokens, spec snapshots, the queue. Proxy: policy, CA rotation.
- **CA distribution is a read-only directory mount** (`/etc/egzo/ca` holding `ca.crt` and
  `ca-bundle.crt`) plus `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`, `GIT_SSL_CAINFO`
  and `NODE_EXTRA_CA_CERTS`, not an overlay of `/etc/ssl/certs/ca-certificates.crt`: a directory
  mount works the same on Docker and Podman. The CA key stays in a volume only the proxy mounts.
  `egzo ca rotate` makes a new CA in the proxy, publishes it and restarts the agents.
- **Proxy behaviour.** CONNECT only, port 443 only, agent identified by `Proxy-Authorization`
  (agent name and token). Hosts of services that inject a credential (or set `inspect`) are
  intercepted: TLS is terminated with a project-CA certificate whose name must match the CONNECT
  target, requests for any other host inside the tunnel get 421, the credential replaces whatever
  the agent sent, the agent is always spoken to in HTTP/1.1, responses stream unbuffered. Every
  other allowed host is a tunnel, but the proxy reads the TLS ClientHello first and refuses the
  tunnel when the server name is missing or differs from the approved host (domain fronting). Plain HTTP is refused (405); agents get `HTTP_PROXY`
  too so `http://` fails loudly. Audit lines (JSON, no bodies or query strings) go to the proxy's
  stdout (`egzo proxy log [--agent]`).
- **Hardening defaults.** Sidecars: read-only root filesystem, all capabilities dropped,
  `no-new-privileges`, tmpfs for `/tmp`. Agents: all capabilities dropped, `no-new-privileges`, an
  init process, writable root filesystem. Agents are always on an internal-only network, with egress only via the proxy. An agent's `runtime:` (as in Compose, e.g. `runsc` for gVisor) is passed to the engine as is; there is no project-wide default (use the engine's `default-runtime`, or a YAML anchor).
- **Agents run as the invoking user** (`uid:gid` of whoever runs egzo, on Docker; Podman maps users itself), so
  what they write on the host belongs to that user. Volumes are created by the engine owned by root, so a prep
  container hands each volume an agent writes to (workspaces, its home) to that user after the agent's container
  is created and before it starts: the engine hands a volume to root when it is the container's working
  directory, which would undo an earlier chown.
- **Harness images** carry the egzo binary (`COPY --from=<egzo image>`), `HOME=/home/agent` (a volume
  `<project>_<agent>-home`, so `/resume` and settings survive recreating the agent) and
  `ENTRYPOINT egzo agent run --`. An integration (`internal/harness`) is pure: from the agent's definition it
  produces files (merged into what the harness already keeps in the volume), a command line and the interrupt
  key. Claude Code starts with `--permission-mode bypassPermissions`, never with `permissions.defaultMode` in the settings (which makes it ask on its first start whether to make auto mode the default; nobody is there to answer in an unattended agent, and an injected Enter would). Claude Code: `~/.claude.json` (onboarding done, each workspace trusted, a placeholder API key
  pre-approved, the `egzo` MCP server), `~/.claude/settings.json` (its bypass prompt skipped, any earlier `defaultMode` removed, hooks
  `egzo hook <Name>`), `--model`, `--append-system-prompt` with the platform instructions + the agent's prompt,
  `IS_SANDBOX=1`. OpenCode: `opencode.json` (`permission: {"*": "allow"}`, the MCP server, model,
  instructions) and a plugin that reports `SessionStart` when it loads (OpenCode only creates a session on the
  first prompt), `UserPromptSubmit` from `chat.message` and `Stop` from `session.idle`. `permissions: default`
  undoes all of it explicitly, so an opt-out also fixes a home an earlier run configured.
- **Messages and delivery** (`internal/control`, `internal/session`; the model is in "Messages"). The control
  sidecar keeps the messages and their states in memory, rebuilt from the event log, and derives the activity
  (`starting | idle | working | blocked`) from hooks (`SessionStart`/`Stop` idle, `UserPromptSubmit` and the tool
  hooks working, `Notification` blocked unless it says the agent waits for input). The holder asks
  `POST /v1/claim` when the TUI is ready and nobody has typed (read-write clients only) for `inject.human_quiet`;
  control hands over an interrupt first, otherwise the announcement line for what is queued, but only to an idle
  agent with no announcement still awaiting its fetch. The line is composed in control from fixed words and ids and
  typed as one paste; fetching is the acknowledgement; no fetch by `inject.ack_timeout` announces again, three
  times in all, then `unconfirmed`. Harnesses without hooks (`inject.idle_signal: quiescence`, the default for
  `custom`) are idle when their output has been quiet for `inject.quiescence`. `egzo ps` shows ACTIVITY, OPEN
  and WAITING next to the reported status.
- **Git workspaces** (`internal/stack/git.go`, `internal/gitprep`): the CLI decides the layout (`clone`:
  `<path>/<agent>`, `shared`: `<path>/shared`, `worktree`: `<path>/<agent>` plus the base `<path>/.base`) and a
  prep container does the git, on the agent's own network so the clone goes through the proxy as that agent. In
  every container a checkout is `/workspace/<name>` and a worktree's base `/.egzo/base/<name>`, so the absolute
  links git writes between them are valid in prep and agent containers (from the host they do not resolve;
  worktrees are registered with `--force` for that reason, each on its own branch `egzo/<agent>`). An existing
  checkout is never touched; one made in another mode is refused. `down --workspaces` inspects every checkout
  in a prep container first and refuses when any holds uncommitted files or unpushed commits (`--force`).
- **Sidecar image** (`Dockerfile`) is the static binary on Alpine with CA roots and git >= 2.47.
- **Label kinds** in use: `control`, `proxy`, `agent`, `workspace` (declared volumes without a source), and
  `prep` (short-lived containers, which never outlive the command).
- **Messaging contract** (control sidecar). Agent API on `http://control:7777` (HTTP basic: agent name and
  its token, in `EGZO_CONTROL_URL`, `EGZO_AGENT` and `EGZO_TOKEN`): `GET|POST /v1/messages`,
  `GET /v1/messages/{id}`, `POST /v1/messages/{id}/resolve|update|ask`, `GET /v1/agents`, `POST /v1/status`,
  `/v1/hooks/{name}`, `/v1/activity`, `/v1/claim`, and MCP at `/mcp` with the tools `list_messages`, `get_message`,
  `resolve`, `update`, `ask`, `message`, `status` and `agents`. An agent can only act as itself and only sees
  messages addressed to it (an unknown id and someone else's answer alike: "no such message"), and the agent port
  serves no operator verb. Operator API (unix socket, exec only): `GET /events?after=&agent=&follow=1`,
  `POST /messages` (`interrupt: true` asks for the current turn to be stopped), `GET /messages?agent=&kind=&all=1`,
  `GET /messages/{id}`, `POST /messages/{id}/resolve`, `GET /agents`, `PUT|DELETE /agents/{name}`, `PUT|GET /templates`. Event types: `status`,
  `message` (with `data.to`, `kind`, `re`, `hops`), `announced`, `fetched`, `resolved` (with the resolution's text and
  `data.outcome`), `unconfirmed`, `interrupt`, `interrupted`, `activity`, `hook`.
- **Spec snapshot** is stored on the control volume at every `up` that changes it, keyed by hash,
  with the resolved config, the label contract version and the config hash of every container.

Open, in the order they matter:
1. **pi** is accepted as a harness name but has no image or integration. `custom` covers it for now.
2. The engine matrix beyond Docker (rootless and rootful Podman, gVisor) is not part of the current spec run;
   the specs were written for it and need a pass on those hosts. Rootful Podman loses outbound connectivity
   intermittently (`known-issues/rootful-podman-intermittent-egress.md`).
3. `request_secret_access`, the optional mount of the user's own `~/.claude` settings, per-service path scoping.
4. A real model call is never made by the specs; the end-to-end behaviour of a harness with a model (that a
   reply comes back, that the Stop text reaches `say`) is unverified.

## Future: egzo-hub + web UI (planning; shapes the foundation, not built yet)

### Model
- The hub + web UI is a **separate deliverable** (`egzo-hub`, own binary and image), distributed as a
  docker-compose stack and allowed its own database. It lives in the same git repository as core
  and shares code through `internal/` (one Go module); it is not part of the `egzo` CLI.
- The hub connects to **engines** (docker/podman: unix socket, tcp+TLS, ssh; local or remote) and
  manages egzo projects and agents running on them. Quick start: mount the local docker socket
  (root-equivalent; docs must say so and point to rootless engine / ssh context as safer).
- Multi-project (like a docker UI shows all compose projects) and multi-user. **Users are global
  to the hub instance** and live only in the hub database.
- **Observe and operate only.** The project YAML + CLI is the one and only way to create or change
  a project (`up`, `down`, recreate, edit). The hub never writes YAML and never applies config.
  - Observe: dashboard, status, event timeline, logs, proxy audit, read-only view of the spec.
  - Operate: spawn instances of declared templates, start/stop/restart/remove instances, interrupt,
    attach, send messages, answer questions agents ask of people.
  - Collaborate: presence, one writer + read-only observers per agent (read-only attach), shared
    timeline with sender on every message, questions routed to users.
- **Secrets are CLI-only.** The hub never reads or writes secret values; it may list which secrets
  exist and which agents use them. A hub compromise must not leak credentials.
- **No users in YAML files, ever.** User identities, roles and per-project grants live in the hub
  database. YAML may only define which *permissions/roles exist* and what each allows, never who
  holds them.
- Trust: whoever holds the engine access is trusted. The hub is the policy enforcement point for
  its users; it passes the acting user as an asserted `actor` field. No token signing/verification
  in projects. The CLI stays "OS user = operator".

### Messaging clients are hub features
Discord (later Slack, Telegram, Teams) is implemented in the hub, not in core projects. The hub
holds one bot per instance, maps chat identities to hub users (a field on the user record),
enforces the same permissions as the web UI, lays out channels across projects, and routes
questions agents ask (`ask`) to the right person. Core only provides the contract below; a messaging
client is just another client of it, tested in core with a fake client. Consequence: no Discord
without a hub (accepted). A hub-less bridge could later be a separate tool on the same contract.

### Core <-> hub contract (public API; version it)
1. Label schema (`ai.egzo.project|service|kind|config-hash|spec-version|project-dir|created-by` plus
   `ai.egzo.agent.*`).
2. Control sidecar operator API (status, event stream, message queue, spec snapshot read),
   versioned, reached via `engine exec` only (unix socket inside the container): no published
   ports, identical over socket/TLS/ssh. The agent-facing TCP API is separate and not for the hub.
3. Terminal attach = engine exec attach (hub bridges it to xterm.js).
4. Self-describing projects: flat labels + `inspect` give the agent/project card (observed state,
   harness, workspace) with no exec; the redacted resolved-spec snapshot on the control volume
   (read via exec) gives the detailed view while control is running.
5. Shared Go packages (list, inspect, read-spec, start/stop, attach, send) reused by the hub.
6. Messaging contract for any client (CLI, web, chat): `send` + queue API (to an agent); typed
   event stream (message sent, announced, fetched, resolved; status change; question asked and answered); questions (`ask`) with a
   stable message id so they can be answered later from anywhere; actor on every message/answer.

### Foundation requirements for core
- Stable, versioned labels and control API (above).
- Principal/address format `user:<id>`, `agent:<project>/<name>` in message envelopes, audit
  entries and permissions from day one; actor field everywhere (default `operator`).
- Permission vocabulary per project (e.g. observe, attach-rw, send, answer, lifecycle), defined in
  YAML as roles->permissions, with no user bindings.
- Schema distinguishes host-bound workspaces (bind) from portable ones (git/volume) so a project
  that is operated on a remote engine cannot silently depend on host paths.
- Core logic lives in importable Go packages, not inside CLI command code.

### Hub risks to track
Privileged socket access (rootless + later label-scoped proxy); engine credentials for remote
engines (ssh/TLS) stored by the hub; many exec streams (engine events + on-demand sidecar streams);
CLI `up` recreating containers while users are attached (hub must handle disconnects cleanly).


## Hardening decisions (code review of the first implementation)

- **Names.** Agents, workspaces: 1 to 63 of `[a-z0-9_-]`, starting with a letter or digit. Reserved: agents
  `control`, `proxy`, `prep`, `shared`; workspaces `control`, `ca`, `ca-private`, `egress` and `<agent>-home`, which
  would otherwise be egzo's own volumes (a workspace named `control` would hand an agent the key every token
  derives from). Instance names follow the same rule and may not be a template name, `control-1` or `proxy-1`, or end in `-home`. Settings that would do nothing (`proxy.audit`, `control.tools`,
  `agent.tools`) are not in the schema.
- **Git sources** carry no credentials in the URL (the proxy injects them) and a real branch name. Git run by egzo
  ignores hooks and file-system monitors of the repository it works in. `down --workspaces` stops the agents, inspects
  each checkout in a container of its own, counts uncommitted files, unpushed commits (also on a detached HEAD) and
  stashes (which block), lists ignored files (which only inform), removes only real checkouts, and starts the agents
  again if it is refused.
- **Proxy.** It connects only to public addresses (checked on the address a name resolved to), caps connections per agent
  and in all, closes idle connections, refuses Encrypted Client Hello, keeps bytes sent right behind CONNECT, truncates
  logged values, logs bytes and duration of a tunnel, bounds its certificate cache, and answers 503 "run `egzo up`" when
  it has no policy; `egzo start|restart proxy` reloads the policy. The CA is only ever replaced by an explicit rotation.
- **Control.** Verbs that write to the log have budgets per agent; hook payloads are reduced to a few named fields (plus
  the line egzo typed); the log is compacted past 64 MB; the project key is created once, atomically, and never replaced;
  snapshots are verified against their hash and retired past 50; every recipient has at most 20 open requests; the
  holder releases an agent that sits at "working" while its screen is silent (`if: working`), and announces itself with
  `if_unset`, so it never undoes the harness's own first report.
- **Containers.** Sidecars run as 65532 with memory and process limits; every container has bounded engine logs; agents
  have a process limit and, with a memory limit, no extra swap; egzo run as root still gives agents uid 1000; rootless
  Docker is treated like Podman. Agents start only after the sidecars are healthy and attached. One command changes a
  project at a time (`.egzo/lock`).
- **Terminal.** Text an agent wrote is made harmless (`internal/termsafe`) before `ps`, `messages` and `send --wait`
  print it; `attach` turns off the modes a TUI left on when the user detaches.
