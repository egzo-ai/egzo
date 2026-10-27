# Roadmap: model providers, and harness settings passed through

**Status.** Decided, not built: the specs will be the status. No backward compatibility: the provider variables an agent gets
change (below).

**Fixed first: [a secret is injected into every request to its hosts](../known-issues/a-secret-is-injected-in-every-request-to-its-hosts.md).** That known issue is a
security shortcoming, not a limit to live with: a service cannot be narrowed to the paths of its API, so a key bound to a host
that is also a website or a console goes with every request to it. Its mitigation (a service limited to path prefixes) is built
before the services below, and each built-in service then declares the path prefix of its API.

## The problem

- **Only three providers work out of the box.** The built-in services are `anthropic`, `anthropic-oauth` and `opencode` (and
  `github`, which is not a model provider). For OpenAI, Google, OpenRouter or any other provider, the user writes a service and
  has to know the provider's host and the header its key goes in.
- **The provider variables are hard-coded per harness.** OpenCode only offers a provider whose key variable is set. Every OpenCode
  agent gets `ANTHROPIC_API_KEY` and `OPENCODE_API_KEY` (`internal/harness/opencode.go`), whatever its profile binds: it offers
  Anthropic models the proxy will deny, and hides every other provider even when the profile serves it. Claude Code has its own
  special case (`bearerProvider`, `ContainerEnv(bypass, bearer)`).
- **A harness setting egzo does not know cannot be set.** Effort is the first example: neither harness can be told an effort
  level from the project file, except through `env` for Claude Code. Any other setting needs egzo to learn a key.

## What was tested (2026-10-25, Docker, Claude Code 2.1.307, OpenCode 2.1.2)

The harness images, against a local stub that records the requests (model, `thinking`, `output_config.effort`):

| Configuration | Effort sent |
|---|---|
| Claude Code, `CLAUDE_CODE_EFFORT_LEVEL=low` | `low` |
| Claude Code, `--settings '{"effortLevel":"xhigh"}'` | `xhigh` |
| Claude Code, `CLAUDE_CODE_EFFORT_LEVEL=low` and `--effort max` | `low`: the variable wins |
| OpenCode TUI, `"model": "anthropic/claude-sonnet-5-5#high"` in `opencode.json` | `high` |
| OpenCode TUI, `OPENCODE_CONFIG_CONTENT='{"model":"anthropic/claude-opus-5-5#max"}'` over a global `opencode.json` | `max`, and its model: the variable wins over the file |
| `opencode run`, `#high` in the config's `model` | none: `run` reads a variant from `-m` only |
| `opencode run`, `agent.build.variant` | none |

- OpenCode calls effort a model **variant** (`provider/model#variant`); its TUI has no model or variant flag. The variants differ
  per model: `openai/gpt-5.6` takes `none` but not `minimal`, `anthropic/claude-sonnet-5-5` takes `low` to `max`.
- OpenCode answers "Model unavailable" for `openai/…` without `OPENAI_API_KEY`, and calls the provider as soon as the variable
  holds anything.
- OpenCode's catalog is `https://models.opencode.ai/api.json` (models.dev): 232 providers, about 8,900 models. Each provider has
  `env` (the variables OpenCode reads), `api` (its base URL; 207 of 232 have one, the others use their SDK's built-in host) and
  `npm` (the SDK). Each model has `reasoning_options`, whose effort values matched what OpenCode accepted for the two models tried.
  It does not say how a key is sent.
- OpenCode does not need the catalog host. With no network at all, `opencode models` answers at once from a snapshot built into
  the binary (29 `opencode-go` models against 37 live, `deepseek-v4.1-flash` included): without `models.opencode.ai` it knows the
  models of the day its image was built. What the TUI does with a configured model the snapshot lacks was not tried.

## Decisions

egzo cares about **where a key goes**: which hosts get it and in which header. That is the security boundary and only egzo can do
it. What the harness does with its model, effort and providers is the harness's, configured in the harness's own terms and passed
through.

### 1. Built-in services for the major providers

| Service | Host | Header | Variables |
|---|---|---|---|
| `anthropic` (exists) | api.anthropic.com | `x-api-key` | `ANTHROPIC_API_KEY` |
| `anthropic-oauth` (exists) | api.anthropic.com | `Authorization: Bearer` | `CLAUDE_CODE_OAUTH_TOKEN` |
| `opencode` (exists) | opencode.ai | `Authorization: Bearer` | `OPENCODE_API_KEY` |
| `openai` | api.openai.com | `Authorization: Bearer` | `OPENAI_API_KEY` |
| `google` | generativelanguage.googleapis.com | `x-goog-api-key` | `GEMINI_API_KEY`, `GOOGLE_API_KEY`, `GOOGLE_GENERATIVE_AI_API_KEY` |
| `openrouter` | openrouter.ai | `Authorization: Bearer` | `OPENROUTER_API_KEY` |
| `mistral` | api.mistral.ai | `Authorization: Bearer` | `MISTRAL_API_KEY` |
| `xai` | api.x.ai | `Authorization: Bearer` | `XAI_API_KEY` |
| `deepseek` | api.deepseek.com | `Authorization: Bearer` | `DEEPSEEK_API_KEY` |
| `groq` | api.groq.com | `Authorization: Bearer` | `GROQ_API_KEY` |

- **Names are OpenCode's provider ids**, so `model: openrouter/…` and `services: { openrouter: … }` read the same. `opencode-go/…`
  models use the `opencode` service: same host, same key.
- **Each row is checked against the provider's own documentation** when it is written (host, header, variable), and cited in a
  comment. The table above is from OpenCode's catalog and memory, not yet verified.
- **Why ten and not the catalog's two hundred.** A built-in service is a promise that a real key goes to those hosts in that
  header. A wrong entry leaks the key or breaks the agent, and on a provider nobody here uses, nobody notices. Most of the catalog
  is resellers, gateways and regional plans. A provider becomes built in when it is widely used, takes a static key in a header,
  has a fixed host, and has docs to cite (Together, Fireworks and Cerebras qualify the day someone asks). Everything else is a
  custom service (below).
- **An API-only host wherever the provider has one.** Of the new services, only `openrouter` shares its host with a website
  (`openrouter.ai`, the API under `/api/v1/`); it is built in anyway, limited to its API's path once the known issue above is
  fixed, and the known issue lists it. Its docs advise a key with a credit limit, used for the agents only (to verify with
  OpenRouter's docs when the row is written).
- **Not built in, ever:** providers whose host depends on the account (Azure, Databricks, Cloudflare), local ones (LM Studio) and
  signed requests (Bedrock with AWS access keys).

### 2. A service names the variables its clients read

A service gets `env`, a list of variable names. Binding the service in an agent's profile sets each of them in the agent to a
fixed dummy, which the client sends and the proxy replaces with the real key. It is how `ANTHROPIC_API_KEY` works today, made
general.

- **The variables come from the profile, not the harness.** `ContainerEnv`'s provider variables, `claudeKey`/`openCodeKey` as
  harness constants and `bearerProvider` go. An OpenCode agent whose profile binds only `openai` gets only `OPENAI_API_KEY`, and
  OpenCode offers only OpenAI's models.
- **The names are the providers'.** Most are what the provider's own SDK reads by default (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`,
  `GROQ_API_KEY`, `XAI_API_KEY`); `OPENROUTER_API_KEY`, `DEEPSEEK_API_KEY` and `MISTRAL_API_KEY` are the ecosystem's convention;
  Google's are split, so all three are set. Two are one tool's on purpose: `OPENCODE_API_KEY`, and `CLAUDE_CODE_OAUTH_TOKEN`
  (the only variable that puts Claude Code in the OAuth mode a subscription token needs; the portable `ANTHROPIC_AUTH_TOKEN`
  does not).
- **Every process in the agent sees them.** A script or a second tool that reads `OPENAI_API_KEY` gets the dummy and its
  requests get the real key, as with `anthropic` today. The docs say it: binding a service gives the whole agent that provider.
- **The dummy differs from `placeholder`.** A placeholder is swapped wherever it appears, which needs the proxy to read the
  request, and requests over 1 MiB or compressed are refused: model requests pass 1 MiB. A service with `env` injects its header
  and never reads the body.
- An agent's own `env` cannot set a variable one of its services sets (as for a placeholder). A custom service can declare `env`.

Any provider in OpenCode's catalog is then a recipe the docs give: the host of `api`, `Authorization: Bearer` for the
OpenAI-compatible ones (188 of them), and the variables of `env`:

```yaml
services:
  moonshot:
    hosts: [api.moonshot.ai]
    secret: main/moonshot-key
    inject: { header: Authorization, value: "Bearer {secret}" }
    env: [MOONSHOTAI_API_KEY]
```

### 3. Bedrock, through an API key

A Bedrock API key is a bearer token (`AWS_BEARER_TOKEN_BEDROCK`, which Claude Code and OpenCode read): a custom service and a
docs recipe, with no code. It cannot be built in: the host is per region (`bedrock-runtime.<region>.amazonaws.com`) and a service
that carries a secret takes no wildcard.

```yaml
egress:
  bedrock:
    services:
      bedrock:
        hosts: [bedrock-runtime.us-east-1.amazonaws.com]
        secret: main/bedrock-api-key
        inject: { header: Authorization, value: "Bearer {secret}" }
        env: [AWS_BEARER_TOKEN_BEDROCK]

agents:
  claude:
    harness: claude-code
    egress: bedrock
    env: { CLAUDE_CODE_USE_BEDROCK: "1", AWS_REGION: us-east-1 }
    model: us.anthropic.claude-sonnet-5-5…   # a Bedrock model or inference profile id
```

To verify with a real key before the recipe is published: which other hosts Claude Code calls in Bedrock mode (listing
inference profiles may use `bedrock.<region>.amazonaws.com`); how long short-term keys last (a key that expires needs another
`project up`); AWS's advice on long-term keys, and the IAM scope the recipe recommends. AWS access keys (SigV4) are not
supported: see "Later".

### 4. `settings:`, passed through to the harness

```yaml
agents:
  coder:
    harness: claude-code
    model: claude-opus-5-5
    settings: { effortLevel: high }                    # Claude Code's settings.json keys
  review:
    harness: opencode
    egress: oai
    model: openai/gpt-5.6#high                         # OpenCode's own syntax for effort
    settings:                                          # opencode.json keys
      provider: { openai: { options: { timeout: 600000 } } }
```

- A free-form object egzo never interprets, handed to the harness's own override layer: Claude Code's `--settings '<json>'`,
  OpenCode's `OPENCODE_CONFIG_CONTENT`. Not merged into the files egzo writes: the agent's home outlives a run and egzo merges
  into its files, so a key removed from `egzo.yaml` would stay. Through the flag or the variable, removed is gone.
- Refused for the `custom` harness, which has no such layer.
- Its string values go through the same secret check as `env`: they are visible in `inspect` and on the command line.
- It can override what egzo wires (the `egzo` MCP server, hooks, the plugin, permissions). That is the user's call: the container
  and the proxy are the boundary, not the harness's settings. The docs say what breaks (idle reporting, messages).
- `model:` stays as it is: a string passed through, the one setting every harness has.

Effort needs nothing else: `settings: { effortLevel: high }` or `env: { CLAUDE_CODE_EFFORT_LEVEL: high }` for Claude Code,
`model: provider/model#variant` for OpenCode. The docs give both, and point to OpenCode's `/variants` for a model's values.

### 5. OpenCode's catalog host belongs to the harness, and is optional

`models.opencode.ai` is not added to the `opencode` service: a service injects its key into every host it lists, and the catalog
takes none. It is not the provider's host either: an OpenCode agent that only uses `openai/…` fetches it too, and another client
of the `opencode` service never does. It stays where it is, the harness's host (`harnesses` in `internal/config/builtins.go`).

The warning for an OpenCode agent whose profile cannot reach it says something false today ("which opencode needs"). It says
instead that OpenCode will use the model list built into its image, which lacks newer models. The docs keep recommending
`models.opencode.ai` in an OpenCode profile, for a current list. (`docs/sandboxing.md` calls it the harness's provider host.)

### 6. Codex

No item of its own. A Codex harness is built with [oauth-services.md](oauth-services.md), whose first user it is: a ChatGPT
Plus or Pro account through the `chatgpt` OAuth service, an OpenAI API key through the `openai` service above. Codex does not read
`OPENAI_API_KEY` by itself: the harness writes the dummy into Codex's `auth.json` (`auth_mode: apikey`), and the proxy replaces it
as for any client.

## Specs and docs

Specs:

- `test_egress.py`: each new built-in service injects its key in its header on its host; an agent gets the variables of the
  services its profile binds and no other (an OpenCode agent without `anthropic` has no `ANTHROPIC_API_KEY`); a custom service's
  `env`; an agent `env` that collides with a service's.
- `test_harness_integrations.py`: `settings` reaches Claude Code as `--settings` and OpenCode as `OPENCODE_CONFIG_CONTENT`;
  OpenCode's TUI sends the effort of `model: …#high` (the scripted model can record it); `settings` refused for `custom`.
- The config specs: the secret check on `settings`; the warning for an OpenCode agent that cannot reach `models.opencode.ai` says
  the model list will be the image's.

Docs: `docs/sandboxing.md` (the services table, `env`, the custom-service recipe from the catalog), `docs/harnesses.md`
(providers, effort for each harness, `settings`, Bedrock), `docs/project-file.md` (`settings`).

## Later

- **AWS access keys for Bedrock.** The client signs each request (SigV4) over its headers and a hash of its body, so a dummy key
  cannot be swapped. The proxy would have to strip the signature and sign again with the real credentials: a new kind of
  injection, buffering every request body to hash it, session tokens and expiring STS credentials.
- More built-in providers, by the rule above.
- Model or effort hints at `egzo project check` from OpenCode's catalog. Not now: it needs the network at check time and puts
  egzo between the user and the harness's own errors.

## Alternatives considered

- **Project-wide defaults per harness (`harnesses: { claude-code: { model, effort } }`)** and an egzo `effort:` key translated
  per harness: an abstraction over two harnesses that already have their own syntax, which egzo would have to keep in step.
  Rejected for pass-through.
- **Generating the built-in services from the catalog** (about 200 with a fixed host and one key): too many hosts egzo would be
  promising to send a key to, unverified. The catalog stays reference for people writing services.
- **Placeholder services for the providers**, so egzo need not know each header: model requests pass the 1 MiB a placeholder
  host allows. Rejected.
- **Writing `settings` into the harness's config file**: survives its removal from `egzo.yaml`, see 4.
- **`args:`, extra words on the harness command line**: helps Claude Code only, which `settings` covers; OpenCode's TUI has no
  flags for any of this. Added when something needs it.
