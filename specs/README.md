# egzo specs

The executable specification of egzo. These are black-box tests that drive the real `egzo`
binary. Running the suite tells you what is done, what is still todo and what is broken, so no
other document has to be trusted for that.

## Running

```console
$ python3 -m venv specs/.venv          # on Debian/Ubuntu without python3-venv, see below
$ specs/.venv/bin/pip install -r specs/requirements.txt
$ EGZO_BIN=/path/to/egzo specs/.venv/bin/pytest specs
```

`EGZO_BIN` defaults to `egzo` on `PATH`. Specs that observe the container engine use the `engine`
fixture and run once per available engine; `EGZO_SPEC_ENGINES=docker` (default `docker,podman`)
restricts the matrix, and engines that are missing or unusable are skipped.

Without `python3-venv`: `python3 -m venv --without-pip specs/.venv`, then bootstrap pip with
`get-pip.py`.

## Status model

| Outcome | Column | Meaning |
|---|---|---|
| pass | done | the behaviour exists and works |
| xfail (`@pytest.mark.todo`) | todo | specified, not implemented yet |
| fail | broken | implemented (or expected to be) and wrong |
| strict XPASS | promote | a todo spec passes now: remove its marker |
| skip | skipped | cannot run here (for example, no engine) |

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

## Not written yet

Needs agents, a harness integration or the sidecars first: proxy injection and TLS, per-agent
network isolation and "no direct egress", attach and the session fidelity matrix, message
injection and agent states, the control sidecar API, the prep container and clone modes, and the
harness bypass-mode specs.
