# Troubleshooting

Start with `egzo system check` ([installing](install.md)), then `egzo ps`, then `egzo proxy log`.

## `project up` or `agent spawn` refuses

| message or symptom | what to do |
|---|---|
| a secret is missing | `egzo project check`; with `pass`, check that `pass show NAME` works in your shell; with `env`, export the variable. Secrets are read before anything is created, so nothing is half-made. |
| "stale" instances block `project up` | their template changed. `egzo agent rm --stale` (or `egzo agent rm NAME`), `egzo project up`, spawn again. `egzo project up --dry-run` shows the change. |
| the name is taken (exit 17) | `egzo agent rm NAME`, or pick another. A leftover network or home volume counts as an instance; `agent rm` clears it. |
| a project of that name exists from another directory | rename with `-p`, `EGZO_PROJECT_NAME` or `name:`, or `egzo project down` the other. |
| image cannot be pulled | the error names the image and the `image:` override. `make images`, or set `EGZO_HARNESS_PREFIX` ([installing](install.md), [images](images.md)). |
| `unknown key "runtime"` | egzo does not choose a container runtime. Remove the key; to use gVisor make `runsc` the engine's default runtime ([installing](install.md)). |
| `all predefined address pools have been fully subnetted` | every project and every agent instance is a network of its own, and Docker's default pools hold about 30 networks for the whole engine, other projects included. Remove networks you do not use (`docker network prune`), or give Docker more room: `"default-address-pools": [{"base": "10.10.0.0/16", "size": 24}]` in `/etc/docker/daemon.json`, then restart Docker. |
| `agent spawn` says a worktree volume or a branch exists | an earlier instance of that name left it: `egzo agent spawn --reuse` continues with it, or remove the volume with `egzo workspace rm NAME --worktree INSTANCE`; the branch `egzo/NAME` stays in the base clone until you delete it there. |
| "another egzo command is changing this project" | one command at a time changes a project (a lease in the engine). Wait; if that command was killed, `egzo project unlock` removes it, or it expires after 30 minutes. |
| my files are not on my machine | workspaces are engine volumes: `egzo workspace get NAME PATH DEST` copies them out, `egzo workspace put NAME SRC` copies files in ([workspaces](workspaces.md)). |
| a git workspace has no base clone | `egzo project up` could not clone it (it printed why): allow the host and give the profile the `github` service, then `egzo project up` again. |

## The agent cannot reach something

```console
$ egzo proxy log            # look for "decision":"deny" and its reason
$ egzo proxy rules          # what this agent may reach
```

See [sandboxing agents](sandboxing.md) for how to change a profile.

Common reasons: the host is not in `allow` or in a service; the port is not 443 (only HTTPS is allowed); the address is
private; the agent named a profile that does not exist. Claude Code also needs `platform.claude.com`. If the proxy
log is empty and the agent still fails, the tool may not honour `HTTPS_PROXY`.

A `503` from the proxy means it lost its policy after a restart: `egzo restart proxy` or `egzo project up` loads it again.

A TLS error on an injected host usually means the tool pins certificates or ignores the CA bundle egzo provides
(`SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`, and similar). `egzo project rotate-ca` issues a new CA and restarts the agents.

## A browser page does not load

```console
$ egzo proxy log                     # the host, and the decision with its reason (`--agent NAME` for one agent)
```

- `deny` with the code `not-in-profile`: the page pulls from a host the profile does not list. Add it, or
  use a wildcard (`allow: ["*.cdn.example.com"]`); [browsing](browser.md) says how much to open.
- the mode `intercept` followed by a `handshake` error with the code `tls-handshake`: the browser does not trust the project CA. The stock images
  import it into Chromium's store; an image of your own needs `certutil` (the `libnss3-tools` package) and the `egzo
  agent run` entrypoint. Chromium ignores `SSL_CERT_FILE`.
- A site that loads for curl but not for the browser, with the mode `intercept` in the log: it pins its certificate.
- `challenge` decisions are not failures: a browser asks for the proxy credentials before it sends them. They name no agent,
  so `--agent` does not show them.
- `Page.goto: net::ERR_TUNNEL_CONNECTION_FAILED`: the proxy refused the host (see its `deny`).

## An agent is not doing what you sent

(How messages work: [messaging](messaging.md).)

```console
$ egzo ps                   # ACTIVITY: is it working, idle or blocked?
$ egzo message ls             # is the message queued, announced, fetched or unconfirmed?
$ egzo agent attach NAME          # look at the real terminal
```

- `blocked`: the harness is waiting on its own UI, often a login. Attach and answer it.
- `queued` for a long time: the agent is not idle, or someone typed recently. Wait, or `send --interrupt`.
- `unconfirmed`: the line was typed three times and never fetched. The agent may have lost track; it can still find
  the message with `list_messages`.

## Logs and shells

```console
$ egzo logs -f proxy        # or control, or an instance name
$ egzo exec NAME -- sh
$ egzo events               # the typed event stream
```

The control sidecar writes JSON lines. The agents' status goes to standard output, one `{"type":"status",...}` line when
something changes and one every 30 seconds; anything control has to complain about goes to standard error as
`{"time","level","msg",...}`. To see only the problems: `docker logs PROJECT-control-1 >/dev/null` (or `podman logs`). The logs use the
engine's default logging: if `egzo logs` prints nothing for a container that should have said something, run
`egzo system check` and read its `logs` line.

## Known problems

- **Rootful Podman, intermittent loss of outbound connectivity.** The proxy's first outbound connection sometimes
  times out; a project that comes up broken stays broken. It reproduces without egzo, appears host-level and is open
  (`known-issues/rootful-podman-intermittent-egress.md`). Rootful Podman is not supported for now: use Docker or rootless Podman.
- **`pi` has no integration**, because it has no MCP support (`known-issues/mcp-support-is-required.md`).
- **Platforms other than the developer's machine have not been run** by the specs
  (`known-issues/reference-platform-ci-matrix.md`).

Please report anything else with the output of `egzo system check` and `egzo version`.
