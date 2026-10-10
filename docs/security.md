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
- **No secrets in the agent.** Credentials sit in the proxy, loaded by `up`, and are added to requests to the hosts
  of declared services. They are not in the agent's environment, in labels, in `docker inspect` or in any image.
  The harness gets a placeholder value.
- **Hardened containers:** all capabilities dropped, `no-new-privileges`, memory and process limits, bounded logs,
  sidecars as a non-root uid with a read-only root. Agents run as your uid and gid, not as root. The engine socket is
  never mounted into an agent.
- **Optional gVisor** (`runtime: runsc`) as a stronger kernel boundary, on engines that can apply it.
- **Per-agent identity.** Each instance has its own token, used for the proxy and the control sidecar. A token only
  lets an agent act as itself, and the proxy applies that agent's profile, not another's.
- **Nothing from your host is mounted** unless the file says so, and only the paths it names.

## What it does not protect against

Be clear about these before running agents on something you care about.

- **Whatever the agent can reach, it can use.** An agent cannot read the API key, but it can make requests that
  carry it, to the hosts of that service, for as long as it runs. A key with broad rights is used with broad rights.
  Give agents tokens with the narrowest scope the work needs.
- **Data can leave through allowed hosts.** The agent can push your repository to any host you allow (including
  `github.com` with an injected token, to a repository it chooses). `allow: ["*"]` makes this wide open. Keep
  profiles tight, and keep secrets and private data out of workspaces the agent does not need.
- **Prompt injection.** Text the agent reads (an issue, a web page, a file) can steer it. The sandbox limits what
  steering can do; it does not stop the agent from being steered.
- **The workspace is writable.** Agents can delete or corrupt what you mount read-write. Use `:ro`, git, and the
  `clone` mode (the default) so one agent cannot damage another's `.git`. `worktree` mode shares the base `.git`.
- **Host paths** you mount are as exposed as you make them.
- **The engine is trusted.** Anyone who can reach the engine socket controls the containers. On a rootful engine
  that is root-equivalent. Prefer a rootless engine.
- **A kernel escape** from a normal container is possible in principle; gVisor reduces that risk, it does not remove it.
- **Podman cannot apply a runtime**, so there is no gVisor there, and egzo refuses to pretend.

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
P-256). The CA private key lives in a volume mounted only into the proxy; agents receive just the certificate, as part
of their CA bundle. All other allowed hosts are plain tunnels where the proxy sees only the host name, which it checks
against the TLS server name. `egzo ca rotate` replaces the CA. Clients that pin certificates cannot be intercepted and
so cannot have credentials injected.

## Messages

A message cannot forge its origin: the terminal only receives a fixed line with a random id, and the text is fetched
by a tool that checks the id belongs to the caller. Ids are unguessable and checked, and there are limits against
loops (20 open requests per recipient, 30 messages a minute, 8 levels deep).

## Reporting a problem

`specs/` includes containment specs that assume the agent is hostile and check the intended limits: no egress except
through the proxy, no secrets in the agent, no host access beyond what the file mounts. They test those limits; they
are not a proof. If you find a way around one, that is a bug worth reporting.
