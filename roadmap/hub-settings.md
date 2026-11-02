# Roadmap: hub settings

**Status.** Built, for operators, with the later items below still open. [docs/hub-access.md](../docs/hub-access.md#operators) says how operators work.
An operator oversees every project and grants themselves a role to go further, so nothing here bypasses roles. Engines are gone: projects
[link themselves](hub-link.md).

## What is built

- **Settings**, an area outside any project: **Links**, **Invites**, **Users**, the hub's **Audit** log, and **Account**, which everyone has.
- **Several operators, the last one protected.** `Store.SetOperator` counts and writes in one transaction and returns `ErrLastOperator`, and it is the only
  way to change the flag.
- **Disabling a user** replaced removing one ([hub-login.md](hub-login.md), "Disabling users"): the audit log, messages and agent labels name people by
  username. An operator cannot be disabled until they stop being one.
- **Break-glass**: `EGZO_LOCAL_LOGIN=true` and `EGZO_ADMIN_PASSWORD`, and a restart, bring `admin` back as an operator. What comes back is oversight: `admin`
  reaches a project's terminals by granting itself a role, on the record.

## Later

- **Remove a user.** Open: whether to offer it at all, and what happens to the agents they created (the actor label stays, as a name that no longer resolves).
- **A richer users list.** The projects each person holds a role on (it answers "what can this person see?"), search for a long list.
- **Later hub-level things** (defaults, retention) would land in the same area. Do not build them now.
