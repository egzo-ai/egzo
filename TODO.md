# Review items for the Go implementation

Full code review of `cmd/` and `internal/` (about 15.9k lines), on `main` at 832cd57, on 2026-10-05. It was done
by reading all the code, then checking suspicious points: `go vet`, `go test -race -cover`, `staticcheck`,
`golangci-lint`, `govulncheck`, and a few throwaway tests (since deleted).

How to read it:
- **[verified]** means I reproduced it (a test, `egzo config`, or the race detector). The others come from reading
  the code and are plausible; confirm them with a test before fixing.
- IDs are stable (`R-nn`) so commits can refer to them. Tick a box when the fix lands.
- Per CLAUDE.md, every behaviour change here starts with a **red** spec or unit test. Where a spec belongs, the
  item says so.
- Severity: **P0** is broken or a boundary is crossed today. **P1** is a real hole or a likely failure. **P2** is a
  hardening, scale or quality item. **P3** is polish.

## Status after the fix pass

Every item below was met with specs (`specs/`), unit tests, or both, and then fixed; `[x]` means the fix
landed and its tests pass (`go test -race ./...` and the full spec run on the `docker-gvisor` platform).
Items left `[ ]` are **partly done** or deliberately open:

- **R-11** the audit log is bounded per field and records bytes and duration, and the sidecars' engine logs are
  capped; aggregating or sampling repeated denials is not done.
- **R-42** done: a doctor check that the engine makes internal networks, the project lock, the OpenCode/bearer
  warning, `init --harness custom`, uid handling. Not done: nothing else of that list is open, but the Podman uid
  mapping is still an open design question (rootless Docker and Podman keep the image's user).
- **R-43** done except: `agentclient` still truncates a reply over 1 MB silently (replies are small by design).
- **R-47** done except: `Observe` still runs once per command and the sidecars' `knownAgent` still reads
  `project.json` per call.
- **R-48** follows from the items it lists, which are done, except audit sampling (R-11).
- **T-06** the stack's engine-bound code (`Up`, `Apply`, `Down`, `RunPrep`) is covered by the specs, and the
  logic around it (plans, dependencies, policy, git inspection, pull stream, registry auth, locks) by unit
  tests; a fake of `client.APIClient` for the rest was not built.
- **T-07** unit tests exist for sanitising, secrets, removable checkouts, hook retry, project identity; the
  `down --workspaces` flow itself and `send --wait` exit codes are specs only.
- **T-12** the Moby client migration is not done (`govulncheck` still flags GO-2026-4887, server side, no fix).
- `errcheck` findings of `golangci-lint` (unchecked `Close` and `Write` returns) were not worked through;
  `staticcheck` is clean and `make lint` runs it with `govulncheck`.

Behaviour that changed on purpose, with the spec that pins it: reserved and malformed names are rejected
(`test_schema.py`); a proxy answers 503 when it has no policy and `egzo restart proxy` reloads it
(`test_proxy.py`); the event log keeps only the name and a few fields of a hook, plus the line egzo itself typed
(`test_messaging.py`); every recipient, people included, holds at most 20 open requests; `down --workspaces`
stops the agents first, inspects each checkout in its own container, counts stashes and detached commits, and
only removes real checkouts (`test_git_workspaces.py`); sidecars run as uid 65532 with memory and process limits.

## P0: broken now, or a trust boundary crossed

- [x] **R-01 [verified] `make test` is red: data race in the session holder.**
  `internal/session/holder.go:95` (`wait()` closes `h.master`) races with `holder.go:222` `repaint()` and `resize()`,
  which call `pty.Setsize`. That calls `os.File.Fd()` on a file that is being closed, so the ioctl can hit a recycled
  fd. `go test -race ./internal/session` fails in `TestAClientArrivingLateGetsWhatTheProgramWroteBefore`.
  `repaint` also reads `h.rows` and `h.cols` without the lock (`holder.go:223`). `h.code` is only safe because of the
  `done` channel.
  Fix: serialise master use with a mutex or a `closed` flag; wait for `pump` to finish instead of
  `time.Sleep(100ms)` (`holder.go:94`); use `SyscallConn().Control` for the ioctls.
  Also run `-race` in CI.

- [x] **R-02 [verified] a `depends_on` cycle hangs `egzo up` forever.**
  `config/resolve.go:154` only checks that the name exists. A cycle (`a -> b -> a`) or self-dependency gives
  cyclic `dependencies()` (`stack/schedule.go:12`), and `runConcurrently` (`schedule.go:76`) waits on `done`
  channels that never close. The context is never cancelled, so there is no timeout.
  Fix: detect cycles and self-dependency in config validation with a clear message ("depends_on cycle: a -> b -> a").
  Defence in depth: have `runConcurrently` reject cyclic deps.
  Tests: a config test, a `dependencies`/`runConcurrently` test with a timeout, and a spec in `test_schema.py`.

- [x] **R-03 [verified] reserved and colliding names are not validated, so a workspace can hand an agent the control volume.**
  Reproduced with `Desire`: a workspace `control` or `ca` makes `p_control` / `p_ca` appear twice in `desired.Volumes`,
  and the agent mounts `p_control` read-write at `/workspace/control`. That volume holds `key`, the HMAC project key.
  Anyone with it can forge every other agent's token, for both the proxy and control. A workspace `ca` gives the agent
  read-write access to the CA bundle the proxy publishes to every agent, so it can plant a CA.
  Other collisions (all accepted by `egzo config` today):
  - agents named `control`, `proxy` or `prep` collide with container names (`<p>-control-1`), networks (`<p>_control`),
    the DNS aliases `control` / `proxy`, and `service()` lookup (`cli/daily.go`);
  - workspaces named `control`, `ca`, `ca-private`, `egress`, or `<agent>-home` collide with volumes and networks
    (`stack/desired.go:155`);
  - an agent named `shared` or `.base` collides with the git layout (`stack/git.go:68` `existingMode`);
  - a workspace named `..` gives paths like `.egzo/workspaces/..` (`config/workspaces.go:30`);
  - agent names such as `my agent` pass config, then fail at the control sidecar (`safeName`, `control/api.go`) or
    break the proxy URL `http://agent:token@proxy` (`stack/git.go:137`) and the branch `egzo/<agent>`.
  Fix: one name rule for agents, workspaces, vaults and secrets, shared with `control.safeName`
  (`^[a-z0-9][a-z0-9_-]*$`); a reserved-name list; a check that no two resources map to the same engine name.
  Tests: config table tests, plus a spec.

- [x] **R-04 [verified] credentials inside a git URL are accepted, printed and written.**
  `egzo config` prints `https://user:ghp_…@github.com/…` verbatim. The `checkEnv` heuristics are not applied to
  `git.url`. The URL then ends up in the snapshot on the control volume and in the prep container's command line,
  which `inspect` shows. `egzo init` copies `remote.origin.url` including userinfo into the scaffold
  (`cli/init.go:106` `originURL`).
  Fix: reject userinfo in `git.url` (point to the `github` service); strip it in `originURL`. `branch` must pass
  `git check-ref-format --branch` and not start with `-` (see R-17).
  Spec: no secret in `egzo config` output.

- [x] **R-05 git hooks run with another agent's identity (worktree mode): cross-agent privilege escalation.**
  In worktree mode every agent mounts the shared base `.git` read-write. `gitprep.Worktree` (`gitprep/gitprep.go:51`)
  runs `git worktree add` in a prep container on the **new agent's network, with that agent's proxy token in the
  environment** (`stack/git.go:137` `proxyEnv`). An agent that planted `.git/hooks/post-checkout` in the base gets the
  other agent's token. Because every agent's network includes the proxy, it can then authenticate to the proxy
  as that agent and use its egress profile and injected credentials.
  `Git()` (`gitprep.go:17`) sets `GIT_CONFIG_GLOBAL=/dev/null` and `GIT_CONFIG_NOSYSTEM=1` but trusts repository-local
  config. The design accepts the shared-`.git` risk, but not this credential theft.
  Fix: in `Git()` always pass `-c core.hooksPath=/dev/null -c core.fsmonitor=false -c protocol.allow=never
  -c protocol.https.allow=always`. Also give prep only the proxy credential it needs, or use a short-lived per-prep
  token. Spec: a hook planted in the base does not run during `up`.

- [x] **R-06 terminal escape injection from agent-controlled text into the operator's terminal.**
  The agent chooses the text of `status`, messages, resolutions and `ask` choices. The CLI prints it raw:
  - `WriteStatus` prints `report.Status` (`stack/apply.go:308`). A newline in it forges extra `ps` rows. An ESC
    sequence can move the cursor, set the title, or write the clipboard with OSC 52.
  - `send --wait` prints the resolution and update text (`cli/messaging.go:107` and the line above it).
  - `writeMessages` (`cli/messaging.go:189`) collapses whitespace but keeps ESC and other C0/C1 bytes. It also cuts
    at byte 57, which can split a UTF-8 character.
  - `egzo logs` prints raw container output (the 4 KB tail from `runAgent`).
  Fix: one `sanitizeForTerminal` helper (drop C0 except tab, C1, DEL; one line for table cells; truncate on rune
  boundaries) used for every agent-controlled string when stdout is a TTY. `egzo events` is JSON and already safe.
  Test: unit test for the helper plus a spec with `status` containing `\x1b]52;c;…\x07` and `\n`.

## P1: real holes and likely failures

### Proxy (`internal/proxy`)
- [x] **R-07 no destination filtering: the proxy dials whatever the name resolves to.**
  `tunnel` (`server.go:143`, dial at `:163`) and the intercept transport use a plain `net.Dialer`. With `allow: ["*"]`,
  or an attacker-controlled name under an allowed wildcard, an agent can reach loopback, RFC 1918, link-local
  (cloud metadata 169.254.169.254) and the proxy's own agent networks, including another agent's port 443. The SNI
  check does not help: the agent writes the ClientHello itself, and a name that resolves to a private address is
  enough. This breaks "agents cannot reach each other directly".
  Fix: resolve first, refuse non-global addresses with a `Dialer.Control` hook (also catches rebinding), log
  `deny: private address`. An explicit opt-in knob can come later.
  Tests: unit test with `Dial` returning a private IP; a spec through a name resolving to a peer agent.

- [x] **R-08 no connection caps or idle timeouts: one agent can starve the shared proxy.**
  `ServeHTTP` hijacks and then nothing bounds the tunnel: no idle timeout, no max duration, no per-agent or global
  limit (`pump` in `tunnel`). The intercepted `http.Server` (`server.go:225`) has `ReadHeaderTimeout` only, so no idle
  timeout. A compromised agent opens connections until the proxy runs out of fds and every other agent loses
  egress.
  Fix: per-agent and global semaphores, `SetDeadline` refresh on activity (idle timeout, say 5 min), a max lifetime,
  and `IdleTimeout` on the inner server.

- [x] **R-09 bytes the client sent right after CONNECT are dropped.** `server.go:111`:
  `conn, _, err := hijacker.Hijack()` discards the `bufio.ReadWriter`. A client that sends its ClientHello without
  waiting for the 200 loses it if `http.Server` already buffered it, and the handshake times out.
  Fix: if `rw.Reader.Buffered() > 0`, wrap `conn` so those bytes are read first. Test: write CONNECT and a ClientHello
  in one `Write`.

- [x] **R-10 ECH can hide the real server name.**
  `readClientHello` / `parseServerName` (`sni.go`) compare the outer SNI. With Encrypted Client Hello the outer name
  can be an allowed public name while the inner name is something else. Only matters behind shared frontends, but the
  domain-fronting guarantee is the documented point of the check.
  Fix: refuse a ClientHello that carries extension `0xfe0d` (and the legacy `0xff03`). Spec: ECH ClientHello is denied.

- [ ] **R-11 the audit log can be flooded, and each failure costs an agent-controlled string.**
  `Event.Agent` and `Reason` echo the unauthenticated `Proxy-Authorization` and the requested host (`server.go:88`
  and the deny in `interceptHandler`), with no length cap and no rate limit, to a stdout that Docker logs without rotation by default. Same for
  tunnel events, which carry no bytes or duration, so the audit cannot show how much was sent out.
  Fix: truncate fields (256 bytes); aggregate or sample repeated denials; log `bytes_in`, `bytes_out` and `duration`
  at tunnel close. Set `max-size` / `max-file` log options on sidecars.

- [x] **R-12 a proxy without a policy answers 407 "missing or invalid proxy credentials".**
  After any proxy restart (host reboot, OOM, `egzo restart proxy`) the policy is gone (by design) and every agent gets
  that message (`server.go:88`), which blames the agent. Nothing tells the operator to run `egzo up`.
  Fix: distinct status and audit reason ("no policy loaded: run `egzo up`"); `egzo start|restart proxy` and
  `egzo ps` should warn (`ps` can query `GET /policy`). Longer term: let `restart`/`start` of the proxy re-push the
  policy.

- [x] **R-13 the minted-certificate cache is unbounded.** `leaf.go:75` keeps one entry per host forever and
  never evicts expired ones. Hosts are agent-chosen when the policy has a wildcard with `inspect`. Fix: LRU with a cap, drop
  expired entries. (Minting also happens under a global mutex: fine, but note it.)

- [x] **R-14 CA handling: key/cert mismatch is never detected and files are written non-atomically.**
  - `parseCA` (`ca.go:94`) does not check that the key matches the certificate.
  - `generateCA` writes key then cert with `os.WriteFile`. A crash in between leaves a mismatched pair.
  - `LoadOrCreateCA` (`ca.go:34`) regenerates the CA silently when the cert is unreadable for any reason other than
    "missing", which changes trust for every agent without anyone asking.
  - `POST /ca/rotate` (`run.go:87`) writes the new CA to disk **before** it is swapped in memory; if `Publish` fails the
    proxy keeps signing with the old CA while the disk has the new one, and the next restart flips trust silently.
  Fix: verify pair on load, write temp + rename (key first, then cert), only regenerate on both-missing, rotate as
  "stage, publish, swap, commit". Tests: truncated files, mismatched pair, rotate failing at Publish.
  (NameConstraints stays a known TODO.)

### Control sidecar (`internal/control`)
- [x] **R-15 a compromised agent can fill the control volume and memory, and kill control for everyone.**
  `status` (16 KB), `hook` (up to 64 KB payload) and `update` append events with no per-agent cap or rate limit.
  Only `send` is rate limited (`messages.go:230`); `update` goes straight to `write` (`messages.go:383`). The whole log
  stays in memory, `events.jsonl` has no rotation, and startup replays all of it.
  Fix: per-agent token bucket for every event-producing verb; cap total log size with compaction or rotation (snapshot the
  message index); drop `hook` events beyond a rate.

- [x] **R-16 hook payloads, including tool output and prompts, are stored verbatim and forever.**
  `agentapi.go:214` stores the full JSON payload. For `PostToolUse` that is the tool's result (file contents, command
  output, anything the agent printed), and for OpenCode `UserPromptSubmit` it is the prompt (`harness/opencode.go`).
  They land in `events.jsonl` on the control volume and in `egzo events`. Secrets read by an agent persist in a
  place the design says never holds secrets.
  Fix: store only a whitelist (`tool_name`, hook name, `Notification.message`, exit status); drop the rest. Spec: a secret
  printed by a tool is not in `egzo events`.

- [x] **R-18 `MCP` endpoint has no body cap and builds a server per request.**
  `agentapi.go:72` `authenticatedHandler` does not apply `MaxBytesReader` (the plain verbs do via `authenticated`).
  `mcp.go:15` creates a new `mcp.Server` with eight reflected tool schemas for every request. Fix: wrap with
  `MaxBytesReader`; build the tool set once and pass the agent through the request context.

- [x] **R-19 the agent HTTP server has no `ReadTimeout`, `WriteTimeout` or `IdleTimeout`** (`control.go:42`), only
  `ReadHeaderTimeout`. Slow-body clients hold goroutines forever. `identify` (`agentapi.go:45`) also calls `projectKey()`,
  which takes the global `s.mu` and **reads the key file from disk on every request**. Cache the key in memory at start.

- [x] **R-20 project key handling can rotate every token silently.**
  `projectKey` (`api.go:67`) regenerates the key if the file is not exactly 32 bytes and writes it with plain
  `os.WriteFile` (non-atomic). A crash mid-write (or a truncated file) changes all tokens, and agents keep the old
  ones in their environment. Fix: create with `O_EXCL`, write temp + rename + fsync, refuse and log when the file
  exists but is malformed.

- [x] **R-21 `PUT /specs/{hash}` truncates silently and writes non-atomically.**
  `api.go:118`: `io.LimitReader(…, 4<<20)` cuts a larger body and answers 204. The write is plain `os.WriteFile`, so a crash
  leaves a truncated snapshot that `GET` returns and the CLI treats as "already stored" (`stack/snapshot.go`). The
  hash is never checked against the body, and `specs/` is never pruned. Fix: `MaxBytesReader` and 413; temp + rename; verify
  `sha256(body) == hash`; retention.

- [x] **R-22 messaging limits can be bypassed or raced.**
  - `fetch` (`messages.go:304`) does not take `deliveryMu`, so a `fetched` event can be followed by an `announced` one;
    `index` (`messages.go:82`) then moves the state back from `fetched` to `announced` and the agent must fetch again
    before `resolve` works.
  - Open-count, depth and rate checks in `send` (`messages.go:230`) are not atomic with `write`; concurrent senders can exceed
    the 20-open cap.
  - The 20-open cap applies to agent recipients only: messages to `operator` and `user:*` are unbounded, so one agent
    can flood `egzo messages` at 30/min indefinitely.
  - `ask` accepts any number and length of `choices` (only the 64 KB body cap applies).
  Fix: take `deliveryMu` in `fetch`; make `index` ignore `announced` for a `fetched` message; one `mu` around check +
  write; per-recipient caps for humans; validate `choices` (count, length, printable).

- [x] **R-23 `Seq` is derived from the number of events, not from the last sequence number.**
  `events.go:63` `event.Seq = len(s.events) + 1`. `openStore` (`events.go:38`) silently skips unparseable lines, so after one
  corrupt line (a partial write at a crash) new events reuse sequence numbers, and `GET /events?after=` skips or repeats
  events. A line over 1 MB makes `scanner.Err()` fail and control refuses to start (`events.go:49`) with no way out.
  Fix: `Seq = last.Seq + 1`; tolerate long lines (use `bufio.Reader`), quarantine bad lines, and log how many were skipped.

### Git workspaces and `down --workspaces`
- [x] **R-17 option injection from config into git.** `gitprep.Worktree` passes `start := branch` (`gitprep.go:60`) as a
  positional argument with no `--`, so `branch: --detach` becomes an option; `Clone` is safe because `--branch`
  takes the value. Fix together with R-04: validate branch names (`git check-ref-format --branch`, no leading `-`).

- [x] **R-24 `down --workspaces` can lose work: the check happens too early and misses things.**
  `chooseWorkspacesToRemove` (`cli/engine_commands.go:148`) inspects the checkouts **before** the confirmation prompt and
  **before the agents are stopped** (`stack.Down` runs after). Agents keep working during the prompt, then
  `os.RemoveAll` (`:129`) deletes what they wrote since. Order should be: stop agents, inspect, ask, remove.
  `gitprep.Inspect` (`gitprep.go:94`) also ignores: ignored-but-valuable files (`.env`, local data), `git stash`, tags,
  commits on a detached HEAD (`log --branches` skips them), and other refs.
  Fix: reorder; count `git stash list`, detached HEAD commits, `--ignored` files (report separately); refuse when
  `git status` fails for any directory (it does already).

- [x] **R-25 `RemoveAll` is not limited to checkouts.** The removal list is any existing `GitDirs` path, not only
  directories with a `.git`. With `path: .` and an agent named `docs`/`src`, `down --workspaces` deletes the user's
  own `./docs`. Fix: only remove directories that contain `.git` (or carry an egzo marker file), refuse paths equal to
  or above the project directory, `$HOME` or `/`, and refuse symlinked roots.

- [x] **R-26 `InspectCheckouts` runs agent-authored git config in one container that sees all checkouts.**
  `stack/git.go:269`: `git status` with `safe.directory=*` executes `core.fsmonitor` and clean filters from the agent-controlled
  `.git/config` and `.gitattributes`. The prep container has no network and read-only mounts, but it sees **every**
  agent's checkout, so one compromised agent can read the others' work. The output is also parsed with
  `fmt.Sscanf(target, "/check/%d", &index)` followed by `existing[index]` (`git.go:304`) with no bounds check, so a forged line
  panics the CLI. Fix: one prep container per checkout, only that checkout mounted; R-05's `-c` flags; read
  stdout only; bounds-check the index.

### Container hardening and engine handling
- [x] **R-27 agents and sidecars have no pids, memory or fd limits, and sidecars run as root.**
  `createContainer` (`stack/apply.go:115`) sets `NanoCPUs` and `Memory` only when the YAML does. No `PidsLimit`, no
  `Ulimits`, no `MemorySwap`. A fork bomb in an agent takes the host. The control and proxy containers run with the
  image's default user (root, `Dockerfile`), with all caps dropped and a read-only rootfs but no memory cap.
  Fix: default `pids_limit` for agents (configurable), memory caps for sidecars, a non-root `USER` and `User:` for
  both sidecars (chown their volumes in the prep step). Spec for the defaults.

- [x] **R-28 `ensureImage` ignores errors reported inside the pull stream.**
  `stack/apply.go:264` does `io.Copy(io.Discard, reader)`. The engine reports a failed layer as JSON in the stream with
  status 200, so a failed or partial pull looks like success and `ContainerCreate` fails later with "no such image".
  Pulls also carry no registry credentials, so private `image:` values cannot be pulled.
  Fix: decode the JSON messages, fail on `error`/`errorDetail`; support `~/.docker/config.json` auth (or document it).

- [x] **R-29 agents run as uid 0 when egzo runs as root, and the engine's user mapping is only half handled.**
  `AgentUser` (`stack/up.go:31`) returns `uid:gid` of the invoking user, so `sudo egzo up` runs agents as root;
  rootless Docker maps that uid to a sub-uid and bind-mounted files end up unreadable. Fix: refuse or fall back to 1000
  when uid is 0; detect rootless Docker (`info.SecurityOptions` is already read by `doctor`) and handle it like Podman, or
  say it is unsupported.

- [x] **R-30 a container created but never started is later started without its volume ownership.**
  `createContainer` (`apply.go:115`): create, then `chownVolumes`, then start. If egzo is interrupted between create and
  chown, the next `up` sees an existing container with the same hash that is not running and issues a plain `start`
  (`stack/plan.go`, the `existing.State != "running"` case). The agent runs with root-owned volumes. Fix: remove containers in
  state `created` before reconciling, or chown on every start.

- [x] **R-31 user `env:` can override egzo's own variables.** `stack/desired.go:267` applies `agent.Env` last, so
  `EGZO_TOKEN`, `EGZO_CONTROL_URL`, `HTTPS_PROXY`, `NO_PROXY` and `SSL_CERT_FILE` can be replaced silently. Fix: reject
  `EGZO_*` and the proxy/CA variables in `checkEnv`, or print a warning.

### Config validation (`internal/config`)
- [x] **R-32 [verified] the "no users in files" check rejects legitimate keys named `users`.**
  `rejectUsers` (`config/load.go:77`) walks every mapping in the file, so `env: { users: 4 }`, a workspace or vault secret
  named `users`, all fail with "users are never declared in project files". Fix: check only the structural positions
  where a users section could appear (top level, `agents.*`, `egress.*`), or say "key `users` here".

- [x] **R-33 services bound to a string secret but defined without `inject` accept the secret silently**
  (`config/egress.go:127`): `existing.Secret = entry.Ref` on a pure allowlist service is useless and reported by nobody;
  the object form already errors. Add the same error.

- [x] **R-34 more validation gaps (from reading; none reproduced):**
  - `workdir: repo/../../etc` passes the prefix test (`workspaces.go:216`) and reaches Docker un-cleaned; `filepath.Clean`
    first.
  - an inline host path of `/`, or one containing the Docker socket's directory (`/var/run`), mounts straight through; at
    least warn, since the design says never to expose the socket (`workspaces.go:177`).
  - `egress` host entries are compared case-sensitively in `reaches` and the duplicate-host check (`egress.go:246`,
    and `checkProfile`) while the proxy lowercases (`policy.go`); normalise (lowercase, strip trailing dot) once, in config.
  - `resources.cpus` negative and `memory` strings are only checked at `up` time (`desired.go`), after other resources
    may be created. Move to validation.

## P1: design and spec deviations

- [x] **R-35 agents can start before the proxy is healthy and before control and proxy are on their network.**
  The design says "Agents start after the proxy is healthy". `dependencies()` (`schedule.go:12`) makes an agent's `create` wait
  for its network and volumes only, not for `create/start container proxy|control` or the `connect` actions. On a
  slow engine the harness starts with no `control` alias: the `SessionStart` hook and the first `/v1/activity` are lost
  (the holder and `egzo hook` ignore failures), so the agent stays "starting" and never receives a message until it
  finishes a turn on its own. The git path avoids this with `splitAgentContainers`, the default path does not.
  Fix: add the dependencies; retry the first reports (R-41). Spec with an artificially slow proxy start.

- [x] **R-36 configuration that is accepted and ignored.**
  `proxy.image` and `proxy.audit` (`config/types.go:37`) have no effect: `Desire` always uses the all-in-one image and always audits.
  `agent.tools` and `control.tools` (`config/resolve.go:201`) are only echoed: the MCP server always offers all eight tools
  (`control/mcp.go`). The design's example uses `tools: [control]`. Either implement them (and fail a spec until they
  are) or remove them from the schema; silently accepting a key that looks like a security control (`tools`) is the worst
  option.

- [x] **R-37 an agent can get stuck in `working` forever, and hook delivery has no retry.**
  Activity is only changed by hooks. The design admits Esc may not fire `Stop`; a crashed harness never fires it.
  `claim` only announces to an `idle` agent (`delivery.go:114`), so messages wait forever.
  `egzo hook` posts once with a 3 s timeout and ignores errors (`cli/agent.go`, `newHookCommand`).
  Fix: a watchdog in the holder (no output and no hook for N minutes means ask control to mark idle, or fall back to
  quiescence), retry with backoff for hooks, and `unconfirmed` handling for the stuck case.

- [x] **R-38 `egzo diff` is not the same algorithm as `up --dry-run`.**
  It drops every `would check…` line (`cli/ops.go:130`), so a rotated secret (policy hash change) is not a difference, yet
  `up` would push it. The design says `diff` = the dry-run. Fix: have the dry run report "policy differs" when it can
  compute the hash (secrets are readable on the CLI).

- [x] **R-39 the prompt file's content is not in the config hash.**
  `desired.go:272` mounts it (path only is hashed). Editing the prompt changes nothing for `up`, but the harness took
  `--append-system-prompt` at start, so the running agent is stale and nothing says so. Fix: hash the file content,
  as other inputs are hashed.

- [x] **R-40 `ExecStream` can report the wrong exit code.**
  `engine/stream.go:89` inspects the exec once after the stream ends. `Exec` (`exec.go:24`) polls up to 10 s precisely because "the
  exit code is only reported once the process is gone"; `ExecStream` does not, and it carries `egzo exec`, `attach` and
  `send --wait` (`ControlStream`). Fix: share the polling. Not reproduced here; test against a fake engine.

- [x] **R-41 project identity uses the path as typed.**
  `loadProject` (`cli/project.go:37`) and `CheckOwnership` (`stack/observed.go:116`) compare `project-dir` strings. The same directory
  through a symlink (or `$PWD`) looks like "another directory", which refuses every command. Use `EvalSymlinks`.

- [ ] **R-42 smaller deviations from the design text:**
  - `egzo doctor` has no network check (design: "engine, rootless, gVisor, network checks"); it should also warn on
    world-readable secret files and on a `.egzo/` that is not git-ignored (done).
  - `egzo init --harness custom` writes a file that fails validation (no `image:`); `pi` is accepted with no integration
    (known). `scaffold` interpolates the URL into YAML unquoted (`cli/init.go`).
  - `AgentUser`/uid mapping on Podman stays an open question in the design: record the decision.
  - `Down` removes containers it did not create if they carry the project label, including a `prep` container of a concurrent
    command; there is no project-level lock, so two `egzo up` runs race (`stack/plan.go`, `stack/up.go`). Add a lock
    (a lock file under `.egzo/`, or a label-based one) or document it.
  - The OpenCode harness always sets `ANTHROPIC_API_KEY` even when the profile injects a bearer token
    (`harness/opencode.go`), so `anthropic-oauth` with OpenCode fails with no config-time warning.

## P2: error handling

- [ ] **R-43 silent failures to audit and fix.** `stack/up.go:386` `putProject` ignores its error (control then does not know
  the agents; `message` to a valid agent answers "no agent"); `control/delivery.go:167` `expire` ignores append errors;
  `session/client.go` ignores `out.Write` errors and `writeFrame` errors; `cli/ops.go` ignores the rotated-CA JSON error;
  `harness/merge.go:27` replaces an existing **corrupt** `.claude.json` / `settings.json` with `{}` and discards the user's
  conversation state with no backup (rename to `.bak` and warn); `merge` decodes numbers as `float64`, so large integers
  lose precision (use `json.Decoder.UseNumber`); `agentclient.Do` truncates replies at 1 MB silently
  (`agentclient.go`); `engine.Connect` and `Exec` have no timeouts (a wedged sidecar hangs `up` forever; the
  `ctx` is only cancelled by Ctrl-C); the second Ctrl-C is swallowed because `NotifyContext` keeps capturing signals
  until the command returns (`cli/engine_commands.go:67`, call `stop()` when the context is done).

- [x] **R-44 `secrets set` writes before it restricts permissions.** `cli/secrets.go:103` truncates and writes an existing
  0644 file, then `chmod` (`os.Chmod` right after). Create with `O_CREATE|O_TRUNC` mode 0600 on a temp file and rename; refuse
  symlinks. `readValue` stores only the first line (a multi-line key is truncated silently) and `secrets ls` hides why
  a secret is "missing" (permission denied versus not found). `readSecret` does not warn on group/world-readable secret
  files.

- [x] **R-45 session robustness.** One client that stops reading blocks `pump` (`holder.go:107`, send at `:126`) and therefore the
  harness: `c.send` has no write deadline and runs sequentially, so a hung `docker exec` stream or suspended terminal
  freezes the TUI for every client, and `wait()` (`holder.go:83`) sends the exit frame under `h.mu`, so it can hang the
  holder at exit. Give each client a bounded queue (drop slow clients) and a write deadline. `handle` has no read deadline for
  the hello frame, and `Inject` interleaves with a human's input because master writes are not serialised (accepted by
  design, but the two-step paste + Enter should hold a write lock). Clients should reset terminal modes on detach
  (mouse tracking, bracketed paste, focus events, keyboard protocol); today a detach from a TUI that enabled them leaves the
  user's shell garbled (check against the fidelity specs).

## P2: over- and under-optimisation

Over-engineered or wasteful:
- [x] **R-46 the control sidecar scans everything, all the time.**
  `interruptPending` (`events.go:118`) walks the whole log backwards when the agent never had an interrupt; `since`, `statuses`, `activity`
  and `overlay` each scan or copy all events or messages; `claim` does three `selectMessages` copies per call; every holder polls
  `/v1/claim` once a second; `expire` rescans every 500 ms forever. With two hook events per tool call and no
  retention (R-15), cost per request grows without bound. Fix: keep per-agent state (activity, pending interrupt, open
  counts, queue) updated in `index`, and use the existing `wake` channel for claims instead of polling.
- [ ] **R-47** `projectKey` reads a file on every authenticated request (R-19); `mcp.NewServer` per request (R-18);
  `Observe` makes one `ContainerInspect` per container, sequentially (`stack/observed.go:62`);
  `agentTokens` starts one exec per agent with no bound (`stack/up.go:159`); `chownVolumes` recursively chowns every
  writable volume at every container (re)creation, walking whole `node_modules` trees (`cli/prep.go`: skip when the
  root already has the right owner, or only fix files that differ); `Holder.tail` is re-sliced and re-appended per chunk,
  copying up to 64 KB each time (`holder.go:116`), so use a ring buffer.

Under-built:
- [ ] **R-48** no log rotation or size limits for the sidecars' logs and the event log (R-11, R-15); no retention for
  `specs/*.yaml` (R-21); no per-client bounds in the holder (R-45); no resource limits anywhere (R-27); no idle limits in
  the proxy (R-08); audit lacks byte counts (R-11); no health signal for "proxy has no policy" (R-12).

## Missing tests

Coverage today: `config` 99%, `harness` 91%, `gitprep` 88%, `agentclient` 84%, `operator` 84%, `control` 73%, `session` 72%, `proxy` 72%,
`stack` 49%, `cli` 37%, `engine` 7%. The pytest specs cover the engine-bound paths end to end, but nothing
below is exercised without a Docker engine, and these are the places the findings above live.

- [x] **T-01 control: the plain HTTP agent verbs** (`list`, `send`, `get`, `resolve`, `update`, `ask`, `agents` in `agentapi.go`) are at 0%:
  only the MCP path is unit tested. Add table tests for auth failure, wrong agent, body limits, and status codes.
- [x] **T-02 control: operator handlers** `token`, `putSpec`/`getSpec`/`listSpecs`, `resolveForOperator`, `putProject`, `Run`, and `ensureState`
  (0%). Include traversal-looking hashes, oversized specs (R-21) and a malformed `project.json`.
- [x] **T-03 control: store reload.** A log with a corrupt line, a duplicate Seq (R-23), a 1 MB line, and replay of
  every state transition; the `fetch`/`claim` race (R-22) with `-race` and `t.Parallel`; rate limit across `update`.
- [x] **T-04 proxy:** `Run`, `operatorHandler` (`PUT /policy` oversize and bad JSON, `POST /ca/rotate` failing at `Publish`, `GET /policy`);
  `parseServerName` with fragmented ClientHello, ECH, trailing-dot and IP SNI; CONNECT with pipelined ClientHello (R-09);
  tunnel idle and cap behaviour (R-08); `Dial` returning private IPs (R-07); `Minter` growth (R-13); `parseCA` mismatch (R-14).
- [x] **T-05 engine:** `Exec` and `ExecStream` against a fake Docker API (`httptest` speaking the hijack protocol) for exit codes, stdin close
  and cancellation (R-40). Also `Connect` with an unreachable `DOCKER_HOST`.
- [ ] **T-06 stack:** `Desire` collisions (R-03), `dependencies`/`runConcurrently` with cycles (R-02) and with proxy/control ordering
  (R-35), `restrictPlan`, `pushPolicy` and `pushSnapshot` against a fake exec, `Up` with a missing secret leaving nothing
  created, `InspectCheckouts` output parsing with forged lines (R-26), `RunPrep` cleanup on cancel, `ensureImage` with an error in
  the pull stream (R-28), `Down` ordering. A small fake of `client.APIClient` pays for all of these.
- [ ] **T-07 cli:** `down --workspaces` chooser (order, prompt, `--force`, `RemoveAll` scope: R-24 and R-25); `ps` / `messages` output sanitising (R-06);
  `send --wait` exit codes 0/3/4/5 with a fake stream; `secrets set` permissions (R-44); `runAgent`, `newHookCommand`
  (retry, timeout), `service()` with a colliding name; `init` with a credentialed `origin` (R-04).
- [x] **T-08 session:** a slow client does not block the program (R-45); attach after exit (R-01); `Inject` interleaving; `Attach`
  (0%: detach inside pasted data, resize, exit frame).
- [x] **T-09 harness:** `mergeFile` with a corrupt existing file (R-43) and large integers; OpenCode `ContainerEnv` and a bearer profile; the
  plan file modes (opencode.json carries the agent's Basic credentials and is written 0644: `harness.go:117`, should be 0600).
- [x] **T-10 config:** table tests for every new rule above (names, cycles, reserved names, userinfo, branch, env reservations,
  case and trailing-dot hosts, resources). Add specs where the user sees the message.
- [x] **T-11 hygiene:** `golangci-lint` reports 56 issues (50 `errcheck`, 4 `staticcheck`, 2 `unused`): unused test helpers `call`
  (`control_test.go:273`) and `mustNot` (`messages_test.go:29`), a self-comparison in `control_test.go:78` (SA4000: a
  determinism check that needs a different shape), `client.IsErrNotFound` deprecated (`stack/apply.go:254`). Add `staticcheck`,
  `golangci-lint` and `go test -race` to CI.
- [ ] **T-12 supply chain:** `govulncheck` flags GO-2026-4887 (Moby AuthZ bypass with oversized request bodies) in
  `github.com/docker/docker v28.5.2+incompatible`. The issue is server-side and has no fixed version, so egzo's client is not
  exposed in practice, but track it and plan the move to the split `github.com/moby/moby/client` modules, since
  `IsErrNotFound` is already deprecated.
