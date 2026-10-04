# egzo specs

The executable specification of egzo. These are black-box tests that drive the real `egzo`
binary. Running the suite tells you what is done, what is still todo and what is broken, so no
other document has to be trusted for that.

## Running

```console
$ python3 -m venv specs/.venv          # on Debian/Ubuntu without python3-venv, see below
$ specs/.venv/bin/pip install -r specs/requirements.txt
$ EGZO_BIN=/path/to/egzo specs/.venv/bin/pytest specs --engine docker
```

`EGZO_BIN` defaults to `egzo` on `PATH`. A run tests **one host scenario**, chosen with
`--engine NAME` (or `EGZO_ENGINE`). CI runs one machine per scenario, so nothing is multiplied
across engines inside a run. Specs that observe the container engine get that scenario from the
`engine` fixture:

| `--engine` | engine | socket |
|---|---|---|
| `docker` | Docker (rootful) | `/var/run/docker.sock` |
| `docker-gvisor` | the same Docker, every agent under `runsc` (gVisor) | `/var/run/docker.sock` |
| `podman` | Podman, rootful | `/run/podman/podman.sock` (this user needs access to it) |
| `podman-rootless` | Podman, rootless | `$XDG_RUNTIME_DIR/podman/podman.sock` |

Without `--engine`, the engine specs **fail** and list the choices. If the chosen engine is missing
on the host they fail too, with what to set up, so a green run means the scenario works. The one
exception is a platform that can never run the scenario (gVisor off Linux), which **skips**. Linux without a registered `runsc` fails.

Without `python3-venv`: `python3 -m venv --without-pip specs/.venv`, then bootstrap pip with
`get-pip.py`.

## Status model

| Outcome | Column | Meaning |
|---|---|---|
| pass | done | the behaviour exists and works |
| xfail (`@pytest.mark.todo`) | todo | specified, not implemented yet |
| fail | broken | implemented (or expected to be) and wrong |
| strict XPASS | promote | a todo spec passes now: remove its marker |
| skip | skipped | cannot run here (gVisor on a non-Linux platform; anything missing on a supported platform fails instead) |

A `todo` spec is a strict xfail, so it cannot rot: when the feature lands the spec starts passing,
the run fails with `promote`, and the author turns it into a regular spec. The summary at the end of
every run counts each outcome per area (one area per `test_<area>.py`).

## Writing specs

- Write a spec before the feature, mark it `@pytest.mark.todo`, and make its assertions the exact
  behaviour you want. A todo spec must fail for the right reason, not because it is half written.
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
