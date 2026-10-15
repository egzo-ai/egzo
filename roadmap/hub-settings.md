# Roadmap: hub settings, with engines and users

**Status.** Open, not designed. Today the hub has one operator, set at the first login, and no way to change that
(see [hub.md](hub.md), "Permissions"). Engines are the operator's page; there is no list of users at all.

The need: a hub-level **Settings** area, outside any project, with two views:

- **Engines**: what the Engines page is today (add, edit, remove, confirm an ssh host key), moved under Settings.
- **Users**: every person who has signed in, and the hub-level access they hold. The first hub-level access is the
  **operator** status, so the operator can hand it to someone else, or take it back.

## What changes in the model

- **More than one operator.** The flag `users.is_operator` stays, but it is no longer "exactly one": `Store.Operator()` (one row)
  and the "very first user" rule are the places that assume it. The first login still becomes the operator.
- **Orphan projects.** Today the operator is given owner on a project nobody owns. With several operators, decide who: all of
  them, or the first one. (All of them is the simplest rule that keeps "an operator can always reach every project".)
- **CLI projects.** Agents spawned from the CLI belong to "the operator" (`rbac.Operator`, the actor label `operator`). With
  several operators this label means any of them; the UI already shows it as "operator (CLI)".
- **Who may.** Only an operator manages engines and users. Roles on projects are unchanged and stay on the project's Members tab.

## Ideas

- **Protect the last operator.** An operator cannot remove or lower their own status when they are the last one, and nobody
  can lower the last one. Same shape as "an owner cannot remove or lower themselves" in `rbac.CanAssign`.
- **Users list.** Name, email, first and last sign-in, operator or not, and the projects they hold a role on. Search, since this
  list can be long. The user list has to say what a person *can* see, because that is the question an operator asks first.
- **Grant and revoke operator** with a confirmation, and an audit entry.
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

- Go unit tests: the last-operator rule, several operators and orphan claiming, who may call each endpoint, the audit entries.
- API: `GET /api/v1/users`, `PUT /api/v1/users/{id}` (operator on or off), under the same JSON and Origin rules as the rest; documented in `hub/docs/api.md`.
- Playwright: open Settings, grant operator, check the other person now sees Engines, check the last operator cannot be removed.
