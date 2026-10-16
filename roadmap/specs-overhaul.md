# Roadmap: overhaul the specs suite

**Status.** Done for the engine specs (see "What was done"); what is left is listed at the end. Specs stay pytest (a transcript format such
as testscript or cram was considered and rejected: it would throw away the status model in `spec_status.py` and the `pexpect` session specs).

## Why

- **Hard to read.** Specs are disconnected from the CLI: arguments are comma-separated Python strings, behind per-file
  helpers (`custom()` in twelve files, five variants of "up then spawn", several `container()` helpers), with the engine and
  image arriving through hidden fixtures. Nothing ties a spec to a real command, so names go stale: the module docstrings
  still say `egzo spawn`, `egzo config`, `egzo attach`, `prune`.
- **Slow.** About 380 of 571 specs bring up their own project (`up`, `spawn`, `down --force`, then a label sweep). Measured on
  `test_labels.py`: 2.9 s per spec, of which 0.6 s is teardown and 2.3 s the call, most of it `up` and `spawn`. Each egzo
  operation is fast (up 0.7 s, spawn 0.8 s, down 0.4 s); the cost is paying them 380 times.

## Decisions

- **Shared project per module** (or class) for specs that only need running sidecars: brought up once, each spec spawns its own
  agents under unique names and leaves nothing that another spec sees. Specs about `up`, `down`, stale instances, the lock and
  anything that changes the project keep a fresh one. Expect more than a 2x gain. Parallel runs (`pytest-xdist`) are rejected for
  now.
- **More scenarios, fewer isolated checks.** A spec may follow one story across several commands (spawn, ps, message, stop,
  rm) and assert at each step, where the checks are read-only or are steps of one flow. Where a spec is about one rule, it stays
  small.
- **Command-literal specs.** The command is written as one string, `p.run("agent spawn coder issue-1 -m 'fix it'")`, so a spec
  reads like the shell session it describes. Files follow the CLI tree (`specs/project/up.py`, `specs/agent/spawn.py`, ...)
  rather than "areas".
  - A collection-time check runs every command string against the CLI's own help tree and fails on an unknown command or flag,
    so a stale name fails the suite instead of the docs.
  - A meta-spec walks the real CLI (every command and flag) and fails for any that has no spec: the todo list stays honest.
- **One shared helper module** replaces the per-file helpers; the engine and image are explicit fixtures of the specs that need
  them.
- **Tiers by marker** (`pure`, `sidecars`, `agents`, `real`: internet, git, real harness images). It does not speed up a full run
  but lets a push run the cheap tiers and a release run all, and it fits [the CI matrix](ci-test-matrix.md).

## Pilot: messaging

`test_messaging.py` (48 specs, 246 s) became `specs/message/test_message_cli.py` and `test_agent_api.py` (19 specs, 64 s, nothing
dropped but the repetition): a shared `team` project per module, fresh `pair` agents only for the specs that count, scenarios that
follow a story, a batch of agent calls in one `exec`, command-literal specs (`project.sh`, `project.ok`) checked against the CLI's
help (`specs/clitree.py`, and a spec in `test_meta.py` for every literal). Three things the pilot taught:

- State that looks per-agent is not always. "A person has at most 20 open messages" is the operator's counter for the whole
  project, so a spec that fills it must answer the messages again, or it poisons every later spec of the module.
- A rate limit left on a shared agent shows in every later spec: flooding specs take fresh agents.
- `up` and `spawn` are about a second each, so the remaining cost of a module is its spawns: fresh agents are what to economise.

## What was done

Every engine spec was converted, file by file, to the layout above: `specs/` follows the CLI tree (`project/`, `agent/`, `message/`,
`workspace/`, `service/`, `system/`, `session/`, `proxy/`, `harness/`, `browser/`), each module shares one project and scenarios follow a
story. Deliberately wrong command lines (a removed command, a flag that must not exist) are gone: the two checks in `test_meta.py` cover
them (every command line a spec writes is one egzo has; every command and flag of egzo is run by a spec, which fails until one does).
`Project.pty` runs a command on a terminal (attach, exec) through the same check.

## Left

- The modules that build their own project per spec on purpose: `up`/`down` stories (`project/test_project_up_down.py`, the `down`
  scenario of `agent/test_agent_stale.py`), the secrets that change the policy (`proxy/test_proxy_secrets.py`), git workspaces that change
  what github.com may reach (`workspace/test_git_workspaces.py`). They need a project that starts from nothing.
- `test_secrets.py`, `test_images.py` and the files that need no engine were only moved to command-literal lines.
- The markers for tiers (`pure`, `sidecars`, `agents`, `real`) are not added yet.
- A shared module accumulates networks (every instance is one, and the engine's pool is limited, around thirty): a module whose specs
  spawn many instances removes them after each spec.
- Teardown is still `project down --force` and a sweep once per module; a label-only sweep is the fallback if it shows in a profile.

## Related

[Executable docs](executable-docs.md) is a separate improvement that builds on the command-literal specs.
