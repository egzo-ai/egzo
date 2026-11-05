# Reference: OAuth sign-ins, what was checked

How the clients egzo serves through an [OAuth service](../sandboxing.md#oauth-services) sign in, refresh and log out, as read from
their source or tried, and why the proxy holds the tokens. The mechanism itself is in [Sandboxing](../sandboxing.md#oauth-services)
and DESIGN.md. Moved from the design page `roadmap/oauth-services.md` when the roadmap moved to GitHub issues.

## Why the proxy holds the tokens

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

## Claude Code's sign-in

Tried on 2026-10-28 with Claude Code 2.1.310 and a Claude Pro account, through a custom service under the `custom`
harness, and read from the code Claude Code ships (the strings of its binary):

```yaml
claude:
  hosts: [api.anthropic.com]
  credential: { method: oauth, token_url: https://platform.claude.com/v1/oauth/token }
```

`/login` in the agent, the model calls, a refresh, a new proxy container and an agent restart all worked; the agent held only
stand-ins. It is a second user of the mechanism, next to `claude setup-token`, which stays the simpler way to a subscription.

- **Sign-in.** `/login`, then *Claude account with subscription*. The authorize URL (`claude.com/cai/oauth/authorize`, with
  PKCE) redirects to `platform.claude.com/oauth/code/callback`, which shows a code the user pastes into the terminal: no
  callback port, so it works in a container.
- **Tokens.** The exchange goes to `https://platform.claude.com/v1/oauth/token` as JSON (public client id
  `9d1c250a-e61b-44d9-88ed-5944d1962f5e`). The answer has an opaque access token (`sk-ant-oat01-…`, 8 hours), a refresh token
  (`sk-ant-ort01-…`), `scope`, the account and the organization. Claude Code keeps them in `~/.claude/.credentials.json`.
- **Refresh.** JSON, with the scope. Claude Code keeps a refreshed token only if the answer's `scope` has `user:inference`;
  otherwise it drops it without a word and goes on with the old one until it expires. A refresh token works once
  (Claude Code's own message for `CLAUDE_CODE_OAUTH_REFRESH_TOKEN` says it "cannot be used again").
- **Where the token goes:** `api.anthropic.com`, as `Authorization: Bearer`: `/v1/messages`, `/v1/mcp_servers`,
  `/api/oauth/profile`, `/api/oauth/claude_cli/roles`. The test set no `paths`; a limit would be `/v1/` and `/api/oauth/`.
- **Compression.** Claude Code asks for compressed answers and Anthropic's servers compress them.
- **Prefix checks.** Claude Code checks `startsWith("sk-ant-oat")` on a static key and on `CLAUDE_CODE_OAUTH_TOKEN`, not on a
  `/login` sign-in, so `egzo-…` stand-ins work there.

### Proxy changes this needed

- Answers from the token and revoke URLs are decoded: the client's `Accept-Encoding` is not forwarded, and an answer the proxy
  cannot read is refused, never passed on. A compressed request body there is refused (`415`, `Accept-Encoding: identity`).
- Every answer the proxy gives repeats what the provider's last answer said besides the tokens (`scope`, the account), kept
  with the sign-in; a renewal that leaves the scope out keeps the old one.
- The proxy renews in the client's encoding (JSON here).

### Not seen yet ([#153](https://github.com/egzo-ai/egzo/issues/153))

- **The proxy's own renewal** against Anthropic: it runs five minutes before the real access token expires, eight hours
  after a sign-in.
- **A refused refresh.** Anthropic's errors read `{"error": {"type", "message"}}`. The proxy ends a sign-in only on OAuth's
  `invalid_grant`; if a refusal is shaped like the other errors, the proxy keeps retrying instead of asking for a sign-in.
- **Logout.** Whether `/logout` revokes the token, and at which URL (the test service had no `revoke_url`).

## Alternatives considered

- **A shared `auth.json` in a volume, linked into every agent** (multica's model). Codex re-reads the file before refreshing, so
  sharing mostly works, but the agent holds a credential that mints tokens.
- **A copy per agent** (scion's model). Breaks at the first refresh.
- **An egzo command that runs `codex login --device-auth` in a throwaway container** and moves the tokens to the proxy. Works,
  but adds a command and a step; capture at sign-in needs neither.
- **Importing the host's `~/.codex/auth.json`.** The user's own Codex and egzo would share one chain and log each other out.
- **Codex access tokens.** Business and Enterprise only.
