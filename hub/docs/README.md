# egzo hub

A web UI over the egzo projects of one or more container engines. It shows what runs, lets people start, stop, remove and
attach to agents (a terminal in the browser), and decides who may do what. It does not create or change projects: the
project file and the `egzo` CLI stay the only way to do that (`egzo project up`, secrets, recreate).

The design and its reasons are in [roadmap/hub.md](../../roadmap/hub.md). This page is how to run it.

## Quick start

```sh
cd hub
cp .env.example .env            # set EGZO_SECRET_KEY: openssl rand -hex 32
export DOCKER_GID=$(stat -c %g /var/run/docker.sock)
docker compose up -d
docker compose logs hub         # the password of the starter account, admin, printed once
```

Open <http://localhost:8080> and sign in as `admin` with that password. `admin` is an operator, who holds every project the hub
finds. Create a project with the CLI as usual (`egzo project up`) and it shows up within a second or two.

> **The quick start mounts the Docker socket into the hub.** That is root-equivalent on the host: anyone who controls the hub
> controls the machine. Prefer a rootless engine, or an ssh engine on another machine (below), for anything but a trial.

## Reaching it

One container, one port, one volume. The hub is meant for a private network (a LAN, Tailscale, a tunnel), not the open internet.
When a reverse proxy is in front, set `EGZO_PUBLIC_URL` and `EGZO_TRUSTED_PROXIES` so the redirects and cookies use the right
address and scheme (cookies are `Secure` when the address is https).

## Signing in

### The starter account

`admin` signs in with a password, for starting out and for demos. Any setup with more than one person connects an identity
provider (below). There is no page to change the password or to make other local users.

- With `EGZO_ADMIN_PASSWORD` set, that is the password, saved at every start: the environment is the source of truth. Changing it
  signs out whoever signed in with the old one.
- Unset, the hub makes one on the first start and prints it **once** on standard output. Later starts print nothing.
- At every start the hub makes sure `admin` exists, is enabled and is an operator.
- After a few failed tries in a row the account waits a little longer before each next one (the hub is not meant to face the
  internet, so there is no lockout and no limit per address).

### Single sign-on

The hub speaks OpenID Connect to one provider of yours: Keycloak, Authentik, Authelia, Zitadel, Okta, Entra ID, Google, GitLab,
Auth0, Dex... It does not run one. Register a client at the provider:

- authorization code flow, confidential (with a secret) or public (no secret: the hub always uses PKCE);
- redirect address `<EGZO_PUBLIC_URL>/auth/callback` (the hub logs the exact value at start when `EGZO_PUBLIC_URL` is set);
- scopes `openid profile email`.

Then set `EGZO_OIDC_ISSUER` (exactly as the provider writes it, trailing slash included), `EGZO_OIDC_CLIENT_ID`,
`EGZO_OIDC_CLIENT_SECRET` if any, and `EGZO_OIDC_NAME` for the button. Examples of the issuer:

| Provider | `EGZO_OIDC_ISSUER` |
|---|---|
| Keycloak | `https://keycloak.example.com/realms/<realm>` |
| Authentik | `https://authentik.example.com/application/o/<application slug>/` |
| Google | `https://accounts.google.com` (make the OAuth app "Internal" to keep it to your Workspace) |

**Moving from the starter account to SSO:** sign in as `admin`, open your account (your name at the bottom of the sidebar), click
**Link**, sign in at the provider, sign out and back in with SSO to check, then set `EGZO_LOCAL_LOGIN=false` and restart. The
operator, and everything they own, is the same user throughout.

When it does not work:

| Symptom | Fix |
|---|---|
| The provider says the redirect address is not allowed | register `<EGZO_PUBLIC_URL>/auth/callback` exactly (scheme, host, port) |
| "the login provider's issuer is not EGZO_OIDC_ISSUER" | copy the issuer from `<issuer>/.well-known/openid-configuration`; a trailing slash or http/https counts |
| "cannot reach the login provider" | the hub (server side) and the browser must both reach the issuer: check DNS and the network from the hub's container |
| a certificate error | a private CA: mount it and set `SSL_CERT_FILE` (or `SSL_CERT_DIR`) in the hub's environment |

### People

- A person is made the first time they sign in with SSO, with a **username** the hub picks from what the provider says
  (`preferred_username`, then the email's local part, then the name), with `-2`, `-3`... when it is taken. It never changes after.
  They see nothing until an owner gives them a role: **to give a teammate access**, they sign in once, then an owner picks them in
  the project's Members tab.
- Nothing is matched by email. A person is their account at the provider (its issuer and subject); the email and name are shown,
  never trusted.
- The provider's picture is shown as the avatar. It is fetched by the browser, never by the hub, so the hub's content security
  policy allows https images, and the picture's host sees who views the page.
- **Disabling** (Settings, Users) signs a person out, closes their terminals and refuses their sign-in, by password and by SSO alike.
  Their roles stay, unused, until they are enabled again; their agents keep running. People are never deleted: the audit log and
  the agents' labels keep naming them. An operator cannot be disabled; remove operator first.
- A person who signed in with SSO before linking it to `admin` is a second user: disable that user, use **Remove** on its account
  under Settings, Users, then link again.

### Changing the identity provider

The hub trusts the one issuer it is configured with.

- Accounts of the old provider can no longer sign in, and their sessions end at the next start (the log names both issuers).
- Everyone who signs in at the new provider is a **new user with no access**, even with the same email, with a new username (`-2`
  when the old one is taken).
- Whoever can still sign in with a password (in practice `admin`) moves their own account: Link the new provider, then Unlink the
  old one. Their roles and agents stay.
- Everyone else is granted again by each project's owners, after they sign in once; `admin` makes the other operators operators
  again. Then disable the old users.
- In order: set `EGZO_LOCAL_LOGIN=true` and `EGZO_ADMIN_PASSWORD`, change the issuer and restart, sign in as `admin`, link the new
  provider and unlink the old, have the team sign in, grant them, disable the old users, turn local login off.
- Entra ID gives each app registration its own subjects: registering the app again is the same as changing provider.

### Locked out

`EGZO_LOCAL_LOGIN=true` with `EGZO_ADMIN_PASSWORD` set, and a restart, always gets `admin` back in as an operator, whatever
happened to the provider or the password. With local login off and no operator who has an account at the configured provider,
the hub starts and says so in its log; with local login off and no operator at all, it does not start.

## Roles

A role is held on one project of one engine. Roles live in the hub's database; the project file knows nothing about them.

| Role | May |
|---|---|
| user | see the project; spawn agents; start, stop, remove and open the terminal of their own agents |
| administrator | the same on every agent of the project; read the audit log and the logs |
| owner | an administrator who also grants and removes roles. An owner cannot remove or lower themselves |

**Operators** are hub-level, not a role. An operator manages the engines and the users, and holds **every project on every
engine as its owner**, without a grant: a project made after the hub started is theirs as soon as a scan lists it, and nothing is
written to the database for it. That includes other people's agents and terminals, so make somebody an operator only when you
would give them the keys to the machine the engines run on (they can already add engines). Operators appear on every project's
Members tab, shaded and tagged "operator": their access is not a role and cannot be changed there. Agents started with the CLI
belong to the operators. Every action, allowed or refused, is in the project's audit log.

`admin` is an operator. An operator makes or removes others under **Settings, Users**; the last operator cannot be removed. If
every operator's sign-in is lost, see "Locked out" above.

There is no messaging in the web UI: talk to an agent in its terminal.

## The terminal

Whoever may operate an agent can open its terminal (**Attach**); there is no read-only view. One person holds it at a time, and the
agent page says who. Opening it while somebody else does offers **Take over**, which ends their terminal ("taken over by ...") and
gives it to you ("Take it back" does the same the other way). Anyone who may open it may take it over. The terminal is sized to
the pane that shows it.

You can have several terminals open at once, across projects and engines. They are listed in the sidebar, stay connected while
you browse the pages, come back after a reload (the agent's session lives on the engine), and can be shown one at a time, side by
side, or four to a screen. Closing one detaches; the agent keeps running. Up to 12 can be open.

Keys inside a terminal belong to the agent, with one exception: **Ctrl+]** is the leader, and the key after it is the hub's
(`1`-`9` go to a terminal, `s` changes the layout, `f` is full screen, `d` closes, `?` lists them all). The browser keeps a few
chords for itself (Ctrl+W, Ctrl+T, Ctrl+N): a page cannot take them, so they act on the browser tab and never reach the agent. On
pages, `:` or Ctrl+K opens the command menu; single-key shortcuts can be turned off in the shortcut reference.

### Known issue: the CLI

The hub only knows the terminals it opened. `egzo agent attach` goes straight to the agent's session and the hub neither sees it nor can
end it. Attaching with the CLI to an agent that is open in the hub is risky: the session has one screen size, so each side's window
resize changes it for the other, and what both type reaches the program. Avoid it. Nothing in the hub prevents it.

## How the hub keeps up

The hub does not poll the engines. It reads two streams that the engines push, and lists the containers only when they say something changed:

- **The status feed.** The control sidecar of every project writes the agents' status to its log, as JSON lines on standard output (a line
  when something changes, and one every 30 seconds). The hub follows that log through the engine's log API, one stream per project,
  and what the sidebar, Home and the project pages show an agent doing (working, idle, blocked, waiting on a question, its status
  line) is the newest line. It runs no command in the sidecar for it.
- **The engine's events.** The hub watches the engine's event stream for the containers of egzo projects. When one is made,
  started, stopped or removed, it lists that engine again (a project, an agent, a sidecar appears or goes), within a fraction of a second.
- **Both are retried** with a delay that grows from one second to a minute when they break, and start over when they have run for
  a while. When an engine's events do not work, the hub lists it every 5 seconds instead; `EGZO_SCAN_INTERVAL` is the slow fallback.
- **A page that polls asks the hub, not the engine.** The project and agent pages read what the hub already holds. What it still
  reads from the engine is the templates (kept for five minutes, or until the sidecar is another container), and the timeline, log,
  spec and audit tabs, only while they are open.

This needs a log the engine can stream back, which is the engine's default driver (egzo chooses none): `json-file`, `local` or
`journald` on Docker, `k8s-file` or `journald` on Podman. Run `egzo system check` on the host. If the status cannot be read the
project page says why, and its agents show as **running**: the hub never says "starting" for a state it does not know. An old
control sidecar (made by an egzo before the status log) writes no status; run `egzo project up` again. See
[the known issue](../../known-issues/container-logging-follows-the-engine-default.md).

## Engines

Engines are added in the UI by an operator (**Settings, Engines**), not in the compose file. The connection is tested before it is saved.

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
| `EGZO_ADMIN_PASSWORD` | none | the starter account's password, saved at every start; unset, one is made and printed once |
| `EGZO_LOCAL_LOGIN` | `true` | the starter account's password sign-in; `false` needs single sign-on |
| `EGZO_OIDC_ISSUER` | none | single sign-on: the provider's issuer, exactly |
| `EGZO_OIDC_CLIENT_ID`, `EGZO_OIDC_CLIENT_SECRET` | | the client registered at the provider; the secret is optional (a public client uses PKCE) |
| `EGZO_OIDC_SCOPES` | `openid profile email` | replaces the scopes (some providers want `offline_access`, some refuse scopes they do not know) |
| `EGZO_OIDC_NAME` | `SSO` | what the sign-in button says |
| `EGZO_HUB_LISTEN` | `:8080` | the one port |
| `EGZO_HUB_DATA` | `/data` | the SQLite database (a volume) |
| `EGZO_PUBLIC_URL` | derived | the address people use, if the proxy cannot be trusted to say |
| `EGZO_TRUSTED_PROXIES` | none | addresses or CIDRs of the reverse proxy whose `X-Forwarded-Host` and `X-Forwarded-Proto` are believed |
| `EGZO_LOCAL_ENGINE` | `unix:///var/run/docker.sock` | seeded as `local` when the hub knows no engine; `none` for no engine |
| `EGZO_SCAN_INTERVAL` | `60s` | how often an engine is listed again whatever its events said: a fallback for a missed event (changes are found by the events). An engine whose events do not work is listed every 5 seconds instead |

The hub does not terminate TLS. Behind a proxy set `EGZO_TRUSTED_PROXIES`, so the login redirect and the cookies use the right
address and scheme.

The database records its schema version. Before 1.0 there are no migrations: a hub that finds a database made by another version
refuses to start and says to remove `hub.db` (engines, roles and users are then set up again).

## Development

The hub is its own Go module (`hub/`); it imports core's packages and core never imports it.

```sh
make hub-test           # go vet and go test -race
make hub-lint           # staticcheck and govulncheck
make hub-web            # the web app (Svelte, Vite) into internal/web/dist, which the binary embeds (node, or docker)
make hub-web-test       # its type check and unit tests
make hub-build          # bin/egzo-hub
make hub-integration    # the Docker engine implementation against a real engine and a real project
make hub-e2e            # the browser tests (Playwright, in docker), against the hub with fake engines
make hub-stack          # the whole stack for real: image, Dex as single sign-on (a test fixture, e2e/), a real project, a real terminal
make hub-image
```

Tests: ordinary Go unit tests (the engine seam is faked in `internal/engines/fake`, the login provider in `internal/oidctest`; the
API is tested through HTTP in `internal/web`), Vitest for the web app's logic (`web/src/lib/*.test.ts`), Playwright tests of the app
in `e2e/`, and the integration and stack runs above. Core's specs-first rule does not apply here.

### The web app

`web/` is a Svelte 5 + TypeScript single-page app built by Vite. The hub serves it for every address that is not the API, so the
app's own routes (`/e/lab/p/web/a/coder-1`) survive a reload. The design it implements is in `roadmap/egzo-design/` (tokens,
components and the clickable prototype). The API it talks to is described in [api.md](api.md).

```sh
cd hub/web && npm ci && npm run dev     # the app on :5173, proxying /api and the login to a hub on :8080 (EGZO_HUB=...)
```

There is no node on some machines; `make hub-web` and `make hub-web-test` run in docker when `npm` is missing.
