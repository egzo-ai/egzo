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

Nothing reaches an agent from outside either, except the ports its template [declares](project-file.md#ports), and only once the agent opens one
for the [hub](hub.md) or, in a project with [`local_ports`](project-file.md#local-ports), for the machine. The agent's network stays internal.

## A profile

```yaml
egress:
  default:                                  # what an agent gets if it names no profile
    allow: [platform.claude.com, "*.pypi.org", files.pythonhosted.org]
    services:
      anthropic: env/ANTHROPIC_API_KEY      # built-in service + its secret
      github: env/GITHUB_TOKEN
```

- `allow`: host names or `*.domain` globs the agent may reach with **no** credential.
- `services`: hosts that get a credential applied. A built-in service (`anthropic`, `github`, `anthropic-oauth`, `opencode`,
  `openai`) needs only the secret: `vault/SECRET` (for a secret named `company/project/token` in a `pass` vault `main`:
  `main/company/project/token`). That string is short for `{ credential: main/company/project/token }`.
- The agent's reach is `allow` plus the hosts of its services. Without a `default` profile, an agent that names none
  can reach nothing.

[Troubleshooting](troubleshooting.md) covers a request that is denied. `egzo project up` warns if an agent's profile cannot reach its harness's provider host.

## Built-in services

| service | hosts | injected |
|---|---|---|
| `anthropic` | `api.anthropic.com` | `x-api-key: <secret>` |
| `anthropic-oauth` | `api.anthropic.com` | `Authorization: Bearer <token>` (a Claude subscription token from `claude setup-token`) |
| `claude` | `api.anthropic.com`, `mcp-proxy.anthropic.com` (claude.ai connectors) | a Claude subscription signed in from Claude Code: the stand-in bearer token becomes the real one (an [OAuth service](#oauth-services)) |
| `opencode` | `opencode.ai` | `Authorization: Bearer <key>` (the API key of an OpenCode Go or Console workspace: a service account's) |
| `chatgpt` | `chatgpt.com`, under `/backend-api/` | a ChatGPT sign-in: the stand-in bearer token becomes the real one (an [OAuth service](#oauth-services)) |
| `github` | `github.com`, `api.github.com` | `Authorization: Basic base64(x-access-token:<secret>)` (git over HTTPS refuses a bearer token) |

Binding a token to the wrong one of the two Anthropic token services is refused at `egzo project up`, and so is a profile with
two of `anthropic`, `anthropic-oauth` and `claude`: [Claude Code](harnesses.md#claude-code) compares the three. With
`anthropic` or `anthropic-oauth`, Claude Code also needs `platform.claude.com` in `allow`; it needs no credential.

## Your own services

An object with `hosts` defines a service: its hosts, how the credential goes into a request, and the `credential`.
An object without `hosts` binds a credential to a built-in or inherited service and names nothing else.

```yaml
egress:
  default:
    services:
      deploy-api:
        hosts: [api.deploy.example.com]
        inject: { header: Authorization, value: "Bearer {secret}" }   # without value, the raw secret is the header
        credential: env/DEPLOY_TOKEN
```

A credential has a `method`. `secret` reads a secret from a vault at `egzo project up`: `credential: env/DEPLOY_TOKEN`
is short for `credential: { method: secret, secret: env/DEPLOY_TOKEN }`. `oauth` is the tokens of a sign-in, which the
proxy captures and renews (see [OAuth services](#oauth-services)).

`inject` has three forms: the raw secret (no `value`), a `value` template around `{secret}`, or HTTP Basic authentication
with `basic_user`, which sends `Basic base64(<user>:<secret>)`; use it for what builds the header from the secret itself,
such as git over HTTPS. `basic_user` and `value` cannot be combined. The built-in `github` service is only this:

```yaml
      git-host:
        hosts: [git.example.com]
        inject: { header: Authorization, basic_user: x-access-token }
        credential: env/GIT_TOKEN
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
        credential: env/DEPLOY_TOKEN
```

- A prefix matches whole segments: `/api` covers `/api` and `/api/x`, not `/apix`; `/api/` covers `/api/x`. Other paths
  of the host stay reachable, without the header (a header the agent sends there itself is left alone).
- A prefix is a plain absolute path: no `.` or `..` segment, `%`, `?`, `#` or `\`.
- Under a prefix, the proxy refuses (400, audit code `path`) a path the server could read as one outside it, which no API
  call needs: a segment of dots (`.`, `..`, decoded, also before a `;` or with trailing spaces), a backslash, an escape of a
  character that needs none (`%2E`, `%61`), an escape escaped (`%252e`), an escaped control character or byte outside
  ASCII. `%2F` and `%20` pass: APIs use them in names (`group%2Fproject`, a file name with a space).
- The audit says of every request whether the credential went with it (`injected`), and why not (`outside-paths`), below.
- `paths` limits an injected header only. A placeholder goes where the agent types it.
- One service decides where a host's credential goes: two services that put the same credential on one host with
  different `paths` are refused. List every prefix in one service.
- A path rule is not an authorization rule: an endpoint under the prefix can still do more than the agent needs. Give the
  credential the least privilege it can work with. There is no rule by method.

The built-in `opencode` service injects only on the paths OpenCode sends its key to ([harnesses.md](harnesses.md#opencode)).
`github` injects on every path of its hosts: `github.com` is also GitHub's website, and git over HTTPS sends
its requests under `/<owner>/<repo>.git/`, so a prefix there would be a list of repositories: see [below](#a-secret-goes-to-every-path-of-its-hosts-unless-the-service-has-paths).

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

## OAuth services

Some credentials are not a secret you keep but a sign-in whose tokens rotate: a ChatGPT Plus or Pro account for Codex, or
a Claude subscription for Claude Code. An OAuth service has no vault secret. The proxy captures the tokens when a client signs in through it, keeps them,
and gives the client stand-ins:

```yaml
egress:
  codex:
    services:
      chatgpt: { credential: { method: oauth } }      # built in: chatgpt.com, under /backend-api/ only
  claude:
    services:
      claude: { credential: { method: oauth } }       # built in: api.anthropic.com
```

- **Sign in from an agent**, where the client asks: `egzo agent spawn` and `egzo project check` say when a service has no
  sign-in. Use a sign-in that ends in the terminal: Codex's device code, or Claude Code's `/login`, which shows a code to
  paste back. A browser sign-in that calls back to a port inside the container cannot work: your browser cannot reach it.
- **Agents after the first** start signed in with the stand-ins of the project's sign-in, which they get when they are
  spawned: Codex an `auth.json`, Claude Code a refresh token that `claude auth login` signs in with before its TUI starts.
- **Stand-ins.** The client's code exchange goes to the provider; the proxy keeps the real access, refresh and id tokens
  and answers the client with stand-ins. A JWT stand-in carries the real claims (clients read them) with a signature that
  is no signature. A stand-in works only through the proxy: on the service's hosts and `paths` the proxy swaps it for the
  real access token, websocket upgrades included; anywhere else it stays a stand-in. Any other token is left alone.
- **Refreshes.** The client's refresh with a stand-in is answered by the proxy; a stand-in never reaches the provider.
  The proxy renews the real tokens itself, five minutes before they expire and at least every eight days, saving the
  rotated refresh token first. One proxy per project means one renewer, so agents never log each other out.
- **One sign-in per service per project**, shared by every agent. On a hub with several users that is one account for
  the project. Two projects signed in to one account log each other out.
- **At rest** the tokens are sealed with the [project secret key](project-file.md#the-secret-key) in the proxy's private
  volume. A restarted proxy opens them at the next `egzo project up`. With another key (after `egzo project rotate-ca`)
  they cannot be opened, and the project signs in again; `egzo project down` ends them too.
- **When it ends** (the client's logout, a revoked token, `egzo proxy logout chatgpt`), the proxy revokes the real token
  when the service has a `revoke_url`, and a stand-in then gets a 401 that says to sign in again. A refresh the provider
  refuses ends the sign-in the same way; `egzo project check` fails until someone signs in again.

A service of your own takes the same credential, with its token URL. The token URL's host is reached through the service:
the proxy answers the token and revoke URLs, and passes the rest of that host without any token.

```yaml
      my-idp:
        hosts: [api.example.com]
        paths: [/v1/]
        credential:
          method: oauth
          token_url: https://auth.example.com/oauth/token
          revoke_url: https://auth.example.com/oauth/revoke     # optional
```

The proxy reads what goes to the token and revoke URLs, so a compressed request body there is refused (`415`, with
`Accept-Encoding: identity`); the provider's answers are decoded whatever the client asked for.

Clients that pin certificates, or whose tokens are bound to a key the client holds (DPoP, mTLS), cannot be served this
way.

## Secrets the agent has to type: placeholders

`inject` suits a header the agent never touches. Some secrets the agent has to put somewhere itself, such as the
password of a login form in a browser. Give the service a `placeholder` instead of an `inject`:

```yaml
vaults:
  main:
    backend: pass

egress:
  default:
    services:
      example-login:
        hosts: [app.example.com]
        credential: main/sites/example/password
        placeholder: EXAMPLE_PASSWORD
```

Agents with this profile get `EXAMPLE_PASSWORD=egzo-ph-<random>` in their environment. They use it as the password
(`curl -d "password=$EXAMPLE_PASSWORD" https://app.example.com/login`, or typed into a form), and the proxy replaces it
with the real password on its way to `app.example.com`: in the URL, in headers and in the body, encoded the way the
place needs (form, JSON). Sent anywhere else, the placeholder stays a meaningless string. It never changes, even if you
rotate the secret.

The proxy has to read a request to find the placeholder, so a request to a placeholder host whose body is compressed
or larger than 1 MiB is refused with a message saying so (`413` or `415`, the `415` with `Accept-Encoding: identity`, as
RFC 9110 has it), always, not sometimes. Use `inject` for a
host that takes uploads, or send the upload to a host that has no placeholder.

Some clients cannot be served this way: see [Limits of secret injection](#limits-of-secret-injection). HTTP Basic
authentication, for example, is built by the client from the placeholder, which hides it; use `inject` for that.

## Several profiles

```yaml
egress:
  default:
    allow: [platform.claude.com]
    services:
      anthropic: env/ANTHROPIC_API_KEY
  operator:
    extend: default                 # one parent; allow lists are unioned
    services:
      github: env/GITHUB_TOKEN      # a string only sets the secret of an inherited or built-in service
      deploy-api: { hosts: [api.deploy.example.com], credential: env/DEPLOY_TOKEN,
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
  main: { backend: pass }

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

## A company network: `proxy.upstream`

The proxy reaches public addresses only, and trusts the system's roots. A service inside a company network, behind a
company CA, says so project-wide:

```yaml
proxy:
  upstream:
    ca: ./certs/internal-root.pem          # PEM certificates, added to the system roots for the TLS the proxy makes upstream
    private_hosts: [idp.internal.example]  # exact names the proxy may reach at a private address
```

- `ca` is read at `egzo project up` and sent to the proxy with the policy. It holds certificates and nothing else; a
  private key in it is an error.
- A private host may resolve to a private address, never to loopback or a link-local address (a cloud metadata
  endpoint). Reaching it still takes a profile that allows it.
- Names only: no wildcard, address, port or scheme.

## Full internet

```yaml
egress:
  open:
    allow: ["*"]
```

Still through the proxy, so still audited, still limited to port 443 and public addresses, and credentials are
injected only for the hosts of declared services. Use it for a trusted template, not by default.

## Limits of secret injection

egzo keeps secrets out of agents by rewriting HTTPS requests in the proxy: `inject` sets a header, a `placeholder`
is swapped for the secret (`DESIGN.md`, "Secrets and vaults"). That covers an API key in a header and a password in
a form. It does not cover everything a client can do with a secret, and in each case below the agent is not given
the secret to work around it: the feature does not work, and says nothing is injected.

### Signed requests (HMAC, AWS SigV4, signed URLs, JWTs the client mints)

A client that signs a request uses the secret to compute the signature, and the signature covers the method, the
path, the headers or the body. The agent only has a placeholder, so it signs with that, and the server rejects the
request. The proxy replaces text; it does not know the signing scheme, so it cannot sign again. Anything that needs
the *real key in the client* is out of reach: AWS SDKs and CLI, Stripe-style webhook signing, OAuth 1.0, and the like.

Works instead: services where the credential is the header (bearer tokens, API keys, session cookies). For a signed
API, give the agent a credential that does not sign (a short-lived token minted outside the sandbox), or run the
signing step outside it.

### Credentials the client transforms before sending

The proxy matches the placeholder text, so it must reach the wire unchanged. A client that encodes or hashes it first
hides it:

- **HTTP Basic authentication** is `base64(user:password)`. A client that builds it from the placeholder sends the
  base64 of the placeholder. Use `inject` with `basic_user` for the whole header instead.
- A login page whose JavaScript hashes or encrypts the password in the browser (a client-side `sha256`, SRP,
  password-manager style derivation) sends the hash of the placeholder.
- Any format that transforms the text: URL-safe base64, gzip of the body (`Content-Encoding` is not scanned),
  encryption inside the payload.

### Clients that do not behave like a proxy-aware HTTPS client

The proxy only sees what is sent to it as a `CONNECT` tunnel to port 443, and can only rewrite what it terminates
with the project CA. These do not get injection, or do not work at all:

- Clients that ignore `HTTPS_PROXY` (some language runtimes and SDKs, and software that opens sockets itself). The
  agent's network has no other route, so they fail to connect rather than leak.
- Clients that do not read the CA bundle egzo provides. Chromium uses its own NSS database, not `SSL_CERT_FILE`: the
  harness images import the project CA there (`egzo agent run`, needs `certutil`), and a browser in an image of your
  own needs the same. Java uses its own keystore; some Python HTTP libraries bundle their own CA list. They reject the
  proxy's certificate for any host that is injected into, inspected, or that the client reaches with Encrypted Client
  Hello (see [Browsing](browser.md)).
- Clients that pin certificates, for the same reason.
- Anything that is not HTTPS on port 443: SSH, raw TCP, plain HTTP (refused), UDP, a database protocol. A git remote
  over SSH cannot use a secret from egzo; git over HTTPS can.
- WebSocket messages after the upgrade are not rewritten.

### Request bodies to a placeholder host

The proxy reads a request body to find the placeholder, so it holds it in memory: up to 1 MiB, uncompressed. A request
to a placeholder host that does not fit those limits is **refused**, not forwarded: `413` for a body over 1 MiB and
`415` for a compressed one (`Content-Encoding`), with a message naming the reason, and a `deny` in the audit log.

This is deliberate. Forwarding the request with the placeholder still in it would make some requests work and others
fail with nothing to tell them apart, which is worse than a clear refusal. The rule depends only on the request, so
the same request always has the same outcome. The cost is that a host with a placeholder cannot take big uploads: use
`inject` for such a host, or a service for the upload that has no placeholder. Raising the limit, or scanning the
stream instead of buffering it (a fixed-length placeholder can be found in a stream, but then the request has to be
sent chunked, which some servers refuse), is possible if a real use-case needs it.

### What the agent sees when it fails

Nothing is retried with the real secret, and nothing falls back to sending it. The server gets the placeholder (or no
header), and the request fails the way a wrong password fails. A body the proxy cannot scan is refused with a message
instead (see above). `egzo proxy log` shows the request on an intercepted
host; it never shows a body.

### If a use needs one of these

If a real use-case needs one of these, the likely fixes are on the proxy side (re-signing for a named scheme, a
`placeholder` form for Basic authentication), added for that scheme only, with specs. Per-client problems with the CA
are image problems and belong in the image ([images](images.md)).

### A secret goes to every path of its hosts, unless the service has `paths`

A service binds a secret to hosts, and without `paths` the proxy applies it to every request an agent sends to one of
them. That is harmless when the host only serves an API. It is not when the host is also the vendor's website or console,
because the agent can then send the secret to pages and endpoints that have nothing to do with the API it was given for.

#### Where it applies today

- **`github`** (`github.com` and `api.github.com`). git over HTTPS needs `github.com`, which is also the website: the token
  goes with every request to it, a pull request page as well as `git push`.
- **Your own services**, whenever the host serves more than its API, until you give them `paths`.

Limited to their API's path already: **`chatgpt`** (`chatgpt.com`, the built-in OAuth service), under `/backend-api/`,
where Codex's calls go.

Planned: **`openrouter`** (`openrouter.ai`, [#148](https://github.com/egzo-ai/egzo/issues/148)), whose API is under `/api/v1/`, on the same host as the website.

Limited to their API's paths already: **`opencode`** (`opencode.ai`, also the OpenCode website, its documentation and the
Console), under `/inference/` and `/zen/` (the model calls, Zen and Go), `/console/api/v2/config` and
`/console/api/websearch` (below).

`anthropic` and `anthropic-oauth` (`api.anthropic.com`) are not affected: the host serves the API and nothing else.

#### What it costs

The secret only ever goes to the host it was bound to, which belongs to the party that issued it. It does not reach a third
party. What follows:

- **The website's own logs and analytics may see it.** They sit behind the same name as the API.
- **The agent can use it on any endpoint of the host,** not only the one it was meant for. A prompt-injected agent can call
  the GitHub web endpoints with the token, and whatever the credential allows is
  what it can do. The credential's own permissions are the only limit.

#### What to do about it now

Give the credential the least privilege it can work with. egzo cannot narrow it, the issuer can:

- **GitHub.** A fine-grained token limited to the repositories the agents work on, with only the permissions they need
  (contents, pull requests), not a classic token with the `repo` scope.

#### What `paths` leaves

- **`opencode`.** The paths are the ones OpenCode 2 sends its key to (anomalyco/opencode,
  `packages/core/src/plugin/provider/opencode.ts`): the model calls under `/inference/` (and `/zen/`, where they went before
  and where the Go usage meter is), and two Console calls. OpenCode fetches the workspace's config from
  `/console/api/v2/config` at start and every minute: it carries the header that routes the model calls to the workspace,
  the models the workspace turned off and its policies. Without the key there the Console answers 401, OpenCode reports
  the connection as signed out and runs without them. `/console/api/websearch` is the hosted web search, which OpenCode
  offers when that config enables it. The device sign-in (`/console/auth/`) is not covered: egzo binds a key. A Console
  config can carry voice and MCP servers that take the key too, under a base URL it gives; one not under these paths
  gets the placeholder.
- **`github`.** git over HTTPS to `github.com` sends its requests under `/<owner>/<repo>.git/`, so the prefix would have to
  name the repositories, and the list would then be the access policy. It stays unlimited, with a least-privilege token.
- A path rule is not an authorization rule: an endpoint under the prefix can still do more than the agent needs. It narrows
  the exposure, it does not replace a least-privilege credential.

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
- `reason.code` is from a fixed list, for tools ([below](#reason-codes)); `reason.text` is for people, and may change.
- `service` is the service of the profile that covers the host, on an allowed `connect`.
- On a `request`, `injected` names the service whose credential the proxy set, and `substituted` the services whose
  placeholder it swapped for the secret. A request to an injecting service's host outside its `paths` has neither, and the
  reason `outside-paths`, which also says when the agent sent the header itself. `path` is decoded, as the agent wrote it;
  `raw_path` is how it went on the wire, when its escapes make it differ (`%2F`).

| protocol | event | meaning |
|---|---|---|
| `https` | `connect` | the decision about a CONNECT. Allowed, its `mode` says how: `tunnel` (the proxy sees the host name, never the traffic), `inject` (a service injects a credential or swaps a placeholder), `inspect` (`inspect: true`), or `intercept` (the client sent Encrypted Client Hello, [browsing](browser.md)). Denied: not in the profile, not port 443, a private address, a TLS server name that differs from the host, wrong proxy credentials, too many connections. An error: the host did not answer, or the client left |
| `https` | `handshake` | the client's TLS handshake with the proxy failed on a terminated host: it does not trust the project CA |
| `https` | `request` | one request to a terminated host: method, path (never the query), status, and the credentials that went with it; or a request refused (another host, a placeholder that cannot be swapped, a path that could climb out of a service's `paths`) or failed upstream |
| both | `close` | the end of an allowed connection, with `bytes_up`, `bytes_down` and `duration_ms` |
| `ssh` | `connect` | an agent's ssh connection to a server of its profile (`server` is its name, `host` the real one), or a refused one (an identity or server that is not the agent's, too many connections; `server` is then the one asked for), or an error when the client left before it signed in |
| `ssh` | `upstream` | the server did not answer, refused the key, presented another host key than the pinned one, or would not open a session |
| `ssh` | `exec`, `shell`, `sftp` | a command (`command`), a shell or an sftp session (`scp`, `sftp`) the agent started on the server |
| `ssh` | `channel`, `request` | refused: port forwarding, X11, agent forwarding, a subsystem other than sftp |

Changing a profile and running `egzo project up` reloads the proxy with the new policy: running agents keep running and
follow it at once, ssh servers included, keeping their binding by profile name (only a change to what their container holds,
such as a new placeholder variable, makes them [stale](managing-projects.md)). After a proxy restart, `egzo restart proxy` (or `egzo project up`) loads the policy again; until
then the proxy answers 503. `egzo project rotate-ca` issues a new project CA and restarts the agents.

### Reason codes

| code | decision | meaning |
|---|---|---|
| `no-policy` | deny | the proxy was restarted and has no policy: `egzo restart proxy` or `egzo project up` |
| `no-credentials` | challenge | no proxy credentials yet |
| `credentials` | deny | wrong proxy credentials: the name is in `claimed_agent`, never in `agent` |
| `host` | deny | the CONNECT target is not a host name or address; it is said in the text, and the event has no `host` |
| `port` | deny | only port 443 is allowed |
| `not-in-profile` | deny | the host is not in the agent's profile |
| `private-address` | deny | the host is a private address |
| `limit` | deny | too many connections, the agent's or the proxy's |
| `client-hello` | deny | the client's TLS hello could not be read |
| `sni-mismatch` | deny | the TLS server name differs from the CONNECT host |
| `host-header` | deny | a request's `Host` header is malformed |
| `host-mismatch` | deny | a request names another host than its connection |
| `placeholder` | deny | a placeholder could not be swapped (a compressed or too large body, a secret that cannot go in a header) |
| `path` | deny | a request under a service's `paths` whose path the server could read as another one: a dot segment, a backslash, a needless or double escape, an escaped control character or byte outside ASCII |
| `outside-paths` | allow | why a request to an injecting service's host went without its credential: outside the service's `paths` (the text says when the agent sent the header itself) |
| `aborted` | error | the client left before the handshake (clients open connections ahead of time); for ssh, before it tried to sign in |
| `dial` | error | the upstream did not answer (for ssh too, a cloud metadata address included) |
| `tls-handshake` | error | the client's TLS handshake with the proxy failed: it does not trust the project CA |
| `upstream` | error | a request to the upstream failed |
| `ech` | allow | why the mode is `intercept`: the client sent Encrypted Client Hello |
| `identity` | deny | ssh: no valid identity for a server of a profile, or the agent's binding or profile changed while it signed in |
| `channel` | deny | ssh: a channel that is not a session (port forwarding, X11) |
| `forwarding` | deny | ssh: a global request (remote forwarding) |
| `request` | deny | ssh: a session request that is not relayed (agent forwarding, a subsystem other than sftp) |
| `host-key` | error | ssh: the server presented another key than the pinned one, or the pin is not usable |
| `key` | error | ssh: the profile's key is not usable |
| `auth` | error | ssh: the server refused the key |
| `session` | error | ssh: the server did not open a session |
| `config` | error | ssh: the listener has no host key |
| `idle` | (close) | ssh: closed after being idle |
