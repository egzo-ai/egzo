# A secret is injected into every request to its hosts, whatever the path

Status: **to fix** (written 2026-10-19). Mitigation not built; it comes before the built-in model providers of
[roadmap/model-providers.md](../roadmap/model-providers.md).

A service binds a secret to hosts, and the proxy applies it to every request an agent sends to one of them: egzo's rules
are by host, and a service cannot be limited to a path or a method (`docs/sandboxing.md`). That is harmless when the host
only serves an API. It is not when the host is also the vendor's website or console, because the agent can then send the
secret to pages and endpoints that have nothing to do with the API it was given for.

## Where it applies today

- **`opencode`** (`opencode.ai`). The model calls go to `opencode.ai/inference/go/…`, but `opencode.ai` is also the OpenCode
  website, its documentation and the Console. OpenCode itself calls the Console (`/console/api/v2/config`), and the key
  goes with that request and with any other request to the host.
- **`github`** (`github.com` and `api.github.com`). git over HTTPS needs `github.com`, which is also the website: the token
  goes with every request to it, a pull request page as well as `git push`.
- **Your own services**, whenever the host serves more than its API.

Planned in [roadmap/model-providers.md](../roadmap/model-providers.md): **`openrouter`** (`openrouter.ai`), whose API is under
`/api/v1/` on the same host as the website. The other planned providers have hosts that serve only their API.

`anthropic` and `anthropic-oauth` (`api.anthropic.com`) are not affected: the host serves the API and nothing else.

## What it costs

The secret only ever goes to the host it was bound to, which belongs to the party that issued it. It does not reach a third
party. What follows:

- **The website's own logs and analytics may see it.** They sit behind the same name as the API.
- **The agent can use it on any endpoint of the host,** not only the one it was meant for. A prompt-injected agent can call
  the Console API with the OpenCode key, or the GitHub web endpoints with the token, and whatever the credential allows is
  what it can do. The credential's own permissions are the only limit.

## What to do about it now

Give the credential the least privilege it can work with. egzo cannot narrow it, the issuer can:

- **OpenCode.** Use a service account with the permission *models: request* and no others (the one this was first tested
  with also had *providers: write*, which lets a stray request change provider settings). Whether *models: request* alone
  is enough has not been tested.
- **GitHub.** A fine-grained token limited to the repositories the agents work on, with only the permissions they need
  (contents, pull requests), not a classic token with the `repo` scope.

## Mitigation, if it becomes necessary

A service could take a list of path prefixes (`paths: [/inference/]`), and the proxy would inject only into requests whose
path starts with one. The proxy already terminates TLS for every host with an `inject` and sees the path of each request,
so the information is there. Open questions before building it:

- A client may need the credential for a call outside the prefix. OpenCode's Console config call is one such candidate; it
  has not been tested without the key.
- A path rule is not an authorization rule: an endpoint under the prefix can still do more than the agent needs. It narrows
  the exposure, it does not replace a least-privilege credential.
- git over HTTPS to `github.com` sends its requests under `/<owner>/<repo>.git/`, so the prefix would have to name the
  repositories, and the list would then be the access policy.
