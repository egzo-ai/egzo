# Roadmap: overhaul the specs suite

**Status.** Started: the messaging specs are the pilot (see "Pilot"). The rest is converted file by file. Specs stay pytest (a transcript format such as testscript or cram was considered and
rejected: it would throw away the status model in `spec_status.py` and the `pexpect` session specs).

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

## Not decided

- Teardown. Today every spec runs `egzo project down --force` and then a per-resource sweep (about 0.6 s). With shared projects
  this mostly disappears; a label-only sweep at the end of the session is the fallback.
- Whether the old files are converted one at a time (heaviest first: `test_instances`, `test_proxy`, `test_messaging`,
  `test_spawn`) or the new layout lands in one go. One at a time keeps the suite running throughout.

## Related

[Executable docs](executable-docs.md) is a separate improvement that builds on the command-literal specs.
