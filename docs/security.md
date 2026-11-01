# The security model

Egzo's bet: **the sandbox, not the harness's permission prompts, is the security boundary.** Agents run in bypass
mode and do not stop to ask. This page says what that does and does not protect. The mechanics are in [sandboxing](sandboxing.md), [workspaces](workspaces.md)
and [messaging](messaging.md).

## What egzo enforces

- **A container per agent**, on a network of its own with no route to the outside. It can reach only the proxy and
  the control sidecar. Agents cannot reach each other or your LAN.
- **The only way out is the proxy**, which allows only HTTPS to the hosts of the agent's egress profile, refuses
  private and internal addresses (cloud metadata, your network, other agents' networks), limits connections and
  logs every decision. Nothing is allowed unless the profile says so.
- **No secrets in the agent.** Credentials sit in the proxy, loaded by `egzo project up`, and are added to requests to the hosts
  of declared services. They are not in the agent's environment, in labels, in `docker inspect` or in any image.
  The harness gets a placeholder value.
- **ssh without the key.** An agent can ssh only to the servers its egress profile names, through the proxy, which holds the key and checks a pinned
  host key; forwarding is refused and every session and command is audited ([ssh to servers](sandboxing.md#ssh-to-servers)).
- **Hardened containers:** all capabilities dropped, `no-new-privileges`, memory and process limits,
  sidecars as a non-root uid with a read-only root. Agents run as your uid and gid, not as root. The engine socket is
  never mounted into an agent.
- **Optional gVisor** as a stronger kernel boundary: egzo runs containers under the engine's default runtime, so make `runsc` the engine's default.
- **Per-agent identity.** Each instance has its own token, used for the proxy and the control sidecar. A token only
  lets an agent act as itself, and the proxy applies that agent's profile, not another's.
- **Nothing from your host is mounted.** Workspaces are engine volumes, so no directory of yours is exposed to an agent;
  files go in and out only with `egzo workspace put` and `get`.

## What it does not protect against

Be clear about these before running agents on something you care about.

- **Whatever the agent can reach, it can use.** An agent cannot read the API key, but it can make requests that
  carry it, to the hosts of that service, for as long as it runs. A key with broad rights is used with broad rights.
  Give agents tokens with the narrowest scope the work needs.
- **What the agent does on a server it can ssh to.** The key is safe; the account is not. Give it a low-privilege account or a forced command.
- **Data can leave through allowed hosts.** The agent can push your repository to any host you allow (including
  `github.com` with an injected token, to a repository it chooses). `allow: ["*"]` makes this wide open. Keep
  profiles tight, and keep secrets and private data out of workspaces the agent does not need.
- **Prompt injection.** Text the agent reads (an issue, a web page, a file) can steer it. The sandbox limits what
  steering can do; it does not stop the agent from being steered.
- **The workspace is writable.** Agents can delete or corrupt what you mount read-write. Use `:ro` for directories and git for
  code. Every agent has its own worktree.
- **The engine is trusted.** Anyone who can reach the engine socket controls the containers. On a rootful engine
  that is root-equivalent. Prefer a rootless engine. [The hub](hub.md) holds no engine access: it can do what the link below lets it, on the projects that linked.
- **What you open with `egzo agent port-forward` is the agent's.** The command reaches into the sandbox through the engine and the agent cannot
  use it to reach out, but the page your browser loads was written by the agent. Open it in a browser profile that is not signed in to anything
  you care about, on a local port no other site of yours uses (cookies ignore ports).
- **An exposed port is what the agent says it is.** [`expose`](messaging.md#ports-an-agent-offers) publishes nothing and changes nothing in the sandbox: it
  records a claim, and its name and protocol are the agent's words. The list goes to everyone who can read the project's status, and each agent sees the
  others' with `agents`.
- **A preview is the agent's code in somebody's browser.** That is why it never runs on the hub's origin: each [preview](hub.md#previews) has a host of its own,
  on a domain the docs ask to keep apart, it reaches nothing of the hub's session, and a person signs in to each one separately. An app the agent wrote can still
  show whatever it likes to whoever opens it, so a viewer should trust an address as far as they trust the agent behind it. The hub can reach only ports an
  agent exposed, and the link enforces that.
- **The link holds the engine.** A project that names a [hub](managing-projects.md#linking-to-a-hub) runs a sidecar with the engine's socket
  mounted: on a rootful engine it is root-equivalent on its host, like the CLI. Agents do not reach it: it is on a network of its own. A hub that
  is taken over can do what its verbs do on the projects linked to it, which includes attaching to their agents: link only to a hub you trust. A
  project registers with a single-use [link code](hub.md#linking-a-project) that a person allowed to connect a project makes, so nobody who merely reaches the link endpoint can
  put anything on a hub, and whoever holds a live code can: treat one as a password.
- **A `build:` Dockerfile is trusted.** `egzo project up` builds it on the engine, outside the egress proxy, as you would by
  hand ([images](images.md#let-egzo-build-it)).
- **A kernel escape** from a normal container is possible in principle; gVisor reduces that risk, it does not remove it.

## How secret injection works

A secret is read by the CLI on your machine, handed to the proxy, and kept in the proxy's memory. The agent gets
either nothing (`inject`: the proxy sets a header on requests to a service's hosts, replacing whatever the agent
sent) or a placeholder (`placeholder`: a random string the proxy swaps for the secret in requests to the service's
hosts). Both apply to the hosts of the service and no others.

What this gives you is that **the agent cannot read the secret**. A prompt-injected agent cannot print it, write it
into a file, or send it to a host you did not allow, because it never has it.

What it does not give you is that the agent **cannot use** the secret. While it runs, it can make any request that
the service's hosts accept, and the proxy adds the credential. The secret is hidden, the permission is not: a token
that may delete repositories lets the agent delete repositories. Limit what a secret can do at its source (a
fine-grained token, a dedicated account, the least privilege the work needs); egzo limits where it can be sent and who
can see it.

## The proxy and TLS

For hosts with an injected credential, the proxy terminates TLS with a CA that egzo generates per project (ECDSA
P-256). The CA private key lives in a volume mounted only into the proxy, encrypted with a key derived from the
project's [secret key](project-file.md#the-secret-key) (XChaCha20-Poly1305), like the proxy's ssh host key: a copy or a
backup of the volume is useless without the vault secret. The proxy is given the secret key with the policy and keeps it
in memory only. The tokens of an [OAuth sign-in](sandboxing.md#oauth-services) are kept the same way, and the agent only
ever holds stand-ins that work through the proxy alone. Agents receive just the certificate, as part of their CA bundle. All other allowed hosts are plain tunnels where the proxy sees only the host name, which it checks
against the TLS server name. A client that sends Encrypted Client Hello (every browser does) is not tunneled, because the
extension can hide the real server name and the proxy cannot tell a harmless one from one that does: the proxy terminates
TLS for it with the project CA instead, checks the site's real certificate itself, and refuses any request for another
host. egzo has no setting that accepts a hello it cannot read or a certificate it cannot verify. `egzo project rotate-ca` replaces
the CA. Clients that pin certificates cannot be intercepted, so they cannot have credentials injected, and a pinning
client that sends Encrypted Client Hello does not work either.

A secret binds to exact host names, never to a wildcard. A service with a wildcard host cannot have an `inject` or a
`placeholder`, because the secret would go to every subdomain, whoever controls it.

## Messages

A message cannot forge its origin: the terminal only receives a fixed line with a random id, and the text is fetched
by a tool that checks the id belongs to the caller. Ids are unguessable and checked, and there are limits against
loops (20 open requests per recipient, 30 messages a minute, 8 levels deep).

## Reporting a problem

`specs/` includes containment specs that assume the agent is hostile and check the intended limits: no egress except
through the proxy, no secrets in the agent, no host access. They test those limits; they
are not a proof. If you find a way around one, that is a bug worth reporting.
