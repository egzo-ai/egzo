# Roadmap: browser access for agents (idea, not decided, not built)

Need: many projects want an agent to drive a browser (test a dev server, read a site, take screenshots).
egzo cannot support every tool an agent might need, so the real question is how little egzo has to do.

**Open decision: do we ship a browser in our own harness images, or only document how a user adds one?**
Shipping it means a heavier image (about 400-600MB), a Chromium and Playwright version to track, and a
supported setup for the proxy CA. Documenting it means egzo ships nothing and the user owns the image.
Not decided. The README should show the "extend the image" recipe either way, once the README work starts.

## What already works

`image:` is a per-agent field and the harness only selects the integration, so a user can build
`FROM ghcr.io/egzo-ai/egzo-harness-claude-code`, add Chromium and Playwright, and point `image:` at it. The
browser then goes through the agent's proxy and egress profile like everything else, so the allowlist and
secret injection apply to it too.

Sketch (untested):

```dockerfile
ARG HARNESS=ghcr.io/egzo-ai/egzo-harness-claude-code
FROM ${HARNESS}
USER root
ENV PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright
RUN npm install -g @playwright/mcp playwright \
 && npx playwright install --with-deps chromium \
 && apt-get install -y --no-install-recommends libnss3-tools \
 && rm -rf /var/lib/apt/lists/* && chmod -R a+rX /opt/ms-playwright
COPY browser-entrypoint.sh /usr/local/bin/browser-entrypoint.sh
ENTRYPOINT ["/usr/local/bin/browser-entrypoint.sh"]
CMD ["claude"]
```

```sh
#!/bin/sh
# Chromium ignores SSL_CERT_FILE; it reads the NSS db. Add the egzo proxy CA (mounted at runtime) to it.
mkdir -p "$HOME/.pki/nssdb"
[ -f "$HOME/.pki/nssdb/cert9.db" ] || certutil -d "sql:$HOME/.pki/nssdb" -N --empty-password
certutil -d "sql:$HOME/.pki/nssdb" -A -t "C,," -n egzo -i /etc/egzo/ca/ca.crt
exec /usr/local/bin/egzo agent run -- "$@"
```

Tools are baked in at build time because anything downloaded at runtime has to pass the egress profile.

## To verify before recommending anything

- Chromium honors `HTTPS_PROXY` on Linux, but Playwright may override it; probably pass `--proxy-server` explicitly.
- Whether a second MCP server (Playwright MCP) survives next to the `egzo` one egzo writes into the harness
  config, given `/home/agent` is a volume. Fallbacks: a `.mcp.json` in a workspace, or the agent runs
  `npx playwright` scripts directly.
- Chromium's own sandbox usually needs `--no-sandbox` under gVisor or an unprivileged uid (the container is the boundary).
- `/dev/shm` is 64MB by default: `--disable-dev-shm-usage`, or a `resources` field for shm size.

## Alternatives considered

- **Per-instance browser sidecar** (browserless or headless-shell on the instance's network, CDP over the
  network): smaller agent image, separate limits or runtime, room for a visible session (noVNC). But `localhost`
  in the agent is not `localhost` for the browser (bad for testing a dev server), a CDP endpoint is full control
  of the browser, and it needs a new concept: extra containers per template.
- **One shared browser service**: rejected. It breaks one-network-per-agent isolation and leaks sessions and cookies.
- **Claude in Chrome extension**: drives a person's real Chrome and sign-ins; not a sandboxed headless agent.

## Related ideas (not Chrome-specific)

- **`build:` on an agent** (Compose style): egzo builds the user's Dockerfile on `up` or `spawn`, tags it by
  content hash, records it in the template hash so a changed Dockerfile shows as a stale instance. Removes the
  manual `docker build`, and passes the harness image as a build arg.
- **Image contract** (for `custom` or from-scratch images): the `egzo` binary at `/usr/local/bin/egzo`, entrypoint
  `egzo agent run -- <cmd>`, `HOME=/home/agent`, writable `/workspace` for any uid, `git`, `ca-certificates`,
  `procps`, the CA bundle path. Maybe `egzo doctor --image X` to check it.
- **General per-template sidecars**: only if something needs a separate runtime or limits, never one feature per tool.
- Rejected: declarative `packages:` or `setup:` (egzo would become a package manager).
