# Browsing

Every `claude-code` and `opencode` agent has a browser. The harness images carry Chromium and the Playwright MCP server,
and egzo registers it with the harness next to its own `egzo` server, so the agent can open pages, click, fill forms and
take screenshots with the usual `browser_*` tools. There is nothing to install and no image to build. What the browser can
reach is what the agent's [egress profile](sandboxing.md) lets it reach, like everything else.

```yaml
egress:
  default:
    allow: [platform.claude.com, example.com]       # besides the model API, the browser may open example.com and nothing else
    services:
      anthropic: main/ANTHROPIC_API_KEY
```

A page served by the agent's own dev server (`http://localhost:3000`) needs nothing: `localhost` never goes through the
proxy.

## How much of the internet

Browsing tends to need more hosts than a coding task (a page pulls scripts and fonts from a dozen of them), so a browsing
agent often gets `allow: ["*"]`. That is legal and it is your call; the proxy still audits every connection, refuses
private addresses and plain HTTP, and injects credentials only for the hosts of declared services. To see what the browser
actually requested, give a service with no secret a wildcard host and `inspect`:

```yaml
egress:
  browsing:
    allow: ["*"]
    services:
      anthropic: main/ANTHROPIC_API_KEY
      pages: { hosts: ["*"], inspect: true }       # every request: method, path, status (never the query or a body)
```

A service host may be `host`, `*.domain` or `*`, **only when the service holds no secret**: `inject` and `placeholder`
are for exact hosts, because `*.example.com` would hand the secret to every subdomain, including ones somebody else
controls. `egzo config` refuses it. `*.domain` covers the names below the domain, not the domain itself. Because a
wildcard service is an allowlist entry too, it also lets the agent reach what it covers.

## Logging in to a site

The browser follows the rules of any other client. For a site whose password the agent must not hold, give the service a
`placeholder`: the agent types `$SITE_PASSWORD` (the value in its environment) into the form, and the proxy swaps it for
the real password on its way to that host ([sandboxing](sandboxing.md)). It does not work for a page that hashes the
password in the browser.

## What the proxy does for a browser

Chromium sends an *Encrypted Client Hello* extension with every connection. A proxy that tunnels cannot tell a harmless one
from one that hides the real destination, so egzo does not tunnel it: for a client that sends it, the proxy terminates TLS
with the project CA, as it does for a host with `inspect`, and fetches the page itself, checking the site's real
certificate. Clients that do not send it (curl, git, Node, Python) are tunneled as before. The harness images trust the
project CA in Chromium's own store, so this needs nothing from you. In the log (`egzo proxy log`) such a connection shows
as `intercept` and then `close` with the bytes that went through, not as one line per request. A host with `inspect: true` is terminated for every client and logs each request instead.

A browser also opens every proxy connection without credentials and sends them when challenged, so you will see a
`challenge` per connection. That is not a refusal.

## Limits

- A site that pins its certificate does not load: the proxy is the TLS server for it.
- The proxy speaks HTTP/1.1 to the browser. WebSocket messages pass but are not rewritten.
- Playwright keeps its output (snapshots, screenshots) in a temporary directory, not in the workspace.
- The images are larger by about 710 MB uncompressed (about 220 MB compressed) than without the browser. The arm64
  images are built from the same Dockerfile, but the browser has not been tried on arm64.
- A browser in your own image: keep `playwright-mcp` on the `PATH` and the `egzo agent run` entrypoint, and the harness
  registers it; without `playwright-mcp` no browser server is registered ([images](images.md)).

Something that does not load: [troubleshooting](troubleshooting.md#a-browser-page-does-not-load).
