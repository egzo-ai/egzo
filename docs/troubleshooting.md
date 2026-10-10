# Troubleshooting

Start with `egzo doctor` ([installing](install.md)), then `egzo ps`, then `egzo proxy log`.

## `up` or `spawn` refuses

| message or symptom | what to do |
|---|---|
| a secret is missing | `egzo secrets ls`; with `pass`, check that `pass show NAME` works in your shell; with `env`, export the variable. Secrets are read before anything is created, so nothing is half-made. |
| "stale" instances block `up` | their template changed. `egzo prune --stale` (or `egzo rm NAME`), `egzo up`, spawn again. `egzo diff` shows the change. |
| the name is taken (exit 17) | `egzo rm NAME`, or pick another. A leftover network or home volume counts as an instance; `rm` clears it. |
| a project of that name exists from another directory | rename with `-p`, `EGZO_PROJECT_NAME` or `name:`, or `egzo down` the other. |
| image cannot be pulled | the error names the image and the `image:` override. `make images`, or set `EGZO_HARNESS_PREFIX` ([installing](install.md), [images](images.md)). |
| `runtime` refused on Podman | by design: Podman cannot apply or verify a runtime. Use Docker for gVisor. |
| a workspace checkout exists in another mode | remove the directory; modes are not converted. |

## The agent cannot reach something

```console
$ egzo proxy log            # look for "deny" and its reason
$ egzo proxy rules          # what this agent may reach
```

See [sandboxing agents](sandboxing.md) for how to change a profile.

Common reasons: the host is not in `allow` or in a service; the port is not 443 (only HTTPS is allowed); the address is
private; the agent named a profile that does not exist. Claude Code also needs `platform.claude.com`. If the proxy
log is empty and the agent still fails, the tool may not honour `HTTPS_PROXY`.

A `503` from the proxy means it lost its policy after a restart: `egzo restart proxy` or `egzo up` loads it again.

A TLS error on an injected host usually means the tool pins certificates or ignores the CA bundle egzo provides
(`SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`, and similar). `egzo ca rotate` issues a new CA and restarts the agents.

## An agent is not doing what you sent

(How messages work: [messaging](messaging.md).)

```console
$ egzo ps                   # ACTIVITY: is it working, idle or blocked?
$ egzo messages             # is the message queued, announced, fetched or unconfirmed?
$ egzo attach NAME          # look at the real terminal
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

## Known problems

- **Rootful Podman, intermittent loss of outbound connectivity.** The proxy's first outbound connection sometimes
  times out; a project that comes up broken stays broken. It reproduces without egzo, appears host-level and is open
  (`known-issues/rootful-podman-intermittent-egress.md`). Recreate the project, or use Docker or rootless Podman.
- **Podman cannot run agents under gVisor** (`known-issues/podman-gvisor-unsupported.md`).
- **`pi` has no integration**, because it has no MCP support (`known-issues/mcp-support-is-required.md`).
- **Platforms other than the developer's machine have not been run** by the specs
  (`known-issues/reference-platform-ci-matrix.md`).

Please report anything else with the output of `egzo doctor` and `egzo version`.
