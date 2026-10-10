# The specs depend on online services

Status: **open** (written 2026-10-10). Specs should be self-sufficient; these are not.

A spec that needs the outside world fails when it is unreachable (`CLAUDE.md`: never skipped), so a run without internet
or with one of these sites down shows red specs that say nothing about egzo. What the suite reaches today:

- **`example.com`, `example.org`**: the egress and audit specs (`test_proxy.py`, `test_agents.py`, `test_daily.py`,
  `test_instances.py`, `test_operations.py`) fetch real pages through the proxy to prove allow and deny. Several other
  specs only use the names as strings in a config and need nothing.
- **`httpbin.org`, `httpbingo.org`**: the injection and placeholder specs in `test_proxy.py` need a server that echoes back
  the headers and body it received. These are third-party sites, and `httpbingo.org` has no say in our uptime.
- **`github.com`**: the git workspace specs clone a public repository (`test_git_workspaces.py`, parts of
  `test_workspaces.py`, `test_up_down.py`, `test_egress.py`, `test_init.py`).
- **A registry** (`localhost:5000`, or `EGZO_SPEC_REGISTRY`): `test_images.py`. That is ours to run, but it has to exist.
- **`docker.io`** (`alpine:3`, `apk add curl git`) and the harness image builds (npm, apt): the agent images the specs build
  come from the internet the first time.

## Why it is not just fixed

The proxy refuses private and internal addresses, deliberately (`docs/security.md`), and checks TLS toward the real host
with the system roots. So a local echo server or git server on the spec machine is not reachable through the proxy, and
making it reachable means a switch in production code that weakens what the specs are testing (the address filter, the
upstream certificate check). Hooks that exist only for the specs are exactly what a spec must not need.

## Directions, not decided

- **A spec-owned "internet"**: a container on the engine with a public-looking address, a certificate from a CA the proxy
  is told to trust, and echo and git services behind names the specs control. It needs the proxy to accept an extra trust
  root and to resolve those names, both as a documented setting rather than a test hook, if either is useful to a user at
  all (a corporate CA and internal hosts are real needs, and `allow` of a private address is a security decision, not a
  spec convenience).
- **Keep the real sites for a few end-to-end specs** (one real clone, one real page) and use a local fixture for everything
  that only needs *a* server, so the suite mostly runs offline and the few that do not are named.
- **Pre-built images** so a run does not build from `docker.io` and npm.

Until then, `specs/README.md` lists what needs the outside world.
