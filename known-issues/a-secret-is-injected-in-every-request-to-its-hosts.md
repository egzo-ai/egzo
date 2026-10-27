# A secret is injected into every request to its hosts, whatever the path

Status: **mitigated for your own services** (written 2026-10-19, revised 2026-10-27). A service can be limited to the
paths of its API (`paths`, `docs/sandboxing.md`), and the built-in `opencode` service is, to the paths OpenCode sends its
key to. `github` cannot be (below). The built-in model providers of
[roadmap/model-providers.md](../roadmap/model-providers.md) and the OAuth services of
[roadmap/oauth-services.md](../roadmap/oauth-services.md) declare their paths.

A service binds a secret to hosts, and without `paths` the proxy applies it to every request an agent sends to one of
them. That is harmless when the host only serves an API. It is not when the host is also the vendor's website or console,
because the agent can then send the secret to pages and endpoints that have nothing to do with the API it was given for.

## Where it applies today

- **`github`** (`github.com` and `api.github.com`). git over HTTPS needs `github.com`, which is also the website: the token
  goes with every request to it, a pull request page as well as `git push`.
- **Your own services**, whenever the host serves more than its API.

Planned:

- **`openrouter`** (`openrouter.ai`, [model-providers.md](../roadmap/model-providers.md)): the API is under `/api/v1/`, on the
  same host as the website. The other planned providers have hosts that serve only their API.
- **`chatgpt`** (`chatgpt.com`, [oauth-services.md](../roadmap/oauth-services.md)): Codex's model calls go to `/backend-api/`,
  on the same host as ChatGPT itself.

Limited to their API's paths already: **`opencode`** (`opencode.ai`, also the OpenCode website, its documentation and the
Console), under `/inference/` and `/zen/` (the model calls, Zen and Go), `/console/api/v2/config` and
`/console/api/websearch` (below).

`anthropic` and `anthropic-oauth` (`api.anthropic.com`) are not affected: the host serves the API and nothing else.

## What it costs

The secret only ever goes to the host it was bound to, which belongs to the party that issued it. It does not reach a third
party. What follows:

- **The website's own logs and analytics may see it.** They sit behind the same name as the API.
- **The agent can use it on any endpoint of the host,** not only the one it was meant for. A prompt-injected agent can call
  the GitHub web endpoints with the token, and whatever the credential allows is
  what it can do. The credential's own permissions are the only limit.

## What to do about it now

Give the credential the least privilege it can work with. egzo cannot narrow it, the issuer can:

- **GitHub.** A fine-grained token limited to the repositories the agents work on, with only the permissions they need
  (contents, pull requests), not a classic token with the `repo` scope.

## The mitigation

A service takes a list of path prefixes (`paths: [/inference/]`), and the proxy injects only into requests under one of
them, by whole segments. A request under a prefix whose path has a dot segment or a backslash is refused, because the
server could resolve it outside the prefix. What is left:

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
