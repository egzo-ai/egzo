# Roadmap: hub settings, with engines and users

**Status.** Open, not designed. Today the hub has one operator, set at the first login, and no way to change that
(see [hub.md](hub.md), "Permissions"). Engines are the operator's page; there is no list of users at all.

The need: a hub-level **Settings** area, outside any project, with two views:

- **Engines**: what the Engines page is today (add, edit, remove, confirm an ssh host key), moved under Settings.
- **Users**: every person who has signed in, and the hub-level access they hold. The first hub-level access is the
  **operator** status, so the operator can hand it to someone else, or take it back.

## What changes in the model

- **An operator bypasses the grant system.** No grants are written for them. In every access check an operator counts as **owner of
  every project** the hub has scanned, on every engine. This replaces `claimOrphans`: nothing gives an operator owner rows any more,
  and the one special case in the model ("the operator owns the orphans") becomes "an operator is an implicit owner". It supersedes
  the earlier plan, in [hub.md](hub.md), of materializing the access.
- **More than one operator.** `users.is_operator` stays, but it is no longer "exactly one": `Store.Operator()` (one row) and the
  "very first user" rule are the places that assume it. The first login still becomes the operator. With bypass, granting or
  revoking operator takes effect at once, with no grants to create or clean up.
- **Only for projects that exist.** The bypass applies to a project in a scan (`access()` in `hub.go`), not to any
  `(engine, project)` string. An unknown project is still not found.
- **Visibility grows.** Today an operator has no access to a project that has a real owner. With the bypass an operator reaches every
  project, other people's agents and terminals included. Operators can already add engines, so the trust is the same, but say it in the
  docs. Every action stays in the audit log under the operator's name.
- **CLI projects.** Agents spawned from the CLI belong to "the operator" (`rbac.Operator`, the actor label `operator`). `rbac.Can`
  already reads that as `subject.IsOperator`, so with several operators it means any of them; the UI shows "operator (CLI)".
- **Roles on projects are unchanged** and stay on the project's Members tab. Only an operator manages engines and users.

## Ideas

- **Protect the last operator.** An operator cannot remove or lower their own status when they are the last one, and nobody
  can lower the last one. Same shape as "an owner cannot remove or lower themselves" in `rbac.CanAssign`.
- **Members tab shows operators, locked.** Operators are listed on every project in a disabled shade with an "operator" tag, so it is
  clear their access is not a role and cannot be removed there. `CanAssign` / `SetRole` refuse to change an operator's access, and the
  list of people to add leaves operators out.
- **Home lists every project for an operator**, tagged "operator" where a role would be.
- **Users list.** Name, email, first and last sign-in, operator or not, and the projects they hold a role on. Search, since this
  list can be long. The user list has to say what a person *can* see, because that is the question an operator asks first.
- **Grant and revoke operator** with a confirmation, and an audit entry.
- **Existing grants.** A person who is made operator may already hold explicit grants, and the first operator holds the owner rows
  that orphan claiming wrote. They are harmless while the bypass is on, but they come back as real access if the person is demoted.
  Decide: promoting deletes their explicit grants, or demoting keeps what they had. Existing hubs also hold claimed rows for the
  operator already; a migration could remove them.
- **Remove a user.** `Store.DeleteUser` exists (grants and sessions go with it, and it refuses the operator) and nothing calls
  it. Decide whether to expose it, and what happens to the agents they created (their actor label stays, as a name that no longer resolves).
- **Hub-level audit.** The audit log is per project (`engine`, `project` columns). Operator changes, user removals and engine
  changes have no project: show them in a hub-level log under Settings, or in the same table with an empty project.
- **Break-glass.** The operator's OIDC identity can be lost (provider account deleted, email changed), and then nobody can
  become operator. A start-up setting, such as `EGZO_OPERATOR_IDENTITY`, that grants operator to that identity, or a hub
  subcommand that does it, would close the gap. Without it the fix is editing the database.
- **Navigation.** A Settings entry in the sidebar, for operators only, with Engines and Users as its two views; `/settings/engines`
  and `/settings/users`. The existing `/engines` route redirects. In the design, Settings is a settings-style page, not a primary view.
- **Later hub-level things** (auth settings, defaults, retention) would land in the same area. Do not build them now.

## Tests

- Go unit tests: the bypass (an operator reaches every scanned project and no unknown one), the last-operator rule, an operator's access
  cannot be changed from Members, who may call each endpoint, the audit entries. The orphan-claiming tests go with `claimOrphans`.
- API: `GET /api/v1/users`, `PUT /api/v1/users/{id}` (operator on or off), under the same JSON and Origin rules as the rest; documented in `hub/docs/api.md`.
- Playwright: open Settings, grant operator, check the other person now sees Engines, check the last operator cannot be removed.
