# Sandboxing agents: egress policy

Agents have no route to the internet. Their only way out is the egzo **proxy**, and the proxy applies the **egress
profile** the agent's template names. Anything not allowed is denied. This page is how to write profiles; [the security model](security.md) says what the sandbox does not cover.

What the proxy does for an agent, in order: it checks the proxy credential the agent was given at spawn (an agent
cannot use another's profile), allows only port 443, checks the host against the profile, refuses private and
internal addresses, and then either tunnels the connection or, for hosts with a credential, terminates TLS with the
project's own CA, adds the credential and forwards. Every decision is logged.

## A profile

```yaml
egress:
  default:                                  # what an agent gets if it names no profile
    allow: [platform.claude.com, "*.pypi.org", files.pythonhosted.org]
    services:
      anthropic: main/ANTHROPIC_API_KEY     # built-in service + its secret
      github: main/GITHUB_TOKEN
```

- `allow`: host names or `*.domain` globs the agent may reach with **no** credential.
- `services`: hosts that get a credential injected. A built-in service (`anthropic`, `github`, `anthropic-oauth`)
  needs only the secret: `vault/SECRET`.
- The agent's reach is `allow` plus the hosts of its services. Without a `default` profile, an agent that names none
  can reach nothing.

[Troubleshooting](troubleshooting.md) covers a request that is denied. `egzo up` warns if an agent's profile cannot reach its harness's provider host.

## Built-in services

| service | hosts | injected |
|---|---|---|
| `anthropic` | `api.anthropic.com` | `x-api-key: <secret>` |
| `anthropic-oauth` | `api.anthropic.com` | `Authorization: Bearer <token>` (a Claude subscription token from `claude setup-token`) |
| `github` | `github.com`, `api.github.com` | `Authorization: Bearer <secret>` |

Binding a token to the wrong one of the two Anthropic services is refused at `up`. Claude Code also needs
`platform.claude.com` in `allow`; it needs no credential.

## Your own services

An object defines a service: hosts, and how to inject the secret.

```yaml
egress:
  default:
    services:
      deploy-api:
        hosts: [api.deploy.example.com]
        inject: { header: Authorization, value: "Bearer {secret}" }   # without value, the raw secret is the header
        secret: main/DEPLOY_TOKEN
```

A service without `inject` is a plain allowlist entry. Add `inspect: true` to also log the request paths of a host
(off by default; it makes the proxy terminate TLS for that host).

## Several profiles

```yaml
egress:
  default:
    allow: [platform.claude.com]
    services:
      anthropic: main/ANTHROPIC_API_KEY
  operator:
    extend: default                 # one parent; allow lists are unioned
    services:
      github: main/GITHUB_TOKEN     # a string only sets the secret of an inherited or built-in service
      deploy-api: { hosts: [api.deploy.example.com], secret: main/DEPLOY_TOKEN,
                    inject: { header: X-Api-Key } }

agents:
  coder:    { harness: claude-code, egress: default }
  releaser: { harness: claude-code, egress: operator }
```

A child can add to what it inherits and cannot remove anything. Cycles and unknown parents are errors, as is a
service that uses a secret that no vault declares. Two services in one profile cannot share a host with different
injection.

## Full internet

```yaml
egress:
  open:
    allow: ["*"]
```

Still through the proxy, so still audited, still limited to port 443 and public addresses, and credentials are
injected only for the hosts of declared services. Use it for a trusted template, not by default.

## What the sandbox does not allow

- **Raw TCP, UDP, ssh, ping, DNS to other servers.** Only HTTPS through the proxy. A git remote must be HTTPS. ssh
  from an agent is an idea in [`roadmap/ssh-access.md`](../roadmap/ssh-access.md), not built.
- **Pinned-certificate clients.** A host with an injected credential is intercepted with the project CA; a client that
  pins certificates will fail on it.
- **Agent-to-agent traffic.** Agents never share a network. They talk only through the control sidecar (`egzo send`,
  the `message` tool).

## See what happened

```console
$ egzo proxy rules       # what each agent may reach, and which credentials go where
$ egzo proxy log         # the audit trail, one JSON event per line (allow, deny, reason)
$ egzo config            # every profile fully resolved
```

Changing a profile and running `egzo up` reloads the proxy with the new policy; running instances keep their
binding by profile name. After a proxy restart, `egzo restart proxy` (or `egzo up`) loads the policy again; until
then the proxy answers 503. `egzo ca rotate` issues a new project CA and restarts the agents.
