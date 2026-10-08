# Managing projects

Quick pointers only. See also [the project file](project-file.md), [messaging](messaging.md) for `send`, `messages`
and `answer`, and [troubleshooting](troubleshooting.md). `egzo --help` lists every command and `egzo <command> --help` explains each. Commands find
`egzo.yaml` in the current directory or the nearest parent (`-f` names another file, `-p` the project).

## The model

- `egzo up` converges the project's **infrastructure** (the proxy, the control sidecar, networks, the CA) to the file
  and **publishes the templates**. It starts no agent, and it never removes or recreates a running instance.
- `egzo spawn` makes an **instance** from a template. Spawning needs no edit of `egzo.yaml`.
- An instance is named by you (`issue-412`) or by egzo (`coder-1`). A template can be spawned as often as you like;
  each instance has its own container, network, home volume and, for `clone`/`worktree` workspaces, its own checkout.

## The daily loop

```console
$ egzo up                                   # once, and after editing egzo.yaml
$ egzo spawn coder issue-412 -m "Fix the flaky checkout test, then open a PR" --attach
$ egzo ps                                   # the sidecars and every instance, with what each is doing
$ egzo attach issue-412                     # the real TUI; Ctrl-] detaches
$ egzo send issue-412 "Also bump the changelog" --wait
$ egzo rm issue-412                         # when the work is pushed
```

`spawn -m` sends the first message (the task); `--wait` blocks until the agent resolves it and prints the answer,
which suits scripts. Messages wait in a queue until the harness is ready.

## Editing the file

```console
$ egzo diff          # what `up` would change; exit 1 if anything
$ egzo up --dry-run
$ egzo up            # applies it
```

If a template changed, running instances of it become **stale**: `ps` marks them and `up` refuses until you retire
them. Remove them with `egzo rm NAME` or `egzo prune --stale`, then spawn again. `up --recreate` rebuilds the
sidecars.

## Tearing down

```console
$ egzo rm NAME...          # an instance: container, network, home volume. Checkouts are kept
$ egzo prune --stale       # remove instances matching a filter (see --help)
$ egzo down                # the whole project: instances, sidecars, networks
$ egzo down --volumes      # also the named volumes
$ egzo down --workspaces   # also the git checkouts, after checking for unpushed work (see workspaces.md)
```

## Looking around

```console
$ egzo ps [--json]         # state, health, activity (idle/working/blocked), open requests
$ egzo logs -f proxy       # a service's engine logs
$ egzo exec coder-1 -- sh  # a command in a container
$ egzo events              # typed event stream
$ egzo messages            # open requests; `egzo questions` lists what agents asked people
$ egzo answer ID "text"    # answer a question an agent asked
$ egzo proxy log           # what the agents tried to reach
$ egzo doctor              # engine, isolation and project checks
```

Everything that creates or removes things takes `--json` where a script would want it (`spawn`, `ps`, `rm`, `prune`),
and `spawn` prints the instance name on stdout.

## Exit codes worth knowing

`spawn` with a name that is taken fails with 17 and changes nothing. `send --wait` and `spawn --wait` exit 0 when the
request is done, 3 when the agent declined it, 4 when it failed (for example, the instance was removed) and 5 when
`--timeout` passes (the message stays open).
