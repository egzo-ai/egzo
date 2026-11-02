# Managing projects

Quick pointers only. See also [the project file](project-file.md), [messaging](messaging.md) for `message send`, `message ls`
and `message reply`, and [troubleshooting](troubleshooting.md). [The hub](hub.md) does the daily loop below (spawn, attach,
stop, remove) from a browser; creating and changing projects stays with the CLI. Commands are grouped: `project`, `agent`, `message`, `workspace` and `system`,
with `ps`, `logs`, `exec`, `start`, `stop`, `restart`, `events` and `proxy` at the top. `egzo --help` lists them and `egzo <command> --help` explains each. Commands find
`egzo.yaml` in the current directory or the nearest parent (`-f` names another file, `-p` the project).

## The model

- `egzo project up` converges the project's **infrastructure** (the proxy, the control sidecar, the link of a project with a hub, networks, the CA) to the file
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

An agent that runs a server in its sandbox (`npm run dev`, a database) has no address you can reach: it is on an internal network of its own.
You decide which ports it may serve, and it opens them when someone needs them:

```yaml
local_ports: { bind: 192.168.8.242, host: mybox.lan }
agents:
  coder:
    harness: claude-code
    ports: { web: "5173/http", db: "5432/tcp" }
```

The agent runs its servers on `0.0.0.0` on those ports and calls `expose(web, local)`, which answers `http://mybox.lan:20481/`. That is the one
address where the app can be reached, from any machine that can reach `192.168.8.242`. A `tcp` or `udp` port answers `mybox.lan:20482`. A
project with a [hub](hub.md) can open an `http` port for it instead: `expose(web, hub)` answers the preview's address. A port is open for one of
the two at a time.

A local port goes through a small relay, a container of its own that lives as long as the instance: its port stays the same through `agent stop`
and `agent start` and a reboot, and `agent rm` frees it. Between `stop` and `start` a connection just fails. The agent's own network stays internal.
The relay turns away whatever comes from the agent, so an agent reaches nothing through it. Closing a port (`unexpose`) stops new connections; ones that are open last until they close.

`egzo ps` lists what is open under `PORTS` (`web:hub,db:local`), and `egzo ps --json` has the address of each. See [messaging](messaging.md#ports-an-agent-opens)
for what an agent is told, and [security](security.md) for what a local port gives away.

## Linking to a hub

A project connects itself to a hub: name it in the file ([`hub:`](project-file.md#the-hub)), run `up`, then register it once with a link code. A
**link** sidecar dials out to the hub on one HTTPS connection; nothing on your machine listens, so a laptop or a box behind NAT works.

```console
$ egzo project up
...
link: not registered: run `egzo project register`
$ egzo project register
Link code:
link: linked to https://my-egzo.example.org as p1a2b3c4d5e6, owned by alice
```

An operator of the hub, or anybody with a project quota, makes the code, under Settings, Links, and it is good once, for 15 minutes. `register` asks for it without showing it, or reads
it from stdin when that is not a terminal (`egzo project register < code.txt`). There is no flag for it, and it goes to the link through
`engine exec`, so it is never in your shell's history, in `ps` or in the project file. Keep it out of the file: a file can be committed.

`up` never waits for this and never asks for anything: the project runs without the hub until it is registered, and after that the link's key is all
it takes. `egzo ps` and `egzo project check` say where it stands:

| State | Means | `project check` |
|---|---|---|
| not registered | the hub does not know this project: `egzo project register` brings a code. The link does not dial again until you do that or run `up` | warns |
| approved | linked: `linked to ... as ..., owned by ...` | ok |
| revoked | the hub turned it away; the link does nothing the hub asks and does not dial again | fails |
| unreachable | the last try failed, and the link keeps trying, from every second up to every minute | warns |
| locked | the sidecar restarted and has not got the secret key: `egzo project up` brings it | warns |

A code that is wrong, expired or used gets one answer, which does not say which. After a project is revoked, the hub can ask it nothing and the link
pushes nothing, and a code does not help: an operator has to **Forget** it first, and `egzo project register` then makes it a new project on the hub,
with no roles. Once registered, the hub can do what its
verbs say and no more: spawn from the project's own templates, start, stop and remove instances, attach to their sessions, reach the ports agents opened
for it, and read logs, events and the published templates. It cannot run a container, mount a path or run a command of its choosing.

- **Behind a company proxy**, set `HTTPS_PROXY` when you run `up`: it is handed to the link, credentials included, and never shows in `inspect`.
  A proxy that inspects TLS must let WebSockets through (Squid: `http_upgrade_request_protocols websocket allow all`) and the hub's `ca` must name
  its CA. A link that reports `HTTP 426` is being stopped by such a proxy, or by a reverse proxy in front of the hub without the `Upgrade` and
  `Connection` headers.
- **Unlinking:** remove `hub:` and run `up`, or run `project down`. Both tell the hub, which forgets the project, and remove the link's key. A
  project linked again needs a new code, and is a new project on the hub. A project that was revoked stays on the hub as revoked until an operator
  forgets it: `down` says so.
- **The link's key** is an Ed25519 key on its volume, sealed with the [secret key](project-file.md#the-secret-key). It is what the hub knows the
  project by. `egzo project rotate-ca` moves it to the new secret key while the link runs and holds it; a link that restarted is locked and cannot
  follow, so it warns, and a change of secret key without the old one means registering again, with a new code. So does losing the link's volume.

## After a restart

After a restart of the machine or the engine, run `egzo project up`. A project does not resume by itself: the proxy holds your credentials and the link
its key in memory only, and egzo reads secrets from your vault when you run a command. `up` starts the sidecars, reloads the policy and opens the link's
key. A linked project is back on its hub as it was, with the same key and owner: nobody registers it again. Until then the link waits `locked`, which
`egzo ps` shows, and the hub shows the project offline. Agents stay stopped until you, or the hub, start them.

How `up` gets run is yours to arrange, by hand or from your own boot script.

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
