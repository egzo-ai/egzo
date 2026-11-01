# The hub

The hub is a web UI over the egzo projects that link to it. It shows what runs and what each agent is doing, lets people spawn, start,
stop, remove and attach to agents (the agent's real TUI, in a terminal in the browser), and decides who may do what. It is optional and
separate from the CLI: a container of its own (`ghcr.io/egzo-ai/egzo-hub`). It holds no way into your machines. A project names its hub in
its own file and [dials out to it](managing-projects.md#linking-to-a-hub), so an agent spawned in the hub shows up in `egzo ps`, and one
spawned with the CLI shows up in the hub.

What the hub does on a project (a spawn, a start, a stop, a removal) can take from a second to a minute, so it is a task: the hub
answers at once and runs it in the background, and the agent shows it while it runs (`spawning`, `stopping`, with the step it is
at), next to what the project says, for everyone who sees the project. One task at a time runs on an agent. A spawn needs a name,
and the dialog suggests one such as `coder-sunnycar2332`; the agent is in the list from then on, and its page shows the steps and
opens its terminal when it is ready. A task that failed stays on its agent with why (a git branch left by an earlier agent of that
name, for instance) until whoever may operate the agent dismisses it, and the project's **Activity** tab lists every task, who
asked and how it went. Whoever asked gets a toast when the agent is not on their screen, or when the task failed.

![Two agents' terminals side by side in the hub: one waiting on a question, one answering](../assets/hub-terminals.png)

It does not create or change projects: the project file and the `egzo` CLI stay the only way to do that (`egzo project up`,
secrets, recreate). This page is how to run it; [access](hub-access.md) is who may do what, and [the hub
API](hub-api.md) is what the web app talks to. The design and its reasons are in [roadmap/hub.md](../roadmap/hub.md).

## Quick start

With Docker and its compose plugin:

```sh
mkdir egzo-hub && cd egzo-hub
curl -fsSLO https://raw.githubusercontent.com/egzo-ai/egzo/main/hub/compose.yaml
cat > .env <<EOF
EGZO_PUBLIC_URL=http://$(hostname -I | cut -d' ' -f1):8080
EOF
cat .env                        # review the settings before starting
docker compose up -d
docker compose logs hub         # the password of the starter account, admin, printed once
```

`EGZO_PUBLIC_URL` is the address of this machine, because a project's link runs in a container and `localhost` there is the link itself.
`.env` holds the hub's settings ([Settings](#settings)); the other ones are listed, commented, in
[`hub/.env.example`](../hub/.env.example). Set `EGZO_HUB_VERSION` in `.env` to a release (`0.9.0`) to pin the image; the default is `latest`.

Open that address and sign in as `admin` with that password ([the starter account](hub-access.md#the-starter-account)). `admin` is an
operator, who may connect projects and [invite people](hub-access.md#invites). Then link a project, on the same machine or any other that reaches the hub, by adding the
address to its file:

```yaml
hub: http://192.168.1.20:8080
```

```console
$ egzo project up
...
link: not registered: run `egzo project register`
```

In the hub, open **Settings, Links** and click **Link a project**. Run `egzo project register` and paste the code it shows. See
[Linking a project](#linking-a-project). Over plain `http://` anyone on the way can pose as the hub and read the code, and `project check`
warns: use https for anything but a trial.

## Linking a project

A project connects itself: nothing has to reach it, so a laptop or a box behind NAT works, and the hub lists only the projects whose owners
linked them. [Linking to a hub](managing-projects.md#linking-to-a-hub) is the project's side.

Each project is a **registration**: an id from the hub, the name the project gives, and the host it says it runs on. Two projects called
`web` on two machines are two projects, and addresses carry the id (`/p/p1a2b3c4d5e6/a/coder-1`).

A project registers with a **link code**. An operator makes one, and so does anybody with room in their
[project quota](hub-access.md#project-quotas): **Link a project** in Settings, Links shows it once, with the commands to run.
The project presents it with `egzo project register`, and is then approved, owned by whoever made the code, with no further step. From then on
its key is all it takes.

- A code registers one project, for 15 minutes, and can be cancelled until then. The hub keeps only its hash, and it is 128 random bits.
- **Whoever holds a live code gets in**, and nobody sees the project's name or host first. Hand a code over as you would a password. The audit
  log says who made it, and which project used it.
- A code made by someone who was disabled is worth nothing, and one that finds its maker's quota used up is refused until there is room.
- A link with neither a known key nor a good code hears it is not registered, and is closed. The hub stores nothing for it and keeps no
  connection: reaching the endpoint gets a stranger nothing.

**Settings, Links** lists every registration and its owners, and the codes still good, for operators; a person with a quota sees their own codes
there. An owner can [unlink](hub-access.md#roles) their project from its Members tab. **Revoke** takes a project off the hub: what runs over
its link ends and its link is refused from then on, while the project keeps running on its machine. **Forget** removes a project that was
revoked, so a new code can register it again, as a new project with no roles. Removing `hub:` and running `up`, or `egzo project down`, has the
hub forget a project that is linked too, with its roles, tasks and network history; the audit log stays. A project that was revoked cannot do
that for itself: only an operator can forget it.

**A project offline.** When its link goes away, the project shows as **offline since** that time, with what the hub last heard of it, until
the link dials again. Nothing can be done on it meanwhile. The terminals that ran over the link end, while the agents' sessions go on, and a
task that was running is **interrupted**: the hub cannot know how it ended, so look at the agent. After a restart of the hub, a project that
is offline shows only its name and host until it connects, since what it pushed is kept in memory.

## Reaching it

The UI is meant for a private network (a LAN, Tailscale, a tunnel), not the open internet. Projects must reach the hub too, which can be from
anywhere, but only the link endpoint has to be reachable for that: `EGZO_LINK_LISTEN` gives it a listener of its own, which serves registered
keys and links that present a code and no page of the hub, and `EGZO_LINK_URL` says the address projects name for it. Publish that port in
`compose.yaml`, on its own host name, and keep the UI where it is. A link proves its key for that exact address, so `hub:` in the project file
must say what the hub is told (a link that does not is logged as refused, saying which address it should name).

One address may have 8 handshakes going at once and 20 failed attempts a minute (a proof that does not verify, an unknown key, a bad code), then
it hears `429` until the minute is over. Projects that are registered and connected count for nothing, so many of them behind one NAT are fine.
Behind a reverse proxy, set `EGZO_TRUSTED_PROXIES` or every project counts as the proxy's address.

When a reverse proxy is in front, set `EGZO_PUBLIC_URL` and `EGZO_TRUSTED_PROXIES` so the redirects and cookies use the right address and
scheme. The link is a long WebSocket: Caddy passes it as it is, and nginx needs the upgrade, or the link reports `HTTP 426`:

```nginx
location /link/v1 {
    proxy_pass http://hub:8080;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_read_timeout 1h;
}
```

Over https the cookies are `Secure` and named `__Host-egzo_session` and `__Host-egzo_login`, which a browser takes from the hub's own host
only, so another host of the same site cannot plant one. Over plain http they are `egzo_session` and `egzo_login`.

## Signing in

`admin`, the starter account, signs in with a password. For a team, connect your OpenID Connect provider (Keycloak, Authentik,
Google, Entra ID...): people sign in there, with an invite, which is how anyone gets an account. See [access](hub-access.md).

## The terminal

Whoever may operate an agent can open its terminal (**Attach**); there is no read-only view. One person holds it at a time, and the
agent page says who. Opening it while somebody else does offers **Take over**, which ends their terminal ("taken over by ...") and
gives it to you ("Take it back" does the same the other way). Anyone who may open it may take it over. The terminal is sized to
the pane that shows it.

You can have several terminals open at once, across projects. They are listed in the sidebar, stay connected while
you browse the pages, come back after a reload (the agent's session lives on its machine), and can be shown one at a time, side by
side, or four to a screen. Closing one detaches; the agent keeps running. Up to 12 can be open.

Keys inside a terminal belong to the agent, with one exception: **Ctrl+]** is the leader, and the key after it is the hub's
(`1`-`9` go to a terminal, `s` changes the layout, `f` is full screen, `d` closes, `?` lists them all). The browser keeps a few
chords for itself (Ctrl+W, Ctrl+T, Ctrl+N): a page cannot take them, so they act on the browser tab and never reach the agent. On
pages, `:` or Ctrl+K opens the command menu; single-key shortcuts can be turned off in the shortcut reference.

### Known issue: the CLI

The hub only knows the terminals it opened. `egzo agent attach` goes straight to the agent's session and the hub neither sees it nor can
end it. Attaching with the CLI to an agent that is open in the hub is risky: the session has one screen size, so each side's window
resize changes it for the other, and what both type reaches the program. Avoid it. Nothing in the hub prevents it.

## Network

When an agent cannot reach something, the **Network** tab of its page (and of the project page, for every agent, also in the
sidebar) says what the proxy refused, why, and how to fix it. An agent that was refused a host in the last hour shows **N denied** in the agents list.

- **Denied** groups what the proxy refused or failed at by agent, host and reason, with how many times and when it was last
  seen. Wrong proxy credentials are grouped by the name they claimed, shown with `?`; **How to fix** says where the latest one
  came from. **How to fix** says what the reason means and what to change: for a host that is not in the profile, the YAML to add to
  that profile in `egzo.yaml`, then `egzo project up`. The hub never changes the project file. When several hosts under one
  domain were refused, it says which wildcard covers them. A group is marked **fixed** once the same agent gets through to the
  host, and opens again at the next refusal.
- **Connections** are the latest the hub heard, one row per connection: the host, how it was handled (tunnel, inject, inspect,
  intercept), its requests, the decision and the traffic. A green pill names each service whose credential went with its
  requests (a key: injected; arrows: a placeholder swapped), and a dashed one counts the requests that went without it,
  outside the service's `paths`. The steps show the same for each request, with why. A row opens on its steps, with the address it came from and what the
  client says it is (`curl/8.11.1`, Chrome's user agent, an ssh client's version). The agent is shown only when the proxy
  credentials proved it; a connection with wrong ones shows the name it claimed, marked with `?`. Filters keep the refused ones,
  a host or an agent.

A user sees the network of the agents they may operate; admins see every agent's, and the refusals the proxy could not
tie to an agent (wrong credentials). An agent whose name comes back is another instance and starts with an empty history.

The denials are kept in the hub's database for 30 days after they were last seen, at most 500 per project. The connections are
the hub's latest 1000 events of the project, in memory; further back, `egzo proxy log` reads the proxy's own log, as far as the
engine keeps it. The schema of the audit trail is in [sandboxing agents](sandboxing.md#see-what-happened).

## Ports the hub serves

A project's template [declares](project-file.md#ports) the ports its agents may serve, and an agent opens one for the hub with `expose(name, hub)`. Only
`http` ports can be opened for a hub, and the hub learns of them from the project's status feed. A port the agent opened with `where` set to `local` is
a port on the project's machine: the hub never serves it, and the project's link refuses to forward to it. The preview addresses, the sign-in they need
and the panel that lists them are in [roadmap/agent-ports.md](../roadmap/agent-ports.md); they are not all built yet.

## How the hub keeps up

The hub does not poll. Each project's link pushes what changed, on one outbound connection, and the pages read what the hub already holds:

- **The project.** The link follows its engine's events and pushes the whole project when a container of it is made, started, stopped or
  removed, within a fraction of a second, and when its templates change, within a minute.
- **The status feed.** The control sidecar of every project writes the agents' status to its log, as JSON lines on standard output (a line
  when something changes, and one every 30 seconds). The link follows that log and pushes each line, and what the sidebar, Home and the
  project pages show an agent doing (working, idle, blocked, waiting on a question, its status line) is the newest one.
- **The audit trail.** The proxy of every project writes its audit trail to its log the same way, and the link pushes it for the **Network**
  tab. A link that connects again sends it from the time of the last event the hub handled.
- **A link retries** with a delay that grows from one second to a minute, and so do its feeds.
- **What a link says is data.** The hub keeps names egzo could make and bounded sizes, and holds back a link that pushes too fast. A project
  speaks for itself only.
- **A page that polls costs the project nothing.** What goes to the link is the timeline, log and spec tabs, only while they are open, and
  what people ask for.

This needs a log the link can stream back from its engine, which is the engine's default driver (egzo chooses none): `json-file`, `local` or
`journald` on Docker, `k8s-file` or `journald` on Podman. Run `egzo system check` on the machine. If the status cannot be read the
project page says why, and its agents show as **running**: the hub never says "starting" for a state it does not know. An old
control sidecar (made by an egzo before the status log) writes no status; run `egzo project up` again. See
[the known issue](../known-issues/container-logging-follows-the-engine-default.md).

## Settings

All settings are environment variables. The compose file passes the common ones from `.env`; add any other to its
`environment:`.

| Variable | Default | |
|---|---|---|
| `EGZO_PUBLIC_URL` | required, unless `EGZO_LINK_URL` is set | the address people use, and the one projects name unless `EGZO_LINK_URL` says another. Unset, the address people use is derived from the request |
| `EGZO_LINK_URL` | `EGZO_PUBLIC_URL` | the address projects name in `hub:`, exactly: a link's proof is for it |
| `EGZO_LINK_LISTEN` | none | a listener for the link endpoint alone, like `:8443`; the link endpoint then leaves the UI's port |
| `EGZO_ADMIN_PASSWORD` | none | the starter account's password, saved at every start; unset, one is made and printed once |
| `EGZO_LOCAL_LOGIN` | `true` | the starter account's password sign-in; `false` needs single sign-on |
| `EGZO_OIDC_ISSUER` | none | single sign-on: the provider's issuer, exactly |
| `EGZO_OIDC_CLIENT_ID`, `EGZO_OIDC_CLIENT_SECRET` | | the client registered at the provider; the secret is optional (a public client uses PKCE) |
| `EGZO_OIDC_SCOPES` | `openid profile email` | replaces the scopes (some providers want `offline_access`, some refuse scopes they do not know) |
| `EGZO_OIDC_NAME` | `SSO` | what the sign-in button says |
| `EGZO_HUB_LISTEN` | `:8080` | the port of the UI, and of the link endpoint unless it has its own |
| `EGZO_HUB_DATA` | `/data` | the SQLite database (a volume) |
| `EGZO_TRUSTED_PROXIES` | none | addresses or CIDRs of the reverse proxy whose `X-Forwarded-Host` and `X-Forwarded-Proto` are believed |

The hub does not terminate TLS. Behind a proxy set `EGZO_TRUSTED_PROXIES`, so the login redirect and the cookies use the right
address and scheme.

The database records its schema version. Before 1.0 there are no migrations: a hub that finds a database made by another version
refuses to start and says to remove `hub.db` (users and roles are then set up again, and every project registers again).

## Development

The hub is its own Go module (`hub/`); it imports core's protocol package, `internal/hublink`, and core never imports it. It does not link the
Docker client: `make hub-lint` fails if it does.

```sh
make hub-test           # go vet and go test -race
make hub-lint           # staticcheck and govulncheck
make hub-web            # the web app (Svelte, Vite) into internal/web/dist, which the binary embeds (node, or docker)
make hub-web-test       # its type check and unit tests
make hub-build          # bin/egzo-hub
make hub-e2e            # the browser tests (Playwright, in docker), against the hub with fake projects linked to it
make hub-stack          # the whole stack for real: image, Dex as single sign-on (a test fixture, e2e/), a project that links itself, a real terminal
make hub-image
```

Tests: ordinary Go unit tests (the seam is faked in `internal/links/fake`, and the link endpoint meets core's real link, the login provider in `internal/oidctest`; the
API is tested through HTTP in `internal/web`), Vitest for the web app's logic (`web/src/lib/*.test.ts`), Playwright tests of the app
in `e2e/`, and the integration and stack runs above. Core's specs-first rule does not apply here.

### The web app

`web/` is a Svelte 5 + TypeScript single-page app built by Vite. The hub serves it for every address that is not the API, so the
app's own routes (`/e/lab/p/web/a/coder-1`) survive a reload. The design it implements is in `roadmap/egzo-design/` (tokens,
components and the clickable prototype). The API it talks to is described in [the hub API](hub-api.md).

```sh
cd hub/web && npm ci && npm run dev     # the app on :5173, proxying /api and the login to a hub on :8080 (EGZO_HUB=...)
```

There is no node on some machines; `make hub-web` and `make hub-web-test` run in docker when `npm` is missing.
