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

## Output contract assumed by the specs

`egzo config` prints the resolved configuration as YAML and exits non-zero with a message on stderr
when the file is invalid. The specs read these keys:

```yaml
name: <project>
workspaces:
  <name>: { mode: clone|worktree|shared, path: <abs host dir> }   # git workspaces
agents:
  <agent>:
    workdir: /workspace/<name> | /workspace
    egress: <profile>
    workspaces:
      - { name, mount, mode: rw|ro, host_path?, from? }
egress:
  <profile>: { allow: [...], services: { <name>: { hosts, inject?, secret? } } }
```

Warnings (for example an agent whose profile cannot reach its harness's provider) go to stderr and
do not change the exit code. Changing this contract means changing these specs first.

## Areas

| File | Covers |
|---|---|
| `test_meta.py` | the status model itself |
| `test_schema.py` | validation of `egzo.yaml`, no users in files, no secrets in output |
| `test_project_name.py` | name resolution and the same-name-other-directory refusal |
| `test_workspaces.py` | mounts, working directory, git sources, cross-agent references |
| `test_egress.py` | profiles, services, built-ins, extend, reachability warnings |
| `test_labels.py` | the `ai.egzo.*` label contract |
| `test_up_down.py` | `up`, `down`, `--dry-run`, idempotency, workspace safety |
| `test_agents.py` | one container per agent, networks, isolation, who agents run as |
| `test_proxy.py` | injection, TLS, deny unless allowed, per-agent policy, CA, audit |
| `test_session.py` | `egzo attach` and the session fidelity matrix, against a stand-in TUI |
| `test_injection.py` | agent states, delivering queued messages, the human-quiet rule, acks, interrupt |
| `test_harnesses.py` | the Claude Code and OpenCode images: bypass, first-run state, hooks, MCP, the real TUIs |
| `test_git_workspaces.py` | the prep container: clone, shared, worktree, `down --workspaces` safety (clones from github.com) |
| `test_operations.py` | `secrets`, `doctor`, `diff`, `ca rotate`, `up AGENT`, `depends_on`, `proxy rules`, `handoff` |
| `test_images.py` | harness image names, pulled from a registry (`EGZO_SPEC_REGISTRY`, default localhost:5000) |

## Not covered

A model is never called: the harness specs stop at what egzo configures, what the TUI shows and what the
harness reports through hooks (prompt submitted, session idle), which needs no credential. The specs that need
the outside world (github.com, httpbin.org, example.com, the registry) fail when it is unreachable.
The harness specs build the real images from `harness/` (a few minutes the first time, cached by content).
