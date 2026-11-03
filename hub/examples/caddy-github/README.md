# The hub behind Caddy, with sign-in with GitHub

One way to run [the hub](../../../docs/hub.md) for a team, with tools egzo does not ship: Caddy in front, with certificates from Let's
Encrypt over the DNS challenge at Cloudflare, and sign-in with GitHub through Dex, with no password. The hub works behind any proxy and
with any OpenID Connect provider; this is an example of putting it together, not a part of egzo.

| File | |
|---|---|
| `compose.yaml` | Caddy, the hub and Dex |
| `.env.example` | everything you set: versions, host names, tokens, the GitHub app. Copy it to `.env` |
| `Caddyfile` | TLS for the three host names, and where each goes |
| `caddy/Dockerfile` | Caddy with the Cloudflare plugin, built on this machine |
| `dex.yaml.tpl` | Dex with GitHub as its only connector, filled in from the environment when Dex starts |

Setting it up (DNS, the Cloudflare token, the GitHub app, the first sign-in) is in
[docs/hub.md, "Example: Caddy and GitHub"](../../../docs/hub.md#example-caddy-and-github).

## Updating

Every version is in `.env`. To update, change one and run:

```sh
docker compose up -d
```

`EGZO_HUB_VERSION` and `DEX_VERSION` are images that compose pulls. Before 1.0 a hub release with a new database schema refuses the old
database: see [Settings](../../../docs/hub.md#settings).

### Caddy is built here

Caddy has no DNS provider built in, and the DNS challenge needs one (Let's Encrypt issues the previews' wildcard certificate only over
DNS). Caddy's own docs build the provider in with `xcaddy`, and so does `caddy/Dockerfile`. There is no image to pull: compose builds
it on this machine.

- **`CADDY_VERSION` and `CADDY_CLOUDFLARE_VERSION` name the image**, `egzo-hub-caddy:2.11.7-cloudflare-v0.2.4`. A new value is a tag that
  does not exist yet, so `docker compose up -d` builds it, then recreates Caddy with it. No `--build` is needed.
- **The build compiles Go**: a few minutes, about 1 GB of memory, and the `caddy:<version>-builder` image to pull. Caddy keeps serving
  on the old image until the new one is built.
- **The image is never pulled** (`pull_policy: never`), so `docker compose pull` skips it, and a name on a registry cannot replace it.
- **A failed build changes nothing running.** Put the old versions back in `.env` to stay where you were.
- **Old images stay.** `docker image ls egzo-hub-caddy` lists them, and `docker image rm egzo-hub-caddy:<old tag>` removes one.
- To build it again at the same versions (to pick up a newer base image), run `docker compose build --pull caddy`, then
  `docker compose up -d`.
