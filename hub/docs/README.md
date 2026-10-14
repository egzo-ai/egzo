# egzo hub

A web UI over the egzo projects of one or more container engines. It shows what runs, lets people start, stop, remove and
attach to agents (a terminal in the browser), and decides who may do what. It does not create or change projects: the
project file and the `egzo` CLI stay the only way to do that (`egzo project up`, secrets, recreate).

The design and its reasons are in [roadmap/hub.md](../../roadmap/hub.md). This page is how to run it.

## Quick start

```sh
cd hub
cp .env.example .env            # set EGZO_SECRET_KEY and EGZO_OIDC_CLIENT_SECRET: openssl rand -hex 32
echo "127.0.0.1 dex" | sudo tee -a /etc/hosts      # a first try on one machine: the browser must reach Dex by the name the hub uses
export DOCKER_GID=$(stat -c %g /var/run/docker.sock)
docker compose up -d
```

Open <http://localhost:8080>, sign in with the local account of `dex.yaml` (`admin@example.com`, password `password`; replace it
with a real connector before anything else). **The first person to sign in is the operator** and owns every project the hub
finds. Create a project with the CLI as usual (`egzo project up`) and it shows up within seconds.

> **The quick start mounts the Docker socket into the hub.** That is root-equivalent on the host: anyone who controls the hub
> controls the machine. Prefer a rootless engine, or an ssh engine on another machine (below), for anything but a trial.

## Logging in

The hub speaks OpenID Connect to one issuer and no more. The compose file bundles [Dex](https://dexidp.io), which turns GitHub,
GitLab, Google, LDAP, SAML or any OIDC provider into one: put your connector in `dex.yaml` (see Dex's documentation). Any other
OIDC provider works the same way; set `EGZO_OIDC_ISSUER`, `EGZO_OIDC_CLIENT_ID` and `EGZO_OIDC_CLIENT_SECRET`, with
`https://<hub>/auth/callback` as the redirect address.

A person is created in the hub when they first sign in, and can see nothing until an owner gives them a role.

## Roles

A role is held on one project of one engine. Roles live in the hub's database; the project file knows nothing about them.

| Role | May |
|---|---|
| user | see the project; spawn agents; start, stop, remove and open the terminal of their own agents |
| administrator | the same on every agent of the project; read the audit log and the logs |
| owner | an administrator who also grants and removes roles. An owner cannot remove or lower themselves |

Agents started with the CLI belong to the operator. A project nobody owns (one made after the hub started, or by someone else)
is owned by the operator, at the next scan. Every action, allowed or refused, is in the project's audit log.

There is no messaging in the web UI: talk to an agent in its terminal.

## The terminal

Whoever may operate an agent can open its terminal; there is no read-only view. One person holds it at a time, and the agent page
says who. Opening it while somebody else does offers **Take over**, which ends their terminal ("taken over by ...") and gives it to
you. Anyone who may open it may take it over. The terminal is sized to the browser window that opens it.

### Known issue: the CLI

The hub only knows the terminals it opened. `egzo attach` goes straight to the agent's session and the hub neither sees it nor can
end it. Attaching with the CLI to an agent that is open in the hub is risky: the session has one screen size, so each side's window
resize changes it for the other, and what both type reaches the program. Avoid it. Nothing in the hub prevents it.

## Engines

Engines are added in the UI by the operator (**Engines**), not in the compose file. The connection is tested before it is saved.

| Address | Notes |
|---|---|
| `unix:///var/run/docker.sock` | the engine of the quick start (seeded as `local`). Rootless Podman: its own socket |
| `tcp://host:2376` | with TLS: paste the CA, the client certificate and key. Without TLS the traffic is clear text |
| `ssh://user@host[/path/to/docker.sock]` | with a private key (no passphrase). The server's host key is shown once and pinned; a changed key is refused |

Credentials are encrypted in the database with `EGZO_SECRET_KEY`, are never shown again, and a blank field on an edit keeps what is
stored. Losing the key means entering the credentials again. One unreachable engine is reported and the others keep working.

## Settings

All settings are environment variables, set in the compose file.

| Variable | Default | |
|---|---|---|
| `EGZO_SECRET_KEY` | required | encrypts engine credentials; at least 16 characters |
| `EGZO_OIDC_ISSUER`, `EGZO_OIDC_CLIENT_ID`, `EGZO_OIDC_CLIENT_SECRET` | required | the login provider |
| `EGZO_HUB_LISTEN` | `:8080` | the one port; put a TLS reverse proxy in front of it |
| `EGZO_HUB_DATA` | `/data` | the SQLite database (a volume) |
| `EGZO_PUBLIC_URL` | derived | the address people use, if the proxy cannot be trusted to say |
| `EGZO_TRUSTED_PROXIES` | none | addresses or CIDRs of the reverse proxy whose `X-Forwarded-Host` and `X-Forwarded-Proto` are believed |
| `EGZO_LOCAL_ENGINE` | `unix:///var/run/docker.sock` | seeded as `local` when the hub knows no engine; `none` for no engine |
| `EGZO_SCAN_INTERVAL` | `5s` | how often engines are asked for their projects |

The hub does not terminate TLS. Behind a proxy set `EGZO_TRUSTED_PROXIES`, so the login redirect and the cookies use the right
address and scheme.

## Development

The hub is its own Go module (`hub/`); it imports core's packages and core never imports it.

```sh
make hub-test           # go vet and go test -race
make hub-lint           # staticcheck and govulncheck
make hub-web            # the scripts and styles, and xterm.js, into internal/web/dist (node, or docker)
make hub-build          # bin/egzo-hub
make hub-integration    # the Docker engine implementation against a real engine and a real project
make hub-e2e            # the browser tests (Playwright, in docker), against the hub with fake engines
make hub-stack          # the whole stack for real: image, Dex login, a real project, a real terminal
make hub-image
```

Tests: ordinary Go unit tests (the engine seam is faked in `internal/engines/fake`, the login provider in `internal/oidctest`),
Playwright tests of the pages in `e2e/`, and the integration and stack runs above. Core's specs-first rule does not apply here.
