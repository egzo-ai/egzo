# Sandboxing agents: egress policy

Agents have no route to the internet. Their only way out is the egzo **proxy**, and the proxy applies the **egress
profile** the agent's template names. Anything not allowed is denied. This page is how to write profiles; [the security model](security.md) says what the sandbox does not cover.

What the proxy does for an agent, in order: it checks the proxy credential the agent was given at spawn (an agent
cannot use another's profile), allows only port 443, checks the host against the profile, refuses private and
internal addresses, and then either tunnels the connection or, for hosts with a credential, terminates TLS with the
project's own CA, adds the credential and forwards. A tunnel is checked too: the TLS server name must be the approved
host (an allowed name cannot front for another one), and a client that sends Encrypted Client Hello, which would hide the
name, is not tunneled but terminated like a credential host (this is what a browser does, see [browsing](browser.md)).
Every decision is logged.

## A profile

```yaml
egress:
  default:                                  # what an agent gets if it names no profile
    allow: [platform.claude.com, "*.pypi.org", files.pythonhosted.org]
    services:
      anthropic: main/ANTHROPIC_API_KEY     # built-in service + its secret
      github: main/GITHUB_TOKEN
```

- `allow`: host names or `*.domain` globs the agent may reach with **no** credential.
- `services`: hosts that get a secret applied. A built-in service (`anthropic`, `github`, `anthropic-oauth`, `opencode`)
  needs only the secret: `vault/SECRET` (for a secret named `company/project/token` in a `pass` vault `main`:
  `main/company/project/token`).
- The agent's reach is `allow` plus the hosts of its services. Without a `default` profile, an agent that names none
  can reach nothing.

[Troubleshooting](troubleshooting.md) covers a request that is denied. `egzo project up` warns if an agent's profile cannot reach its harness's provider host.

## Built-in services

| service | hosts | injected |
|---|---|---|
| `anthropic` | `api.anthropic.com` | `x-api-key: <secret>` |
| `anthropic-oauth` | `api.anthropic.com` | `Authorization: Bearer <token>` (a Claude subscription token from `claude setup-token`) |
| `opencode` | `opencode.ai` | `Authorization: Bearer <key>` (the API key of an OpenCode Go or Console workspace: a service account's) |
| `github` | `github.com`, `api.github.com` | `Authorization: Basic base64(x-access-token:<secret>)` (git over HTTPS refuses a bearer token) |

Binding a token to the wrong one of the two Anthropic services is refused at `egzo project up`. Claude Code also needs
`platform.claude.com` in `allow`; it needs no credential.

## Your own services

An object defines a service: hosts, and how to inject the secret.

```yaml
egress:
  default:
    services:
      deploy-api:
        hosts: [api.deploy.example.com]
        inject: { header: Authorization, value: "Bearer {secret}" }   # without value, the raw secret is the header
        secret: main/DEPLOY_TOKEN
```

`inject` has three forms: the raw secret (no `value`), a `value` template around `{secret}`, or HTTP Basic authentication
with `basic_user`, which sends `Basic base64(<user>:<secret>)`; use it for what builds the header from the secret itself,
such as git over HTTPS. `basic_user` and `value` cannot be combined. The built-in `github` service is only this:

```yaml
      git-host:
        hosts: [git.example.com]
        inject: { header: Authorization, basic_user: x-access-token }
        secret: main/GIT_TOKEN
```

A service with neither `inject` nor a `placeholder` (see below) is a plain allowlist entry and cannot have a secret.

Without more, `inject` sets the header on every request to the service's hosts, including a website or a console on the
same name. When the API lives under a path, say so with `paths`, and the header goes only with the requests under one of
them:

```yaml
      deploy-api:
        hosts: [deploy.example.com]               # also the vendor's website
        paths: [/api/v1/]                         # the API: the token goes nowhere else
        inject: { header: Authorization, value: "Bearer {secret}" }
        secret: main/DEPLOY_TOKEN
```

- A prefix matches whole segments: `/api` covers `/api` and `/api/x`, not `/apix`; `/api/` covers `/api/x`. Other paths
  of the host stay reachable, without the header (a header the agent sends there itself is left alone).
- A prefix is a plain absolute path: no `.` or `..` segment, `%`, `?`, `#` or `\`.
- The proxy refuses (400, audit code `path`) a request under a prefix whose path has a `.` or `..` segment (decoded, also
  before a `;`) or a backslash: the server could read it as a path outside the prefix.
- `paths` limits an injected header only. A placeholder goes where the agent types it.
- A path rule is not an authorization rule: an endpoint under the prefix can still do more than the agent needs. Give the
  credential the least privilege it can work with. There is no rule by method.

The built-in services inject on every path of their hosts. `github.com` is also GitHub's website, and git over HTTPS sends
its requests under `/<owner>/<repo>.git/`, so a prefix there would be a list of repositories: see [the known
issue](../known-issues/a-secret-is-injected-in-every-request-to-its-hosts.md).

Service `hosts` take the patterns `allow` takes (`host`, `*.domain`, `*`), **but only for a service without a secret**: a
secret binds to exact hosts, because a wildcard would hand it to every subdomain, including ones somebody else controls.
`inject` or `placeholder` with a wildcard host is an error at `egzo project check --resolved` and `egzo project up`. A wildcard service is an allowlist
entry too: it lets the agent reach every host it covers. `*.example.com` covers the names below `example.com`, not
`example.com` itself. A service has no setting that weakens TLS: unknown keys are errors.

`inspect: true` logs the method, path and status of every request to the service's hosts (never the query string or a
body); `egzo proxy log` shows them. It is off by default because it makes the proxy terminate TLS for the host, which
a client that pins its certificate cannot accept. A host with `inject` or a `placeholder` is already logged this way.

```yaml
      docs-site: { hosts: [docs.example.com], inspect: true }
      every-page: { hosts: ["*"], inspect: true }     # a wildcard is fine without a secret: audit everything
```

An exact service that injects still injects where a wildcard service also covers the host; the wildcard only adds the
`inspect` audit.

## Secrets the agent has to type: placeholders

`inject` suits a header the agent never touches. Some secrets the agent has to put somewhere itself, such as the
password of a login form in a browser. Give the service a `placeholder` instead of an `inject`:

```yaml
vaults:
  main:
    backend: pass
    secrets: [sites/example/password]

egress:
  default:
    services:
      example-login:
        hosts: [app.example.com]
        secret: main/sites/example/password
        placeholder: EXAMPLE_PASSWORD
```

Agents with this profile get `EXAMPLE_PASSWORD=egzo-ph-<random>` in their environment. They use it as the password
(`curl -d "password=$EXAMPLE_PASSWORD" https://app.example.com/login`, or typed into a form), and the proxy replaces it
with the real password on its way to `app.example.com`: in the URL, in headers and in the body, encoded the way the
place needs (form, JSON). Sent anywhere else, the placeholder stays a meaningless string. It never changes, even if you
rotate the secret.

The proxy has to read a request to find the placeholder, so a request to a placeholder host whose body is compressed
or larger than 1 MiB is refused with a message saying so (`413` or `415`), always, not sometimes. Use `inject` for a
host that takes uploads, or send the upload to a host that has no placeholder.

Some clients cannot be served this way: see [known issues](../known-issues/secret-injection-limits.md). HTTP Basic
authentication, for example, is built by the client from the placeholder, which hides it; use `inject` for that.

## Several profiles

```yaml
egress:
  default:
    allow: [platform.claude.com]
    services:
      anthropic: main/ANTHROPIC_API_KEY
  operator:
    extend: default                 # one parent; allow lists are unioned
    services:
      github: main/GITHUB_TOKEN     # a string only sets the secret of an inherited or built-in service
      deploy-api: { hosts: [api.deploy.example.com], secret: main/DEPLOY_TOKEN,
                    inject: { header: X-Api-Key } }

agents:
  coder:    { harness: claude-code, egress: default }
  releaser: { harness: claude-code, egress: operator }
```

A child can add to what it inherits and cannot remove anything. Cycles and unknown parents are errors, as is a
service that uses a secret that no vault declares. Two services in one profile cannot share a host with different
injection.

## ssh to servers

For sysadmin work (diagnostics, logs, restarting a service) an agent can run `ssh` to the servers its profile names. The private key
stays in the proxy, like any other secret:

```yaml
vaults:
  main: { backend: pass, secrets: [servers/prod-ssh-key] }

egress:
  ops:
    ssh:
      prod:                                  # what the agent types: ssh prod
        host: 10.0.4.12                      # one exact name or address (private ones are fine)
        port: 22                             # optional
        user: egzo-ops                       # fixed here: the agent cannot choose another account
        key: main/servers/prod-ssh-key       # an unencrypted private key; a pass entry is read whole
        host_key: SHA256:Fp3x...             # required: ssh-keygen -lf, or a public key line (ssh-ed25519 AAAA...)
agents:
  admin: { harness: claude-code, egress: ops }
```

Inside the agent, `ssh prod 'journalctl -u nginx -n 100'` and `ssh -t prod` work, with stdin, stderr, exit status and a terminal.
What happens: the agent signs in to the proxy with an identity egzo makes for the instance, the proxy opens its own connection to
`host` with the key and **refuses a server whose host key is not the pinned one**, and relays the session. So:

- **The agent never holds the key**, in its environment, files, the engine's records or the audit log. It does not need a default route either.
- **Only the servers of its profile.** An agent of another profile gets "Permission denied" and the server is never contacted.
- **The host key is required.** There is no trust on first use; a mismatch is a failure the audit records with the key the server presented. With a
  fingerprint, a server with several host key types works; with a public key line the type is fixed.
- **Sessions only, and sftp.** Port forwarding (`-L`, `-R`, `-W`), agent forwarding and X11 are refused, and so is every subsystem but `sftp`: `scp`,
  `sftp` and `rsync -e ssh` work.
- **It does not limit what the agent runs on the server.** Use a dedicated low-privilege account, or a forced command in `authorized_keys`,
  on the server. The audit records every session and every command (`ssh-exec`), and not what is typed in a shell.
- The proxy never connects to loopback or a cloud metadata address, and gives up on a server that does not answer, or goes quiet, after the
  handshake timeout, keepalive failures or an hour without data. A proxy restart ends live ssh sessions.
- A key with a passphrase is refused by `egzo project up`; FIDO (`sk-`) keys are not supported.

The harness images carry the ssh client. An image of your own needs `openssh-client`; run `egzo agent ssh-setup` at its start if its
entrypoint is not `egzo agent run`. See [images](images.md).

## Full internet

```yaml
egress:
  open:
    allow: ["*"]
```

Still through the proxy, so still audited, still limited to port 443 and public addresses, and credentials are
injected only for the hosts of declared services. Use it for a trusted template, not by default.

## What the sandbox does not allow

- **Raw TCP, UDP, ping, DNS to other servers; ssh anywhere else.** Only HTTPS through the proxy, and ssh to the servers a profile names
  ([above](#ssh-to-servers)). A git remote of a workspace must be HTTPS: the base clone (`egzo project up`) and the fetch before each worktree (`egzo agent spawn`) run in short-lived prep containers on a network the proxy serves, so they need the git host in `allow` and, for a private repository, the `github` service.
- **Pinned-certificate clients.** A host with an injected credential is intercepted with the project CA; a client that
  pins certificates will fail on it.
- **Agent-to-agent traffic.** Agents never share a network. They talk only through the control sidecar (`egzo message send`,
  the `message` tool).

## See what happened

```console
$ egzo proxy rules       # what each agent may reach, and which credentials go where
$ egzo proxy log         # the audit trail, one JSON event per line
$ egzo project check --resolved            # every profile fully resolved
```

Each event is one step of one connection:

```json
{"time":"…","conn":"5f0c3a9e1b2d4c6f","protocol":"https","event":"connect","decision":"deny","agent":"coder-1",
 "instance":"01JBX…","profile":"default","host":"registry.npmjs.org",
 "reason":{"code":"not-in-profile","text":"not allowed by the agent's egress profile"}}
```

- `conn` is the connection: its `connect`, its requests and its `close` share it.
- `agent` is the instance's name, `instance` its id (never reused when a name is), `profile` the profile that decided. They
  are set only once the proxy has checked the credentials (for ssh, the key's signature): `agent` always means proved.
- `claimed_agent` is the name the client sent in its proxy credentials, right or wrong: on wrong credentials it is the only
  name (https only; an ssh client sends none).
- `client` is the address the connection came from, and `user_agent` what the client says it is (the `User-Agent` of the
  CONNECT, or an ssh client's version, such as `SSH-2.0-OpenSSH_9.6p1`). Both are what the proxy saw, never proof of who.
- `decision` is `allow`, `deny`, `error`, or `challenge`: a connection that arrived without proxy credentials. A client
  may start that way and send them once asked, so a challenge is not a refusal, and it names no agent; its `client` says
  where it came from.
- `reason.code` is from a fixed list, for tools (the full list is in [the roadmap](../roadmap/proxy-audit.md)); `reason.text`
  is for people.

| protocol | event | meaning |
|---|---|---|
| `https` | `connect` | the decision about a CONNECT. Allowed, its `mode` says how: `tunnel` (the proxy sees the host name, never the traffic), `inject` (a service injects a credential or swaps a placeholder), `inspect` (`inspect: true`), or `intercept` (the client sent Encrypted Client Hello, [browsing](browser.md)). Denied: not in the profile, not port 443, a private address, a TLS server name that differs from the host, wrong proxy credentials, too many connections. An error: the host did not answer, or the client left |
| `https` | `handshake` | the client's TLS handshake with the proxy failed on a terminated host: it does not trust the project CA |
| `https` | `request` | one request to a terminated host: method, path (never the query) and status; or a request refused (another host, a placeholder that cannot be swapped, a path that could climb out of a service's `paths`) or failed upstream |
| both | `close` | the end of an allowed connection, with `bytes_up`, `bytes_down` and `duration_ms` |
| `ssh` | `connect` | an agent's ssh connection to a server of its profile (`server` is its name, `host` the real one), or a refused one (an identity or server that is not the agent's, too many connections; `server` is then the one asked for), or an error when the client left before it signed in |
| `ssh` | `upstream` | the server did not answer, refused the key, presented another host key than the pinned one, or would not open a session |
| `ssh` | `exec`, `shell`, `sftp` | a command (`command`), a shell or an sftp session (`scp`, `sftp`) the agent started on the server |
| `ssh` | `channel`, `request` | refused: port forwarding, X11, agent forwarding, a subsystem other than sftp |

Changing a profile and running `egzo project up` reloads the proxy with the new policy; running instances keep their
binding by profile name. After a proxy restart, `egzo restart proxy` (or `egzo project up`) loads the policy again; until
then the proxy answers 503. `egzo project rotate-ca` issues a new project CA and restarts the agents.
