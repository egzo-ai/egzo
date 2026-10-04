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
   tiny explicit `egzo.yaml` (scaffolded by `egzo init`), then `egzo up` and `egzo attach`. There is
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
5. **Egzo features are additive.** Multi-agent, messaging, status, handoff, policies appear as
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
     via the hooks/`ask_user` path, never permission gates.
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
- `egzo-agent` is the in-container role (hooks, MCP stdio fallback, pty holder candidate), built from
  the same code; how it gets into harness images stays an open spike.
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

Names: `<project>-<service>-<n>`; networks `<project>_<agent>` (one per agent) ; volumes
`<project>_<vol>`.
`egzo up` = list by label -> diff config-hash -> create/recreate/remove. Idempotent. `up` always
returns once converged: there is no foreground mode and no `-d` (agents are TUIs you `attach` to,
nothing streams to the `up` terminal). Preview with `--dry-run` (long form only, no short flag).
Caveat: labels are immutable, so *runtime status* (idle/busy/blocked) is NOT stored in labels.
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
  audit: true                  # log connections, and requests (no bodies) for intercepted hosts

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

agents:
  coder:
    harness: claude-code       # claude-code | opencode | pi | custom
    image: ghcr.io/egzo-ai/egzo-harness-claude-code
    workspaces: [repo, scratch] # at /workspace/<name>; 2+ => workdir is /workspace
    egress: default            # profile name; omitted => default
    # workdir: repo            # optional override (name under /workspace, or a path)
    model: claude-sonnet-5-5
    prompt: ./prompts/coder.md
    tools: [control]           # MCP servers exposed to the agent
    resources: { cpus: 2, memory: 4g }
    runtime: runsc             # OCI runtime, as in Compose (e.g. gVisor); engine default if omitted
    permissions: bypass        # bypass (default) | default
    env: { FOO: bar }          # non-secret only; validated, secret-looking values rejected

  reviewer:
    harness: opencode
    workspaces: [./docs:ro]    # host dir, read-only (a git workspace cannot be :ro)
    depends_on: [coder]

control:                       # orchestrator MCP + status sidecar (always present)
  tools: [status, ask_user, handoff, request_secret_access]

# No bridges and no users here. Chat integrations (Discord, ...) are hub features, and who may
# talk to agents is never declared in project YAML (see Future: egzo-hub).
```

## CLI UX

    egzo init [--harness claude-code]      # scaffold egzo.yaml
    egzo config                            # resolved/validated YAML (like compose config)
    egzo up [svc...]                       # reconcile; --recreate; --dry-run. No -d: always returns
    egzo diff                              # file vs running (same algorithm as up --dry-run)
    egzo down [--volumes]
    egzo start|stop|restart <svc>          # operate on existing containers (no reconcile)
    egzo ps                                # engine state + status from control sidecar
    egzo attach <agent>                    # native TUI; detach key configurable (default Ctrl-])
    egzo logs [-f] <svc>
    egzo send <agent> "message"            # same queue path any messaging client (hub) uses
    egzo exec <svc> -- cmd
    egzo secrets ls|set|rm                 # vault management, never prints values
    egzo proxy log|rules                   # audit trail
    egzo ca rotate                         # new project CA; restarts agents
    egzo doctor                            # engine, rootless, gVisor, network checks

## Decisions
- Language: Go.
- Engine access: Docker Engine API (Go docker client). The engine is whatever `DOCKER_HOST` points at
  (default `/var/run/docker.sock`), like the docker CLI: no probing, no engine setting in egzo.yaml.
  Podman works through its Docker-compat socket (`podman.socket`); its API ignores a container's
  `runtime:`, so egzo warns that it cannot apply or verify it. Never mount the socket into agent
  containers. Engine matrix in specs.
- No daemon. Everything runs in containers except the `egzo` CLI.
- Harness control is TUI-first (Scion-style). The native TUI always runs in the agent container
  behind a pty layer (the `Session` backend, see Session backend below); there is never a mode
  switch. ACP was the v1 primary channel and is dropped: it cannot run concurrently with the TUI on
  one session. It may return later as an optional driver for headless agents only.
  - Human -> agent: injected bracketed paste, queued per "Injection and agent states".
  - Agent state -> orchestrator: harness hooks call the control sidecar's agent API.
  - Agent -> human: MCP `egzo` tools (`say`, `status`, `ask_user`, `handoff`) plus the Stop hook's
    final assistant text.
  - Attach (CLI and web UI) = `engine exec -it` into the Session backend.
- Specs: pytest in `specs/`; `@pytest.mark.todo` = xfail(strict=True), so one run shows done
  (pass) / todo (xfail) / broken (fail); an unexpectedly passing todo fails the run.
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
- `:ro` on a git-defined workspace is a validation error with a helpful message (git needs write
  access) that points to the cross-agent reference below.
- Cross-agent reference `<agent>/<workspace>:ro` (e.g. a reviewer lists `coder/repo:ro`): mounts
  the other agent's own instance of that workspace (its clone or worktree volume) read-only, at
  `/workspace/<agent>/<workspace>`. This is the supported way to give an agent read access to
  another agent's git workspace. Validation: the agent and workspace must exist, that agent must
  list the workspace, no self-reference, and `:ro` is mandatory (read-write access to another
  agent's clone would defeat per-agent isolation). The engine enforces the read-only mount. Workspaces outlive agents; `down --volumes` removes volumes, never host binds.
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

## Control plane (decided)
- The control sidecar holds status, event log, message queue and spec snapshots (volume).
- Agent -> control: HTTP on the container network. MCP over streamable HTTP; hooks and a stdio-MCP
  fallback go through the small static `egzo-agent` binary (also the candidate pty holder).
- One network per agent; control and proxy join each agent's network, agents never share one, so
  agent-to-agent traffic only exists through control and policy. `NO_PROXY` covers the control host.
- Per-agent token (generated by `up`, injected as env) identifies the agent to control. It is an
  agent identity, not a provider secret; compromising an agent only lets it act as itself.
  The same token is the agent's proxy credential (`HTTPS_PROXY=http://agent:<token>@proxy:3128`).
- Control has two listeners: an **agent API** (TCP, token auth, agent verbs only: say, status,
  ask_user, handoff, hook ingest) and an **operator API** on a unix socket inside the container,
  reached only via `engine exec` by the CLI/hub (read spec, drain queue, interrupt, list questions).
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
- **Built-in services** ship with egzo as data (initially `anthropic`: `api.anthropic.com` +
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

## Session backend (decided: explore two, ship exactly one)
tmux vs an `egzo-agent` pty holder, behind a `Session` interface, both run against one
backend-independent fidelity spec (pytest/pexpect); the product ships ONE, chosen by the matrix,
never two. P0 rows: keys (Shift+Enter, Ctrl-C/D), large bracketed paste, resize, truecolor, native
terminal scrollback/selection, injection. Other rows: OSC52, OSC8, wide chars, detach/reattach
redraw, two clients of different sizes, read-only observer, title/bell.
Leaning pty holder: tmux takes the outer alternate screen so native scrollback is lost (mitigations
like `alternate-screen off` duplicate lines); a raw pass-through has exact keys/clipboard, injects by
writing the pty master, supports read-only observers natively, and reuses `egzo-agent`. Its weak
point is state restore on reattach (replay last N KB + SIGWINCH repaint); fallback is holder + a
small terminal emulator, only if the raw replay proves inadequate. `Session` interface:
Attach, Resize, Inject(text), Clients() (rw/ro), LastHumanInputAt(), SubscribeOutputActivity().

## Injection and agent states (decided)
- States: `starting`, `idle`, `busy`, `blocked` (waiting on a human), `stopped`. Signals: harness
  hooks (Claude: SessionStart, UserPromptSubmit->busy, Stop->idle, Notification->blocked), the pty
  holder's view of the stream, and engine container state. Verify hook matchers per release.
- Never inject while a human types: inject only when `idle` and no read-write client has typed for
  `inject.human_quiet` (default 30s). Read-only observers never block. Known residual risk: a half-
  typed draft merges with the paste (no screen model to see the input box); accepted.
- Mechanics: one atomic write of `ESC[200~ text ESC[201~`, short delay (~100-300 ms), then Enter.
  Every message carries a header like `[egzo msg 7f3a from user:cedric]` (platform instructions
  explain it; v1: Claude flagged an unexplained header as injection). The UserPromptSubmit hook
  payload contains the prompt, so matching the id is an exact ack. No ack within timeout ->
  `unconfirmed`, surfaced to the operator; never blind-retry (could duplicate).
- Queue: FIFO per agent in control. Message states: queued, delivering, delivered (acked),
  unconfirmed/failed. Queued messages are combined into one prompt when idle, each still acked
  individually. `--interrupt` sends the harness's interrupt key (declared per harness).
- Each harness integration declares `idle_signal: hook | quiescence` (no pty output for N s; weaker).
  OpenCode likely has `session.idle` plugin events (verify); pi unknown.
- Lost signals: if we interrupted, control marks idle itself; a human Esc may not fire Stop (verify),
  so tool-end hooks + output quiescence yield `stalled`, which returns to idle when output stays quiet.

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
- Exact mount layout for worktree mode (relative link validity without exposing other worktrees),
  and the prep container's uid mapping under rootless podman.

## Still open (spikes)
1. Session backend fidelity matrix results (tmux vs pty holder); Claude Code repaint on SIGWINCH.
2. Per-agent `internal` networks and CA bundle bind-mount on rootless podman + gVisor.
3. Claude Code hook behavior: Stop on user interrupt, Notification matchers.
4. Per-harness hook/idle signal for OpenCode and pi.
5. Whether `egzo-agent` is baked into harness images or mounted read-only.
6. Practical label size limits on docker and podman, incl. remote engines.

## specs/ (pytest)

The suite is the status report: `specs/README.md` explains the done / todo / broken / promote model.
It runs against Docker and rootless Podman (`EGZO_SPEC_ENGINES`), builds its own sidecar and agent
images, and needs internet access for the specs that talk to real hosts (they skip when offline).

    specs/
      spec_status.py       # plugin: todo = strict xfail, per-area table, --spec-json
      promote.py           # removes @todo from specs that now pass
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

Planned (need a harness integration, the session backend or the control agent API first):

    test_fidelity.py     # backend-independent Session matrix (P0 rows block release)
    test_injection.py    # idle/busy/blocked, human-quiet rule, header ack, no blind retry, queue combine
    test_control_mcp.py  # the MCP adapter over the control verbs
    test_attach.py       # pty attach/detach via pexpect
    test_prep.py         # git clone/worktree/shared, idempotency, down --workspaces safety

Security specs are the differentiator: assert secrets never appear in `engine inspect`,
agent env, agent filesystem, or logs; assert direct egress fails.

## Implementation notes (what exists, and where it differs from the drafts above)

Built and covered by specs on Docker and rootless Podman: `init`, `config`, `up` (`--dry-run`,
`--recreate`), `down` (`--volumes`), `ps`, `logs`, `exec` (with a terminal: raw mode, window
resizes), `start|stop|restart`, `proxy log`, `send`, `events`, `questions`, `answer`, plus the
in-container roles `egzo control` and
`egzo proxy serve`. Agents run any image through the `custom` harness (it requires `image:`).

Decisions taken while building (reversible; each is covered by specs):
- **Two-phase `up`.** The control sidecar comes up first because it hands out per-agent tokens
  (HMAC of the agent name under a random project key kept on the control volume, so tokens are stable
  across `up` runs and change only when the control volume is deleted). Secrets are read before
  anything is created, so a missing secret never leaves half a project behind.
- **Secrets reach the proxy only through `engine exec` stdin** and live in its memory. The policy
  hash covers the secret values, so rotating a secret re-pushes the policy without recreating any
  container; a restarted proxy denies everything until the next `egzo up` reloads its policy. The
  proxy container's `inspect` never holds a secret.
- **Operator APIs** are unix sockets inside the sidecar, called through `egzo <role> request METHOD
  PATH` run by exec (scratch images have no curl). Control: tokens and spec snapshots. Proxy: policy.
- **CA distribution is a read-only directory mount** (`/etc/egzo/ca` holding `ca.crt` and
  `ca-bundle.crt`) plus `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`, `GIT_SSL_CAINFO`
  and `NODE_EXTRA_CA_CERTS`, not an overlay of `/etc/ssl/certs/ca-certificates.crt`: a directory
  mount works the same on Docker and Podman. The CA key stays in a volume only the proxy mounts.
- **Proxy behaviour.** CONNECT only, port 443 only, agent identified by `Proxy-Authorization`
  (agent name and token). Hosts of services that inject a credential (or set `inspect`) are
  intercepted: TLS is terminated with a project-CA certificate whose name must match the CONNECT
  target, requests for any other host inside the tunnel get 421, the credential replaces whatever
  the agent sent, the agent is always spoken to in HTTP/1.1, responses stream unbuffered. Every
  other allowed host is a tunnel, but the proxy reads the TLS ClientHello first and refuses the
  tunnel when the server name is missing or differs from the approved host (domain fronting). Plain HTTP is refused (405); agents get `HTTP_PROXY`
  too so `http://` fails loudly. Audit lines (JSON, no bodies or query strings) go to the proxy's
  stdout.
- **Hardening defaults.** Sidecars: read-only root filesystem, all capabilities dropped,
  `no-new-privileges`, tmpfs for `/tmp`. Agents: all capabilities dropped, `no-new-privileges`, an
  init process, writable root filesystem. Agents are always on an internal-only network, with egress only via the proxy. An agent's `runtime:` (as in Compose, e.g. `runsc` for gVisor) is passed to the engine as is; there is no project-wide default (use the engine's `default-runtime`, or a YAML anchor).
- **Sidecar image** must carry CA roots (a scratch image needs `ca-certificates.crt`) and, once the
  prep role exists, git >= 2.47.
- **Label kinds** in use: `control`, `proxy`, `agent`, and `workspace` (volumes of declared
  workspaces without a source).
- **Messaging contract** (control sidecar, decided in the hub section, now built). Agent API on
  `http://control:7777` (HTTP basic: agent name and its token, in `EGZO_CONTROL_URL`, `EGZO_AGENT` and
  `EGZO_TOKEN`): `POST /v1/status`, `/v1/say`, `/v1/ask` (returns a stable question id),
  `GET /v1/questions/{id}`, `GET /v1/inbox` (hands over queued messages and marks them delivered),
  `POST /v1/hooks/{name}` (a harness hook payload becomes an event). An agent can only act as
  itself, only sees its own questions, and the agent port serves no operator verb. Operator API (unix
  socket, exec only): `GET /events?after=&agent=&follow=1` (typed JSON-line stream), `POST /queue`,
  `GET /queue`, `GET /questions`, `POST /questions/{id}/answer`, `GET /agents`. Every event has a
  sequence number, a time, a type (`status`, `say`, `question`, `answer`, `message`, `delivered`,
  `hook`) and an actor (`agent:<name>`, `user:<id>` or `operator`); the log is an append-only file on
  the control volume and survives a restart. The CLI always acts as `operator`; the hub will pass the
  signed-in user. Until the session backend exists, an agent receives messages by polling its inbox.
  The MCP tools (`say`, `status`, `ask_user`, ...) will be a thin adapter over these verbs.
- **Spec snapshot** is stored on the control volume at every `up` that changes it, keyed by hash,
  with the resolved config, the label contract version and the config hash of every container.

Known gaps, in the order they block real use:
1. **Which user agents run as.** On rootful Docker, root without capabilities cannot write a host
   directory owned by the invoking user (rootless Podman works because container root is the host
   user). Git clones and host-path workspaces need this answered.
2. **Git workspaces.** Refused with a clear error until the prep role exists: a short-lived container
   running as the agent (so the agent's egress profile and injected token apply) that clones into the
   configured host path.
3. **Harness images and integrations** (Claude Code, OpenCode, pi): bypass-mode config, first-run
   seeding, hooks. They need real credentials to validate, so none is written yet.
4. **Attach and the session backend.** The terminal plumbing exists (`exec`); the choice between a
   tmux and a pty-holder backend is still the fidelity spike, which needs a real harness.
5. **Delivery into a TUI and the MCP adapter.** Events, status, questions and the queue exist; what is
   missing is injecting a queued message into the agent's terminal (needs the session backend, the
   idle states and the human-quiet rule) and exposing the verbs as MCP tools.
6. Not yet implemented: `egzo ca rotate`, `down --workspaces`, `up SERVICE`, per-agent
   `--agent` filtering of `proxy log`.

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
  - Operate: start/stop/restart existing containers, interrupt, attach, send messages, answer
    `ask_user`.
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
`ask_user` questions to the right person. Core only provides the contract below; a messaging
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
   event stream (Stop text, status change, question asked, question answered); `ask_user` with a
   stable question id so it can be answered later from anywhere; actor on every message/answer.

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
