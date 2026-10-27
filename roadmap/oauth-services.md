# Roadmap: OAuth services, captured at sign-in and renewed by the proxy

**Status.** Decided, not built: the specs will be the status. The mechanism is generic OAuth; its first user is a Codex harness
signed in with a ChatGPT Plus or Pro account, built as part of this work. Needs:

- [project-secret-key.md](project-secret-key.md): the sign-ins are sealed at rest with the project's secret key;
- the mitigation of [a secret is injected into every request to its hosts](../known-issues/a-secret-is-injected-in-every-request-to-its-hosts.md):
  `chatgpt.com` is also ChatGPT's website, so the service is limited to `/backend-api/`.

## The problem

A service today injects a static secret that the CLI reads from a vault at `up`. That covers API keys and long-lived tokens such
as Claude's `claude setup-token`. It does not cover a sign-in whose tokens rotate:

- **A ChatGPT Plus or Pro account has no long-lived token.** Codex signs in with OAuth (in the browser, or with a one-time device
  code) and keeps an access token, an id token and a refresh token in `~/.codex/auth.json`. Codex refreshes them about every
  8 days, when the access token is close to its `exp`, and on a 401. Codex access tokens (`CODEX_ACCESS_TOKEN`) exist for
  ChatGPT Business and Enterprise workspaces only.
- **A refresh token works once.** After a refresh the old one is refused (`refresh_token_reused`). Every copy of `auth.json`
  competes, and the first to refresh logs the others out. OpenAI's CI guidance says one file per runner, never shared by
  concurrent jobs.
- **A vault cannot hold it.** The first refresh makes the stored copy useless, and egzo only reads vaults. The user does not
  need the token in the vault anyway: what matters is that it is encrypted at rest and that Codex keeps working without
  signing in again.

How others do it, with the sign-in inside the agent:

- **scion** (`harnesses/codex`, 2026-10-26) stores a captured `auth.json` as a secret and writes a copy into every agent at start.
  Nothing writes a refreshed file back: after the first refresh the secret and the other copies are stale.
- **multica** links each task's `auth.json` to the user's own `~/.codex/auth.json`, so every task and the user's Codex share one
  file. Its PR #2126 fixed a fallback that copied the file instead of linking it.

Both leave a credential that can mint new tokens inside the agent. egzo's rule is that the agent never holds a real credential.

## The design

The proxy terminates TLS for the token endpoint, keeps the real tokens and gives the client stand-ins. The user signs in where the
client already asks, and nothing is configured beyond the service.

```yaml
services:
  chatgpt:
    hosts: [chatgpt.com]
    paths: [/backend-api/]        # the path mitigation of the known issue
    oauth: { token_url: https://auth.openai.com/oauth/token }
```

1. **Sign-in, in the agent.** The first Codex agent of the project starts without a sign-in and shows Codex's own first-run
   screen. The user attaches (CLI or the hub's terminal) and picks *Sign in from another device with a one-time code*. `agent
   spawn` says so when the project's `oauth` service has no sign-in. The browser option cannot work in a container: its callback is
   `http://127.0.0.1:<port>/auth/callback` in the container, which the user's browser cannot reach.
2. **Capture.** The device-code flow ends with an ordinary `authorization_code` exchange at the token URL (redirect
   `https://auth.openai.com/deviceauth/callback`). The proxy forwards it, saves the real `access_token`, `refresh_token`,
   `id_token` and `expires_in` from the response, and rewrites the response with stand-ins before the client reads it. Any
   response from the token URL is rewritten, whatever the grant: the agent cannot get a real token through the proxy.
3. **Inject.** On the service's hosts and paths, a request whose `Authorization: Bearer` is a stand-in gets the current real
   access token. A request with any other token is not touched.
4. **The client's refreshes.** A refresh request to the token URL with a stand-in refresh token is answered by the proxy: it
   refreshes the real tokens if they need it and returns new stand-ins. A stand-in is never forwarded upstream. The client keeps
   its normal behaviour.
5. **Renewal.** The proxy refreshes the real tokens ahead of `exp` (or `expires_in`), and at least every 8 days for Codex, so the
   chain stays alive while no agent runs. One proxy per project means one refresher: no race between agents. The rotated refresh
   token is saved before the new access token is used.

### Stand-ins look real

A client may read its tokens. Codex decodes the JWT claims (plan, account id, email, `exp`) without checking the signature
(`codex-rs/login/src/token_data.rs`, `decode_jwt_payload`). A JWT stand-in therefore carries the real token's claims with a
meaningless signature; an opaque token gets a random value of the same shape. A stand-in is useless anywhere but through this
proxy.

### Where the tokens are kept

The tokens are internal to the proxy and never leave it. egzo does not write to the user's vault.

- **Sealed at rest** with the [project secret key](project-secret-key.md) (its `egzo/v1/oauth` key), in an engine volume only
  the proxy mounts. A copy or backup of the volume is useless without the vault secret.
- **Restart.** A restarted proxy opens nothing until the next `up` delivers the key, as for every secret today.
- **Another key.** The sign-ins cannot be opened: the proxy discards them and the user signs in again.
- The volume survives `project down`, like the instance branches. Removing a sign-in is explicit (`egzo service logout
  <service>`, name to decide).
- One sign-in per service per project, shared by every agent of the project. On a hub with several users that means one account
  for the project: the docs say so.
- Two projects that share a sign-in log each other out; egzo does not prevent it, the docs say so.

### Agents after the first

Without help each new Codex agent shows the sign-in screen again, and a second sign-in starts a new chain that replaces the
first. Once the project has a sign-in, egzo writes a stand-in `auth.json` (`auth_mode: chatgpt`, the stand-in tokens,
`last_refresh`) into each new Codex agent. This is the only harness-specific part: where the client keeps its tokens and in what
format.

### A sign-in that stops working

The refresh token is revoked (sign-out everywhere, a password change), expires after a long idle period, or the volume is gone.
The proxy reports it where the user looks: `project check`, the hub's status feed, and the agent's own error (a 401 with a body
that says to sign in again).

## The Codex harness

A `codex` harness built on this: an image with a pinned Codex version, the `egzo` MCP server and `use_agent_identity = false` in
`config.toml`, the status hooks, and the stand-in `auth.json` above. The model hosts are `chatgpt.com` (through the service) for a ChatGPT account, or
`api.openai.com` with the `openai` service of [model-providers.md](model-providers.md) for an API key. `auth.openai.com` is
reached only through the `oauth` service.

## What was checked (2026-10-27, `openai/codex` at `c587d6e`, from the source)

Read from the source, not run against a real account.

- **Refresh.** A standard OAuth refresh grant: `POST https://auth.openai.com/oauth/token`, JSON, public client id
  `app_EMoamEEZ73f0CkXaXp7hrann`, no client secret (`login/src/auth/manager.rs`). Proactive when the access token's `exp` is
  within 5 minutes, or else when `last_refresh` is older than 8 days. On a 401: reload `auth.json`, then refresh.
- **Sign-in.** The TUI's first-run screen offers ChatGPT sign-in, the one-time device code and an API key
  (`tui/src/onboarding/auth.rs`). The device-code flow polls `auth.openai.com/api/accounts/deviceauth/…`, then exchanges the code
  at the token URL (`login/src/device_code_auth.rs`), with no API-key exchange (only the browser flow does one).
- **JWTs** are decoded without a signature check (`login/src/token_data.rs`), so stand-ins carrying the real claims work.
- **Where the token goes:** only `chatgpt.com/backend-api/` (`/codex` for the model, `/wham` for usage and limits, `/ps/mcp` for
  ChatGPT apps, a stable feature on by default). The path limit covers all of it.
- **Websockets.** The default OpenAI provider uses the Responses API over a websocket. Auth is on the upgrade request only, never in
  frames (`codex-api/src/endpoint/responses_websocket.rs`). The dialer honours `HTTPS_PROXY`, and every client honours
  `SSL_CERT_FILE` (`http-client/src/custom_ca.rs`). egzo's proxy sets the injected header on the upgrade request (`ReverseProxy`,
  HTTP/1.1 for upgrades). If the websocket fails, Codex falls back to HTTP for the session.
- **Agent identity** (`use_agent_identity`, under development, off by default): Codex would register its own ed25519 key and sign
  `AgentAssertion` headers, a credential held by the agent. The harness pins it off in `config.toml`, and the proxy never injects
  on `auth.openai.com/api/accounts/`, so a registration fails and Codex falls back to ChatGPT auth.
- **EMA** (enterprise-managed authorization of MCP servers) exchanges the refresh token with third-party authorization servers.
  It runs only for MCP servers configured with an EMA registration; egzo writes the config and adds none. A token exchange with a
  stand-in refresh token is refused.
- **`login_owner_id`** is a local id generated at sign-in, never sent.
- **Logout** (`codex logout`) sends the refresh token to `auth.openai.com/oauth/revoke` (`login/src/auth/revoke.rs`).

### Proxy rules this adds

- On `auth.openai.com`: `/api/accounts/deviceauth/*` passes without injection; `/oauth/token` and `/oauth/revoke` are answered
  by the proxy; nothing else there is injected.
- `/oauth/revoke` with a stand-in revokes the real token and discards the sign-in: a logout signs the project out.
- A refresh the proxy cannot serve (a dead chain) is answered with OAuth's `invalid_grant`, which Codex treats as permanent and
  asks for a new sign-in; that sign-in is captured.

## Open questions

- **A real session** to confirm the above end to end.
- **Clients this cannot serve:** a client that pins its certificate, or tokens bound to a key the client holds (DPoP, mTLS).
- **Claude's regular sign-in** (not `setup-token`) likely has the same shape and would be the second user. Not checked.
- **Terms of use**, to be read before it ships.

## Specs

- Engine specs with a stub token endpoint and a stub model backend, no account needed:
  - a sign-in through the proxy: the agent's `auth.json` holds stand-ins, the stub saw the real exchange;
  - a model request reaches the stub with the real token; a request with any other token is untouched;
  - a websocket upgrade gets the real token like any request;
  - `/oauth/revoke` with a stand-in signs the project out; a dead chain answers `invalid_grant`;
  - the client's refresh is answered by the proxy and never reaches the stub with a stand-in;
  - the proxy renews ahead of `exp`, saves the rotated refresh token, and keeps working after a proxy restart and `up`;
  - the volume holds no token in clear; with another secret key the proxy discards the sign-in and asks for a new one;
  - two agents of the project, one sign-in;
  - a revoked refresh token is reported by `project check` and in the agent's error;
  - a stand-in sent to a host outside the service stays a stand-in.
- One online spec with a real account, listed in
  [specs-depend-on-online-services.md](../known-issues/specs-depend-on-online-services.md).

## Alternatives considered

- **A shared `auth.json` in a volume, linked into every agent** (multica's model). Codex re-reads the file before refreshing, so
  sharing mostly works, but the agent holds a credential that mints tokens.
- **A copy per agent** (scion's model). Breaks at the first refresh.
- **An egzo command that runs `codex login --device-auth` in a throwaway container** and moves the tokens to the proxy. Works,
  but adds a command and a step; capture at sign-in needs neither.
- **Importing the host's `~/.codex/auth.json`.** The user's own Codex and egzo would share one chain and log each other out.
- **Codex access tokens.** Business and Enterprise only.
