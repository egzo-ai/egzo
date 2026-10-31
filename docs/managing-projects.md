# Managing projects

Quick pointers only. See also [the project file](project-file.md), [messaging](messaging.md) for `message send`, `message ls`
and `message reply`, and [troubleshooting](troubleshooting.md). [The hub](hub.md) does the daily loop below (spawn, attach,
stop, remove) from a browser; creating and changing projects stays with the CLI. Commands are grouped: `project`, `agent`, `message`, `workspace` and `system`,
with `ps`, `logs`, `exec`, `start`, `stop`, `restart`, `events` and `proxy` at the top. `egzo --help` lists them and `egzo <command> --help` explains each. Commands find
`egzo.yaml` in the current directory or the nearest parent (`-f` names another file, `-p` the project).

## The model

- `egzo project up` converges the project's **infrastructure** (the proxy, the control sidecar, networks, the CA) to the file
  and **publishes the templates**. It starts no agent, and it never removes or recreates a running instance.
- `egzo agent spawn` makes an **instance** from a template. Spawning needs no edit of `egzo.yaml`.
- An instance is named by you (`issue-412`) or by egzo (`coder-1`). A template can be spawned as often as you like;
  each instance has its own container, network, home volume and, for each git workspace it lists, its own worktree volume.

Every project names a **secret key**, kept in a vault, that seals what egzo keeps at rest ([the project
file](project-file.md#the-secret-key)). Make it once, before the first `up`, and keep it: `openssl rand -hex 32 | pass
insert --multiline egzo/<project>-key`. With another key `up` stops; `egzo project rotate-ca` moves the project to the key
the file names now.

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

## Opening an agent's dev server

An agent that runs a server in its sandbox (`npm run dev`, `python3 -m http.server`) has no address you can reach: it is on an internal
network of its own, and nothing is published. `agent port-forward` brings the port to you:

```console
$ egzo agent port-forward issue-412 3000        # http://127.0.0.1:3000 here reaches port 3000 in the agent
$ egzo agent port-forward issue-412 8000:3000   # on local port 8000
$ egzo agent port-forward issue-412 0:3000      # on a local port the system chooses, which it prints
```

The port shows up on the machine where you run the command: with `DOCKER_HOST=ssh://box` on a laptop, it is on the laptop, and the engine's
host listens on nothing. Each connection is one exec of `egzo agent forward` in the agent's container, which connects to the container's own
loopback, so a server bound to `127.0.0.1` works, WebSockets too, and the sandbox does not change. It takes any port, on `127.0.0.1` unless you
name an address (`0.0.0.0:8000:3000` offers the agent's server to your network).

Ctrl-C ends it, and it ends by itself when the agent stops. A connection to a port where nothing listens closes, and the command says so and
keeps going, so you can start it before the server. When a client has closed its side, the agent's server has a minute of silence to answer
or close before the connection is dropped, and ten minutes in all however much it says: a client that left cannot be told from one that only
ended its input, and a server that keeps talking must not keep the command running in the agent. The agent's image must contain egzo, as for `agent attach`: the harness images do.

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
$ egzo agent rm NAME...          # an instance: container, network, home volume, worktree volumes (committed work stays on its branch)
$ egzo agent rm --stale       # remove instances matching a filter (see --help)
$ egzo project down                # the whole project: sidecars, networks, volumes, CA. Refused while an agent runs
$ egzo project down --force        # removes the running agents too, with their worktrees and homes
$ egzo workspace rm NAME           # down keeps the workspace volumes (your files, the base clones); this deletes one
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
$ egzo workspace ls              # workspaces, their volumes and states
$ egzo workspace fetch           # clone or refresh the git bases
```

Everything that creates or removes things takes `--json` where a script would want it (`agent spawn`, `agent ls`, `agent rm`, `ps`),
and `agent spawn` prints the instance name on stdout.

## Exit codes worth knowing

`agent spawn` with a name that is taken fails with 17 and changes nothing. `message send --wait` and `agent spawn --wait` exit 0 when the
request is done, 3 when the agent declined it, 4 when it failed (for example, the instance was removed) and 5 when
`--timeout` passes (the message stays open).
