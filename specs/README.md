# egzo specs

The executable specification of egzo. These are black-box tests that drive the real `egzo`
binary. Running the suite tells you what is done and what is not, so no
other document has to be trusted for that.

## Running

```console
$ python3 -m venv specs/.venv          # on Debian/Ubuntu without python3-venv, see below
$ specs/.venv/bin/pip install -r specs/requirements.txt
$ EGZO_BIN=/path/to/egzo specs/.venv/bin/pytest specs --engine docker
```

`EGZO_BIN` defaults to `egzo` on `PATH`. A run tests egzo on **one reference platform**. A platform says what
the host is like; it does not ask egzo for anything:

| platform | the host is | socket |
|---|---|---|
| `docker` | a rootful Docker host | `/var/run/docker.sock` |
| `docker-gvisor` | the same, with `runsc` (gVisor) as the engine's default runtime | `/var/run/docker.sock` |
| `podman` | a rootful Podman host | `/run/podman/podman.sock` (this user needs access to it) |
| `podman-rootless` | a rootless Podman host | `$XDG_RUNTIME_DIR/podman/podman.sock` |

egzo never chooses a runtime: containers run under the engine's default one, so gVisor is a property of the host, which
`docker-gvisor` makes true. A `podman-gvisor` platform would be Podman with `runsc` as its default in `containers.conf`; there is none yet.

Name the platform with `--engine NAME` (or `EGZO_ENGINE`), as CI does, one machine per platform. With none
named, `select_platform.py` picks the first one this machine can be, in the order `docker-gvisor`,
`podman-rootless`, `docker`, `podman`, and the run prints what it found, what it chose and how to override
(`python specs/select_platform.py` prints just that). A platform that is named but missing fails the engine
specs with what to set up.

A spec that needs a feature a platform lacks is marked to fail there (strict xfail), so it shows as unsupported on
that platform. Today none does: the one gVisor spec checks, from inside an agent, that it runs under gVisor exactly
when the platform says so. Nothing is probed or skipped: egzo is asked, and the outcome is the result. CI runs them on every platform: see [In CI](#in-ci).

Without `python3-venv`: `python3 -m venv --without-pip specs/.venv`, then bootstrap pip with
`get-pip.py`.

## In CI

The specs run on GitHub Actions, one fresh VM per reference platform (`.github/workflows/specs.yml`, set up by `scripts/ci-engine.sh`):

| scenario | the host is | in CI |
|---|---|---|
| `docker` | a typical rootful Docker host, no gVisor | required |
| `docker-arm64` | the same, on arm64 | experimental |
| `docker-gvisor` | Docker with `runsc` as the engine's default runtime | experimental |
| `podman-rootless` | a rootless Podman host, no gVisor | experimental |
| `podman` | a rootful Podman host, no gVisor | experimental, and not a supported platform |

Every cell is Ubuntu 26.04, so a green cell reads as "works on Ubuntu 26.04 for that platform" ([#139](https://github.com/egzo-ai/egzo/issues/139)
for other distributions). Still open: red specs for unbuilt features against the required check
([#135](https://github.com/egzo-ai/egzo/issues/135)), and the other cells becoming required ([#136](https://github.com/egzo-ai/egzo/issues/136)).

### Decided

- **GitHub Actions, hosted runners.** A matrix cell is a job, a job is a fresh VM, so a cell never shares a kernel, a daemon or
  a network pool with another: Docker and Podman are never on the same machine. `ubuntu-26.04` carries Docker and Podman
  already (the runner image README lists Docker 29.4.2 and Podman 5.7.0); gVisor and the Podman API sockets are set up by
  `scripts/ci-engine.sh`.
- **When it runs** (`.github/workflows/specs.yml`):

  | Event | Platforms | Blocks? |
  |---|---|---|
  | `pull_request` | `docker` | yes: `specs (docker)` is the required check for merging into `main` |
  | a push to `main` | all | no; a release tag requires its required cells to have passed |
  | nightly, and by hand | all | no; a red night is a finding |
  | a release tag | none | `release.yml` does not run them again: it builds nothing unless the tagged commit is on `main` and its push passed the required cells |

  A pull request gets the Docker cell, and merges once it passes: `main` is linear (rebase merges only, no merge queue), and a
  pull request need not be up to date with `main` to merge, as "Rebase and merge" rebases it then. The whole matrix runs on
  every push to `main`, after the merge, so what one platform rejects, or two pull requests that pass apart and break together,
  shows there. The nightly run catches what no push touched: a new runner image
  (an engine version), a new engine release, an online service the specs reach going away.
- **Cells that are not proven are `experimental`**: they run and show, and cannot fail the run. `docker` is required from the
  start. `docker-gvisor`, `podman-rootless` and `docker-arm64` become required, one by one, after a clean history ([#136](https://github.com/egzo-ai/egzo/issues/136)), by
  removing the flag in `specs.yml` and adding the check to the branch rules. Docker and rootless Podman are supported platforms.
  Rootful Podman (`podman`) is not supported for now: it has a diagnosed intermittent failure
  ([docs/podman.md](../docs/podman.md), netavark leaves a stale per-bridge sysctl file that switches forwarding off
  on a reused bridge name, containers/netavark#1497). Its cell stays `experimental` and its specs stay red, as they say what is true.
  Whether it is supported in 1.0.0, probably with a minimum Podman and netavark version, is decided in
  [#2](https://github.com/egzo-ai/egzo/issues/2).
- **Serial.** Each cell runs the specs one at a time. Four `pytest-xdist` workers (`--dist loadfile`) were tried on the first
  runs: locally with Docker and gVisor they took 3 minutes instead of about 12, but 54 specs failed on `all predefined address
  pools have been fully subnetted` (every project and instance is a network and Docker's default pool holds about 30), and with
  the pool widened the timing-sensitive specs (a terminal's colours, the pty attach, the browser, the stand-in agents' timings)
  failed on three platforms. Serial passes on every Docker cell. Stable runs are preferred to fast ones, so xdist and the pool
  setting are gone; parallel runs stay out ("not for now", [#123](https://github.com/egzo-ai/egzo/issues/123)).
- **Podman cells.** Things the first runs found that are about the host, not egzo, and that `scripts/ci-engine.sh` sets up:
  the rootful socket is served by `podman system service` in a `/run/podman` the runner user can enter (systemd leaves it
  root-only); the test registry on `localhost:5000` is declared insecure (Podman does not trust localhost for plain HTTP as Docker
  does); and the rootful cell runs without Ubuntu's AppArmor container profile
  ([docs/podman.md](../docs/podman.md#rootful-podman-on-apparmor-hosts-exec-cannot-reach-the-containers-processes)). gVisor comes from its release archive (`gvisor.tar.bz2`), as the separate
  `runsc` files are gone. Podman 5.7 refuses the `uid=` option of a tmpfs mount, so egzo's sidecar tmpfs use `mode=1777`.
- **Artifacts.** Each cell uploads the log of its run (the summary per area is its last lines) and a JUnit file.

### What a cell must show

- The suite runs to the end with the platform's expected result. Specs that need a feature the platform lacks (none
  today) are strict xfail there, so a cell that **unexpectedly passes or fails is a finding**,
  not noise.
- The summary per area (one area per `test_<area>.py`) is kept as an artifact, so a regression on one platform shows as
  a change in a column.
- gVisor is checked on a pair of platforms: an agent really runs under gVisor where it is the engine's default runtime,
  and does not where it is not (`test_agents_run_under_the_engines_default_runtime…`).
- Known host-level problems stay visible: the rootful Podman intermittent egress failure
  ([docs/podman.md](../docs/podman.md)) should show up as a flaky cell and not be hidden by retries.

## Status model

| Outcome | Column | Meaning |
|---|---|---|
| pass | done | the behaviour exists and works |
| fail | broken | the spec fails: the feature is not built yet, or it is wrong |
| xfail (strict) | unsupported | this reference platform cannot do it (gVisor on a platform without gVisor) |
| skip | skipped | does not apply to this platform (for example a spec about Podman on Docker) |

**The failing specs are the todo list.** A spec for a feature that is not built fails; there is no marker
that turns it green, and no `xfail` or `skip` for "not implemented", "not decided" or "blocked" (a blocked
spec fails and says what it waits for). `xfail` is only for platforms, and is strict, so a pass on a
platform said to lack the feature is reported as broken too. The summary at the end of every run counts each
outcome per area (one area per `test_<area>.py`).

## Writing specs

- Write a spec before the feature and make its assertions the exact behaviour you want. It must fail for
  the right reason, not because it is half written. Leave it failing until the feature lands.
- A "valid input is accepted" spec only means something next to the rejection specs that pin the
  rules down. A binary that validates nothing passes it.
- Use `support.py` builders (`spec`, `agent`, `anthropic_profile`, ...) so a spec reads as the YAML
  it describes. Specs that need an engine take `live_project`, which cleans up everything it created.
- Specs assert behaviour at the CLI and engine boundary only, never implementation details.
- **Write the command as the shell would take it**: `project.sh("agent spawn coder issue-1 -m hi")`, or `project.ok(...)` when it
  must exit 0; values with spaces go after the string (`project.ok("message send coder", text)`). The line is checked against
  the CLI's own help, at run time and, for every literal, by a spec in `test_meta.py`, so a renamed command or flag fails the spec.
  `run` is for a line that is meant to be wrong.
- **Share what can be shared.** `shared_project` and `team` (in `conftest.py` and `agentapi.py`) bring one project up for a whole
  module, with a template `worker` and the instances `coder` and `reviewer`. A spec that counts something (open requests, a rate
  limit, the rows of `ps`) takes fresh agents with `pair`. A spec about `up`, `down`, stale instances or the lock keeps
  `live_project`. Prefer a scenario that follows one story, with an assertion at each step, over many specs that each set the
  same thing up; the engine specs of a module should cost about one `up`.

## Helpers for a spec that needs a running agent

`up` starts no agent: it makes the infrastructure and publishes the templates. The `live_project` fixture has
`up(document=None)`, `spawn(template, name=None, *flags)` (returns the instance name; `spawn("claude")` is
`claude-1`, container `<project>-claude-1`), `start(*templates)` (`up`, then one instance of each) and
`container(instance)`. The `engine` fixture has `instances(project)` and `instance(project, name)`.

## Fixtures

`fixtures/` holds the programs the specs run *inside* an agent: `fake-tui.sh` (a stand-in TUI), `tls_client.py` (sends one
ClientHello through the proxy, with or without Encrypted Client Hello, and says what answered; the spec agent image
carries `python3` for it) and `mcp_client.mjs` (starts the MCP server a harness is configured with and calls its tools,
for the browser specs).

### Servers the specs bring along

A spec must not depend on a site it does not control. `ssh_server` (`sshd.py`, image in `fixtures/sshd/`) is a real OpenSSH
server in a container of the engine under test, up for the whole run, with one unprivileged user (`ops`), key authentication only,
and keys made when it starts: `host`, `port`, `user`, `private_key`, `public_key`, `host_key`, `fingerprint` (`SHA256:...`), `sh(script)` to
look inside, and `keygen()` for a key pair it does not know. It is published on the host's address (on a rootless Podman, under
the name Podman gives the host), which a container of every platform can reach. It is the first piece of a server of the specs' own:
the HTTPS sites the proxy specs fetch today are meant to move here, so that the suite needs no internet.

## Output contract assumed by the specs

`egzo project check --resolved` prints the resolved configuration as YAML and exits non-zero with a message on stderr
when the file is invalid. The specs read these keys:

```yaml
name: <project>
workspaces:
  <name>: {} | { git: {url, branch} }   # a volume either way; there is no path (an engine volume has no host directory)
agents:
  <agent>:
    workdir: /workspace/<name> | /workspace
    egress: <profile>
    workspaces:
      - { name, mount, mode: rw|ro }
egress:
  <profile>: { allow: [...], services: { <name>: { hosts, inject?, secret?, placeholder?, inspect? } },
               ssh: { <name>: { host, port, user, key, host_key } } }
```

`agents:` entries are templates; there is no `depends_on`, no cross-agent workspace reference and no host path.
Warnings (for example an agent whose profile cannot reach its harness's provider) go to stderr and
do not change the exit code. Changing this contract means changing these specs first.

## Areas

The specs are laid out by the command they are about, as the CLI is (`egzo project`, `egzo agent`, ...). A file's docstring says what
it covers and how the project it runs on is shared.

| File | Covers |
|---|---|
| `test_spec_fixtures.py` | the servers the specs bring along (the ssh server) do what the specs rely on |
| `test_meta.py` | the status model itself; every command line a spec writes is one egzo has; every command and flag of egzo is run by a spec |
| `test_schema.py` | validation of `egzo.yaml` (`egzo project check --resolved`), no users in files, no secrets in output, the `inject` section |
| `test_egress_ssh.py` | ssh servers in a profile: fields, the pinned host key, the key reference, extend |
| `test_egress.py` | profiles, services (wildcards only without a secret), built-ins, extend, placeholders, by-host rules, reachability warnings |
| `test_secrets.py` | vaults, the `env` and `pass` backends (with a stand-in `pass`) |
| `test_workspaces.py` | the declaration of workspaces: volumes, no `path`, references, mounts, working directory |
| `test_init.py` | `egzo project init`: the scaffolded file |
| `test_project_name.py` | name resolution and the same-name-other-directory refusal |
| `test_project_check.py` | `egzo project check`, and `egzo msg` as the alias of `message` |
| `test_images.py` | harness image names, pulled from a registry (`EGZO_SPEC_REGISTRY`, default localhost:5000), `egzo version` |
| `project/test_project_up_down.py` | `project up` and `project down` in one story |
| `project/test_project_operations.py` | `up --dry-run`, `rotate-ca`, `proxy log` and `rules`, the operator API under `/v1`, the project lock and `unlock` |
| `project/test_project_labels.py` | the `ai.egzo.*` label contract |
| `project/test_project_logs.py` | the engine's default logging; the control sidecar's JSON logs, status on standard output |
| `project/test_project_build.py` | `build:` on an agent: `up` builds through the engine's build endpoint, by content, `.dockerignore`, base images, staleness, `down` |
| `project/test_project_snapshot.py` | the resolved project kept on the control volume, read through `/v1` |
| `system/test_system_check.py` | `egzo system check`: engine, isolation, log driver, git |
| `agent/test_agent_spawn.py` | `agent spawn`: names, exit 17, published templates, `-m`, `--wait`, `--attach`, refusals |
| `agent/test_agent_lifecycle.py` | `ps`, `agent ls`, `agent rm` and its filters, `start`, `stop`, what a removed container leaves |
| `agent/test_agent_stale.py` | stale instances, the `up` refusal, `down` and a name that was an instance |
| `agent/test_agent_containers.py` | the network, who an agent runs as, limits, whether it runs under gVisor on a gVisor platform, and the workspace volumes it mounts |
| `service/test_service_commands.py` | `logs`, `exec` (with and without a terminal), `start`, `stop`, `restart` on an instance or a sidecar |
| `message/test_message_cli.py` | `message send|ls|reply`, `--wait`, what a person sees in `ps` and `events` |
| `message/test_agent_api.py` | what an agent does to the control sidecar: the HTTP tools, MCP, state reports, and the limits |
| `message/test_message_announcements.py` | agent states, announcing queued messages, the human-quiet rule, acks, interrupt |
| `session/test_session_attach.py` | `agent attach` and the session fidelity matrix, against a stand-in TUI |
| `proxy/test_proxy_isolation.py` | how an agent reaches the proxy, private addresses, Encrypted Client Hello (no internet needed) |
| `proxy/test_proxy_egress.py` | profiles, the audit trail, `inspect` and wildcard services, bindings of instances (needs the internet) |
| `proxy/test_proxy_secrets.py` | injection, placeholders, rotation, refused secrets (needs httpbin.org) |
| `harness/test_harness_integrations.py` | the Claude Code and OpenCode images: bypass, first-run state, state reports, MCP, the real TUIs |
| `browser/test_browser.py` | the Chromium and Playwright MCP server built into both harness images, browsing through the proxy, a form login with a placeholder |
| `workspace/test_workspace_commands.py` | `workspace ls|put|get|rm|fetch` |
| `workspace/test_git_workspaces.py` | the base clone at `up`, a worktree per instance at spawn, fetch, `--reuse`, removal with the instance (clones from github.com) |

## Not covered

A model is never called: the harness specs stop at what egzo configures, what the TUI shows and what the
harness reports as states (prompt submitted, session idle), which needs no credential. The specs that need
the outside world (github.com, httpbin.org, example.com, the registry) fail when it is unreachable (a known weakness:
[#105](https://github.com/egzo-ai/egzo/issues/105)).
The harness specs build the real images from `harness/` (a few minutes the first time, cached by content).
