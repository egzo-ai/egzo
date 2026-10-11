# Managing projects

Quick pointers only. See also [the project file](project-file.md), [messaging](messaging.md) for `message send`, `message ls`
and `message reply`, and [troubleshooting](troubleshooting.md). Commands are grouped: `project`, `agent`, `message`, `workspace` and `system`,
with `ps`, `logs`, `exec`, `start`, `stop`, `restart`, `events` and `proxy` at the top. `egzo --help` lists them and `egzo <command> --help` explains each. Commands find
`egzo.yaml` in the current directory or the nearest parent (`-f` names another file, `-p` the project).

## The model

- `egzo project up` converges the project's **infrastructure** (the proxy, the control sidecar, networks, the CA) to the file
  and **publishes the templates**. It starts no agent, and it never removes or recreates a running instance.
- `egzo agent spawn` makes an **instance** from a template. Spawning needs no edit of `egzo.yaml`.
- An instance is named by you (`issue-412`) or by egzo (`coder-1`). A template can be spawned as often as you like;
  each instance has its own container, network, home volume and, for each git workspace it lists, its own worktree.

## The daily loop

```console
$ egzo project up                                   # once, and after editing egzo.yaml
$ egzo agent spawn coder issue-412 -m "Fix the flaky checkout test, then open a PR" --attach
$ egzo ps                                   # the sidecars and every instance, with what each is doing
$ egzo agent attach issue-412                     # the real TUI; Ctrl-] detaches
$ egzo message send issue-412 "Also bump the changelog" --wait
$ egzo agent rm issue-412                         # when the work is pushed
```

`agent spawn -m` sends the first message (the task); `--wait` blocks until the agent resolves it and prints the answer,
which suits scripts. Messages wait in a queue until the harness is ready.

## Editing the file

```console
$ egzo project check                 # is the file valid, can every secret be read
$ egzo project up --dry-run          # what `up` would change; exit 1 if anything
$ egzo project up                    # applies it
```

If a template changed, running instances of it become **stale**: `ps` marks them and `up` refuses until you retire
them. Remove them with `egzo agent rm NAME` or `egzo agent rm --stale`, then spawn again. `up --recreate` rebuilds the
sidecars.

## Tearing down

```console
$ egzo agent rm NAME...          # an instance: container, network, home volume, worktrees (committed work stays on its branch)
$ egzo agent rm --stale       # remove instances matching a filter (see --help)
$ egzo project down                # the whole project: sidecars, networks, volumes, CA. Refused while an agent runs
$ egzo project down --force        # removes the running agents too, with their worktrees and homes
```

## Looking around

```console
$ egzo ps [--json]         # state, health, activity (idle/working/blocked), open requests
$ egzo logs -f proxy       # a service's engine logs
$ egzo exec coder-1 -- sh  # a command in a container
$ egzo events              # typed event stream
$ egzo message ls            # open requests; `egzo message ls --kind question` lists what agents asked people
$ egzo message reply ID "text"    # answer a question an agent asked
$ egzo proxy log           # what the agents tried to reach
$ egzo system check              # engine, isolation and git (no project needed)
$ egzo workspace ls              # workspaces, their paths and states
$ egzo workspace fetch           # clone or refresh the git bases
```

Everything that creates or removes things takes `--json` where a script would want it (`agent spawn`, `agent ls`, `agent rm`, `ps`),
and `agent spawn` prints the instance name on stdout.

## Exit codes worth knowing

`agent spawn` with a name that is taken fails with 17 and changes nothing. `message send --wait` and `agent spawn --wait` exit 0 when the
request is done, 3 when the agent declined it, 4 when it failed (for example, the instance was removed) and 5 when
`--timeout` passes (the message stays open).
