# Roadmap: OAuth services, captured at sign-in and renewed by the proxy

**Status.** Decided, not built: the specs will be the status. The first user is a Codex harness signed in with a ChatGPT Plus or Pro
account; the mechanism is generic OAuth. Comes after the mitigation of
[a secret is injected into every request to its hosts](../known-issues/a-secret-is-injected-in-every-request-to-its-hosts.md):
`chatgpt.com` is also ChatGPT's website, so the service has to be limited to `/backend-api/`.

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
    oauth: { token_url: https://auth.openai.com/oauth/token, store_key: main/egzo-store-key }
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

- **A store key from the vault.** An `oauth` service names a secret that is the key of the proxy's token store: 32 random bytes
  the user generates once (`openssl rand -hex 32`) and keeps in the vault, like the hub's `EGZO_SECRET_KEY`. It reaches the proxy
  with the policy (exec stdin at `up`) and lives in its memory only.
- **Sealed at rest.** The proxy keeps the tokens in an engine volume only it mounts (for example `<project>-credentials`),
  sealed with AEAD (XChaCha20-Poly1305) under a key derived with HKDF from the store key and the project, so one vault secret
  can serve several projects without sharing a key. A copy or backup of the volume is useless without the vault secret.
- **Restart.** A restarted proxy opens nothing until the next `up` delivers the key, as for every secret today.
- **Lost or changed key.** The store cannot be opened: the proxy discards it and the user signs in again. That is the only cost.
  Re-sealing under a new key (`up` given the old and the new) is later.
- The volume survives `project down`, like the instance branches. Removing a sign-in is explicit (`egzo service logout
  <service>`, name to decide).
- One sign-in per service per project, shared by every agent of the project. On a hub with several users that means one account
  for the project: the docs say so.
- Two projects that share a store volume or a sign-in log each other out; egzo does not prevent it, the docs say so.

```yaml
services:
  chatgpt:
    hosts: [chatgpt.com]
    paths: [/backend-api/]
    oauth:
      token_url: https://auth.openai.com/oauth/token
      store_key: main/egzo-store-key
```

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

A `codex` harness built on this: an image with a pinned Codex version, the `egzo` MCP server in `config.toml`, the status hooks,
and the stand-in `auth.json` above. The model hosts are `chatgpt.com` (through the service) for a ChatGPT account, or
`api.openai.com` with the `openai` service of [model-providers.md](model-providers.md) for an API key. `auth.openai.com` is
reached only through the `oauth` service.

## What was checked (2026-10-27, `openai/codex` `main`, from the source)

- The refresh is a standard OAuth refresh grant: `POST https://auth.openai.com/oauth/token`, JSON, public client id
  `app_EMoamEEZ73f0CkXaXp7hrann`, no client secret (`login/src/auth/manager.rs`).
- Proactive refresh: when the access token's `exp` is within 5 minutes, or else when `last_refresh` is older than 8 days. On a
  401: reload `auth.json` from disk, then refresh.
- The TUI's first-run screen offers ChatGPT sign-in, the one-time device code, and an API key (`tui/src/onboarding/auth.rs`).
- The device-code flow polls `https://auth.openai.com/api/accounts/deviceauth/…`, then exchanges the code at the token URL
  (`login/src/device_code_auth.rs`). The browser flow may also exchange the id token for an API key at the same URL
  (`token-exchange` grant, `obtain_api_key` in `login/src/server.rs`); its response is captured like any other.
- `auth.json` is written in place (truncate and write), and the refresh URL can be overridden with
  `CODEX_REFRESH_TOKEN_URL_OVERRIDE`. Neither is needed by this design.

Nothing of this was run against a real account yet.

## Open questions

- **Websockets.** If Codex sends model requests over a websocket, the injection happens on the upgrade request; check that the
  proxy handles it.
- **Codex's newer account code** (`agent_identity`, `account_grant`, `login_owner_id` in `manager.rs`): whether any of it sends a
  token elsewhere, or has a server check a JWT that a stand-in would fail. A captured session of a real sign-in answers both.
- **Clients this cannot serve:** a client that pins its certificate, or tokens bound to a key the client holds (DPoP, mTLS). Codex
  uses neither.
- **Claude's regular sign-in** (not `setup-token`) likely has the same shape and would be the second user. Not checked.
- **Terms of use.** Requests still come from OpenAI's own client and only a header changes, as in OpenAI's documented CI flow; to
  be read before it ships.

## Specs

- Engine specs with a stub token endpoint and a stub model backend, no account needed:
  - a sign-in through the proxy: the agent's `auth.json` holds stand-ins, the stub saw the real exchange;
  - a model request reaches the stub with the real token; a request with any other token is untouched;
  - the client's refresh is answered by the proxy and never reaches the stub with a stand-in;
  - the proxy renews ahead of `exp`, saves the rotated refresh token, and keeps working after a proxy restart and `up`;
  - the store volume holds no token in clear; with another store key the proxy discards it and asks for a sign-in;
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
