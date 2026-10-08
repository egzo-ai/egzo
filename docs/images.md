# Overriding base images

The stock harness images ([harnesses](harnesses.md)) are small: Node, git, ripgrep, curl and the harness. If your agents need a compiler, a
language runtime, `psql` or a linter, build an image of your own on top of the stock one and name it in the template.

## Extend a stock image

```dockerfile
# Dockerfile.agent
FROM ghcr.io/egzo-ai/egzo-harness-claude-code:latest

USER root
RUN apt-get update \
 && apt-get install -y --no-install-recommends python3 python3-pip build-essential postgresql-client \
 && rm -rf /var/lib/apt/lists/*
```

```console
$ docker build -f Dockerfile.agent -t registry.example/shop-agent:1 .
```

```yaml
agents:
  coder:
    harness: claude-code
    image: registry.example/shop-agent:1
    workspaces: [repo]
```

`image:` replaces the default image name; everything else about the harness stays: the entrypoint (`egzo agent run`),
hooks, MCP and the home volume. Use the same tag as your `egzo` version if you want to be sure the in-container
binary matches the CLI (`egzo version`; the stock images are tagged with it).

Rules for a derived image:

- **Keep the entrypoint.** Do not set your own `ENTRYPOINT`. It starts the session holder, which is what makes
  `attach`, messages and status work. Do not set `CMD` either.
- **Install as root, run as nobody in particular.** Egzo runs the agent as your uid and gid, whatever `USER` says,
  so tools must be world-readable and executable, and anything the agent writes must go to the workspace, `/tmp` or
  its home. `/home/agent` and `/workspace` are writable by everyone.
- **Do not bake secrets in.** Credentials come from the proxy. An image is not a place for a token, and nothing
  in it can be injected.
- **Network is the proxy.** `apt`, `pip` and `npm` at *run* time work only for hosts in the agent's profile ([sandboxing](sandboxing.md))
  (`allow: ["*.pypi.org", files.pythonhosted.org]`). Installing at *build* time, as above, needs no egress rule at
  all, and is the better place for tools.
- **Prefer the image to a runtime install.** Anything an agent installs into its home volume survives, but
  anything it installs system-wide does not, and an image gives every instance the same tools.

After changing the image tag, the template changes, so running instances become stale: `egzo up`, `egzo rm`, spawn again.

## Pin it

Use a version tag, not `latest`, for anything you share. A moving tag means two instances of one template can
differ.

## Building the stock images yourself

(See also [installing](install.md).)

```console
$ make images                   # builds egzo and egzo-harness-{claude-code,opencode}
$ EGZO_HARNESS_PREFIX=registry.example/team egzo spawn coder
```

`EGZO_HARNESS_PREFIX` moves where the default names are looked up, so a team can mirror the stock images.

## The `custom` harness

`harness: custom` takes any image and runs it as given:

```yaml
agents:
  linter:
    harness: custom
    image: docker.io/library/alpine:3
```

The sandbox still applies in full (own network, proxy only, no secrets, your uid, resources, optional gVisor), but
there is no harness integration: no hooks, no MCP tools and no home volume, and activity is judged from output
quietness instead of hooks. Use it to run a script or a tool egzo has no integration for yet, such as `pi`.
