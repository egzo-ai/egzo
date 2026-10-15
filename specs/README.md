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
| `docker-gvisor` | the same, with `runsc` (gVisor) registered | `/var/run/docker.sock` |
| `podman` | a rootful Podman host | `/run/podman/podman.sock` (this user needs access to it) |
| `podman-rootless` | a rootless Podman host | `$XDG_RUNTIME_DIR/podman/podman.sock` |

There is no `podman-gvisor`: Podman's API cannot select a runtime (`known-issues/podman-gvisor-unsupported.md`).

Name the platform with `--engine NAME` (or `EGZO_ENGINE`), as CI does, one machine per platform. With none
named, `select_platform.py` picks the first one this machine can be, in the order `docker-gvisor`,
`podman-rootless`, `docker`, `podman`, and the run prints what it found, what it chose and how to override
(`python specs/select_platform.py` prints just that). A platform that is named but missing fails the engine
specs with what to set up.

Specs that use a feature the platform lacks, which today is only gVisor (`runtime: runsc`), are marked to
fail there (strict xfail), so they show as unsupported on `docker`, `podman` and `podman-rootless` and done on
`docker-gvisor`; one spec requires that a project asking for gVisor cannot start without it. Nothing is
probed or skipped: egzo is asked, and the outcome is the result. The specs have so far only run on one
developer machine: see `known-issues/reference-platform-ci-matrix.md`.

Without `python3-venv`: `python3 -m venv --without-pip specs/.venv`, then bootstrap pip with
`get-pip.py`.

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
  <profile>: { allow: [...], services: { <name>: { hosts, inject?, secret?, placeholder?, inspect? } } }
```

`agents:` entries are templates; there is no `depends_on`, no cross-agent workspace reference and no host path.
Warnings (for example an agent whose profile cannot reach its harness's provider) go to stderr and
do not change the exit code. Changing this contract means changing these specs first.

## Areas

| File | Covers |
|---|---|
| `test_meta.py` | the status model itself |
| `test_schema.py` | validation of `egzo.yaml` (`egzo project check --resolved`), no users in files, no secrets in output |
| `test_init.py` | `egzo project init`: the scaffolded file |
| `test_cli.py` | the shape of the command line: the groups (`project`, `agent`, `message`, `workspace`, `system`) and the top-level commands |
| `test_project_name.py` | name resolution and the same-name-other-directory refusal |
| `test_workspaces.py` | engine-volume workspaces (no `path`), references, mounts, working directory, `egzo workspace put|get|ls|rm`, the lock lease |
| `test_egress.py` | profiles, services (wildcards only without a secret), built-ins, extend, placeholders, by-host rules, reachability warnings |
| `test_secrets.py` | vaults, the `env` and `pass` backends (with a stand-in `pass`), the secrets `project check` and `proxy rules` report |
| `test_labels.py` | the `ai.egzo.*` label contract |
| `test_up_down.py` | `project up` (infrastructure and templates, no agent), `project down`, idempotency, workspace safety |
| `test_spawn.py` | templates and `egzo agent spawn`: names, exit 17, published templates, `-m`, `--wait`, `--attach`, refusals |
| `test_instances.py` | `ps` and `ps --json`, `agent rm`, stale instances and the `project up` refusal |
| `test_agents.py` | one container per instance, networks, isolation, who agents run as |
| `test_proxy.py` | injection, placeholders, `inspect` and wildcard services, TLS, Encrypted Client Hello, deny unless allowed, per-agent policy, CA, audit |
| `test_session.py` | `egzo agent attach` and the session fidelity matrix, against a stand-in TUI |
| `test_injection.py` | agent states, delivering queued messages, the human-quiet rule, acks, interrupt |
| `test_harnesses.py` | the Claude Code and OpenCode images: bypass, first-run state, hooks, MCP, the real TUIs |
| `test_browser.py` | the Chromium and Playwright MCP server built into both harness images: registered with the harness, browsing through the proxy, a form login with a placeholder |
| `test_git_workspaces.py` | the base clone at `up`, a worktree per instance at spawn, fetch, `--reuse`, removal with the instance (clones from github.com) |
| `test_operations.py` | `system check` (including the log driver), `project up --dry-run`, `project rotate-ca`, `up` taking no agent, `proxy rules`, the engine-side project lock |
| `test_images.py` | harness image names, pulled from a registry (`EGZO_SPEC_REGISTRY`, default localhost:5000) |
| `test_daily.py` | `logs`, `exec`, `start`, `stop`, `restart`, `proxy log` |
| `message/test_message_cli.py` | `egzo message send|ls|reply`, `--wait`, what a person sees in `ps` and `events` |
| `message/test_agent_api.py` | what an agent does to the control sidecar: the HTTP tools, MCP, hooks, and the limits |
| `test_logs.py` | the engine's default logging for every container; the control sidecar's JSON logs, status on standard output |
| `test_snapshot.py` | the resolved project kept on the control volume, read through `/v1` |

## Not covered

A model is never called: the harness specs stop at what egzo configures, what the TUI shows and what the
harness reports through hooks (prompt submitted, session idle), which needs no credential. The specs that need
the outside world (github.com, httpbin.org, example.com, the registry) fail when it is unreachable (a known weakness:
`known-issues/specs-depend-on-online-services.md`).
The harness specs build the real images from `harness/` (a few minutes the first time, cached by content).
