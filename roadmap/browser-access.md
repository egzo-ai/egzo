# Roadmap: browser access for agents (decided, in progress)

Need: many projects want an agent to drive a browser (test a dev server, read a site, take screenshots). A browser is
a basic tool, so egzo ships one: Chromium and the Playwright MCP server are in **the stock `claude-code` and `opencode`
harness images**, registered with the harness, and they work as long as the project's egress profile lets the agent
reach what it browses. There is no browser flavor of the image and no recipe to follow.

The rule that shaped everything below: **the client is never customized to suit the proxy.** What the proxy cannot
handle safely it handles itself.

## What was tested (2026-10-10, Docker, Chromium 155, `@playwright/mcp` 0.0.83, Claude Code 2.1.293)

- Through an HTTP CONNECT proxy Chromium sends **Encrypted Client Hello GREASE on every connection**, never real ECH: it
  does not resolve the destination, so it never learns an ECHConfig (even for `crypto.cloudflare.com`, which publishes
  one). The extension has a random `config_id`, a 32-byte `enc`, a 144/176/208/240-byte payload, and the **outer SNI is
  the real host**.
- RFC 9849 makes GREASE and real ECH the same format on purpose, so a proxy cannot tell them apart by looking. Stripping
  the extension is impossible too (the client's `Finished` covers its hello).
- The egzo proxy refused any ECH hello at a tunnel, so every HTTPS site failed. A browser policy
  (`EncryptedClientHelloEnabled: false`) fixes it, but only for a well-behaved Chromium, and it is client customization.
- With the browsed hosts **terminated** (the proxy is the TLS server, with the project CA), Chromium works unchanged:
  example.com, Wikipedia, Hacker News, GitHub, BBC, Cloudflare, a WebSocket echo and a 20 MB download all worked; the proxy
  used 23 MiB and ~0% CPU after ~500 requests. A terminated connection never forwards the client's ECH.
- Playwright MCP needs: the **installed** binary (`npx @playwright/mcp` asks the npm registry at run time, which the
  allowlist denies); a browser installed by `playwright-mcp install-browser chrome-for-testing` (it bundles its own
  `playwright-core`, whose Chromium revision differs from `npx playwright install`'s); **proxy credentials as separate
  fields** (Chromium ignores `user:pass@` in a proxy URL and `--proxy-server` has no credentials); `localhost` bypassing
  the proxy (otherwise loopback goes through it and gets a 405); `chromiumSandbox: false` (uid 1001, no user
  namespaces); `--disable-dev-shm-usage` (`/dev/shm` is 64 MB).
- Chromium ignores `SSL_CERT_FILE`: it reads the NSS database, so the project CA must be imported there.
- A second MCP server survives next to `egzo`'s: egzo merges its entry into the harness config.

## The plan

### 1. The proxy terminates what it cannot tunnel safely

At a plain tunnel the proxy reads the client's first TLS record. If it carries `encrypted_client_hello` (0xfe0d, or the
draft codepoint 0xff03):

- the **outer SNI must equal the CONNECT host**, as for any hello; a missing or different name is denied, so ECH cannot
  be used to front (this check was always there and stays);
- otherwise the proxy **terminates TLS** with a project-CA certificate, exactly like a host with `inspect: true`, replaying
  the bytes it already read. It never tunnels an ECH hello and never denies one that names the approved host;
- the audit line is `intercept` (reason: Encrypted Client Hello), then `close` with the bytes that went through, like a
  tunnel. **No per-request `request` lines** unless the host's service has `inspect: true`: browsing would drown the log;
- the request inside must still be for the CONNECT host (421 otherwise), the upstream certificate is checked with the
  system roots, plain HTTP and private addresses stay refused;
- a client that does not trust the project CA fails the handshake; the audit says why and what to do (the client sent
  ECH, so egzo had to terminate TLS, and it does not trust the project CA).

A browser starts every proxy connection **without credentials** and sends them only once challenged with a 407 (Playwright
cannot send them first). That first CONNECT is not a denial: with no `Proxy-Authorization` header at all the proxy answers
407 and logs `challenge`; credentials that are present and wrong are still a `deny` that names the agent. Otherwise a
browsing session would fill the audit with a false `deny` per connection.

It applies to every allowed host that would have been a tunnel, whatever the profile, and there is no switch for it. Clients
that send no ECH (curl, Node, Python, git) keep tunneling, so nothing about them changes. Pinned-certificate clients that
send ECH (Chromium, Firefox) fail on hosts the proxy terminates, as they already do on injected hosts.

### 2. Service hosts may be wildcards, but only without a secret

Service `hosts` accept the same patterns as `allow`: a host, `*.domain`, or `*`.

| Config | Result |
|---|---|
| wildcard host, no `inject`, no `placeholder` | accepted. It is an allowlist entry (a wildcard service **is** an allow: it counts for reachability) and, with `inspect: true`, every request to a covered host is audited (method, path, status; never the query or a body) |
| wildcard host with `inject` or `placeholder` | **refused at `egzo config` / `up`**: a secret binds to exact hosts. `*.example.com` would hand it to every subdomain, including attacker-controlled ones |
| a secret bound (string form, from `extend` or a child) to a wildcard service | refused, for the same reason (such a service has neither `inject` nor `placeholder`) |
| a wildcard no-secret service next to an exact injecting service on a covered host | accepted: the exact one injects, `inspect` still applies, so `browse: { hosts: ["*"], inspect: true }` coexists with `anthropic` |
| two services that disagree on injection for the same host | refused, as before |
| unknown service keys (`insecure`, `skip_verify`, `ech`, ...) | refused, as before. egzo has no knob that weakens TLS verification or the ECH rule, and will not get one |

Host names stay case-insensitive and a trailing dot is ignored, wildcards included. `allow: ["*"]` stays legal and
silent (it is a documented, deliberate choice; a warning on every `up` would only be noise).

### 3. The browser in the harness images

- **Image** (`harness/claude-code`, `harness/opencode`): the pinned `@playwright/mcp`, its Chromium installed at build time
  in `/opt/ms-playwright` (world-readable; `PLAYWRIGHT_BROWSERS_PATH`), the system libraries Chromium needs, and
  `libnss3-tools` (`certutil`). Everything is installed at build time because anything fetched at run time has to pass the
  egress profile. The images grow by roughly 400-600 MB.
- **`egzo agent browser`** (in-container role, same binary): builds the Playwright MCP configuration from the agent's
  environment (the proxy URL's credentials as separate fields, `localhost` and `127.0.0.1` bypassing the proxy, headless,
  isolated profile, no sandbox, `--disable-dev-shm-usage`, output in a temporary directory, never the workspace) and
  runs `playwright-mcp` with it. No credential is written to disk beyond that process's own temporary config.
- **Registration**: the harness integration writes a `playwright` MCP server (stdio) next to the `egzo` one, in Claude
  Code's `~/.claude.json` and OpenCode's `opencode.json`, **only when `playwright-mcp` is on the `PATH`**, so an image
  without a browser gets no dangling server.
- **CA trust**: `egzo agent run` imports the project CA into `~/.pki/nssdb` when `certutil` is on the `PATH` (best effort,
  idempotent), so Chromium trusts hosts the proxy terminates with nothing for the user to do.
- Nothing else is configured in the client: no ECH policy, no proxy flags by hand.

### 4. Specs and docs

Specs: `test_browser.py` (the browser in both images, through the real proxy), `test_proxy.py` (ECH termination and its
refusals, wildcard `inspect` and injection side by side), `test_egress.py` (wildcard service rules). Docs: `docs/harnesses.md`,
`docs/images.md`, `docs/sandboxing.md`, `docs/security.md`, `docs/troubleshooting.md`, `known-issues/secret-injection-limits.md`,
`DESIGN.md`.

## Costs and limits we accept

- A browser's HTTPS goes through the proxy's TLS stack (CPU, memory) and the proxy checks every site's real certificate.
  Logs of terminated hosts are the tunnel kind (one `intercept`, one `close`), not per request, unless `inspect: true`.
- Sites that pin their certificate break in a browser, as for any injected or inspected host.
- A terminated connection speaks HTTP/1.1 to the agent (the proxy always does). HTTP/2-only upstreams, large uploads and
  WebSocket messages were not exercised beyond the experiment above; WebSocket messages are not rewritten for placeholders
  (`known-issues/secret-injection-limits.md`).
- Login with a password the agent must not hold is `placeholder` (`DESIGN.md`, "Placeholders"). It does not work for a page
  that hashes the password in the browser.
- The images are also built for arm64 (`release.yml`): Playwright's Chromium for Linux arm64 has not been tried here.
- Untested: gVisor and Podman with Chromium, headed mode (noVNC), a model driving the tools.

## Alternatives considered

- **A browser policy disabling ECH** baked into the image: works, but customizes the client, only protects a Chromium that
  honors it, and leaves the proxy refusing ECH from everything else. Rejected for that reason.
- **The proxy accepts ECH when the outer SNI equals the host**: GREASE passes, and so does fronting through a provider that
  ignores the outer name. Rejected.
- **The proxy accepts ECH when the host publishes no ECH config in DNS**: needs DNS HTTPS-record lookups, trusts DNS, and
  leaves Cloudflare-hosted sites broken. Rejected.
- **Intercept everything by configuration** (`inspect` on `*`): works, but the user has to ask for it and a profile that does
  not is broken for browsers. The wildcard service rule above keeps it available as an audit choice.
- **Per-instance browser sidecar** (browserless, CDP over the network): `localhost` in the agent is not the browser's, a CDP
  endpoint is full control of the browser, and it needs extra containers per template.
- **One shared browser service**: breaks one-network-per-agent isolation and leaks sessions and cookies.
- **Claude in Chrome extension**: drives a person's real Chrome; not a sandboxed headless agent.

## Related ideas (not Chrome-specific)

- **`build:` on an agent** (Compose style): egzo builds the user's Dockerfile on `up` or `spawn`, tags it by content hash,
  records it in the template hash so a changed Dockerfile shows as a stale instance, and passes the harness image as a
  build arg.
- **Authenticating agents by source address** on their own network, so a client needs no proxy credentials (the Playwright
  wrapper exists only because Chromium cannot send them in the proxy URL). A separate decision.
- **Image contract** (for `custom` or from-scratch images): the `egzo` binary at `/usr/local/bin/egzo`, entrypoint
  `egzo agent run -- <cmd>`, `HOME=/home/agent`, writable `/workspace` for any uid, `git`, `ca-certificates`,
  `procps`, the CA bundle path, and the `PATH` entries that switch on integrations such as the browser. Maybe
  `egzo doctor --image X` to check it.
- **General per-template sidecars**: only if something needs a separate runtime or limits, never one feature per tool.
- Rejected: declarative `packages:` or `setup:` (egzo would become a package manager).
