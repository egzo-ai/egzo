# Roadmap: hub settings, with engines and users

**Status.** The first slice is built (see "What was built"); "Later" is open. Before it, the hub had one operator, set at the
first login, and no way to change that (see [hub.md](hub.md), "Permissions"); engines were the operator's page and there was no
list of users.

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

## First

One slice, in this order. Each step leaves the hub working.

1. **The bypass.** `access()` and `Projects()` treat an operator as owner of every scanned project; `claimOrphans` and `ClaimIfOrphan` go.
   The visible part belongs here too: **operators listed on every project's Members tab, locked** (a disabled shade, an "operator" tag,
   `CanAssign` / `SetRole` refuse to change them, they are left out of the list of people to add), and **Home lists every project for an
   operator**, tagged "operator" where a role would be.
   *Existing grants:* leave them alone, no migration. The owner rows that claiming wrote stay as ordinary rows. They do nothing while the
   person is an operator and show up as real roles on the Members tab if they are demoted, where an owner can remove them.
2. **Several operators, and the last one is protected.** `Store.Operator()` and the "very first user" rule stop assuming one. The last
   operator cannot be demoted, by themselves or by anyone, in the same shape as "an owner cannot remove or lower themselves".
3. **Users, and grant or revoke operator.** `GET /api/v1/users` (name, email, operator or not) and `PUT /api/v1/users/{id}` to set
   operator, operator only, with a confirmation in the UI. Each change is an audit entry with no engine and no project (the columns
   are already empty-able), written now so that nothing is lost before there is a view of it.
4. **Settings in the sidebar.** For operators only, with **Engines** (moved, `/settings/engines`, and `/engines` redirects) and
   **Users** (`/settings/users`). In the design, Settings is a settings-style page, not a primary view.

Tests and docs go with each step (below). `hub.md`, `hub/docs/README.md` and `hub/docs/api.md` change when step 1 lands, because they
describe the claiming that step 1 removes.

## Later

- **Break-glass.** The operator's OIDC identity can be lost (provider account deleted, email changed), and then nobody can become
  operator. A start-up setting such as `EGZO_OPERATOR_IDENTITY`, or a hub subcommand, would close it. Not now: the hub is early, the fix
  is a one-line edit of the database (`users.is_operator`), and a break-glass needs its own documentation and care. Write the edit
  down in the operator docs in step 3 so that the way out exists on paper.
- **A hub-level audit view.** Step 3 writes the entries; showing them (a log under Settings, or the project table with an empty
  project) is separate.
- **Remove a user.** `Store.DeleteUser` exists (grants and sessions go with it, and it refuses the operator) and nothing calls it.
  Open: whether to expose it, and what happens to the agents they created (the actor label stays, as a name that no longer resolves).
- **A richer users list.** First and last sign-in, the projects each person holds a role on (it answers "what can this person see?"),
  search for a long list.
- **Later hub-level things** (auth settings, defaults, retention) would land in the same area. Do not build them now.

## What was built

- **The bypass.** `Hub.access` returns owner for an operator on any project of a known engine; when the engine's last scan listed
  its projects, the project must be on it. `Projects` lists every scanned project for an operator, with `operator: true`. The
  claiming is gone: `claimOrphans`, `ClaimIfOrphan` and `HasOwner` are removed, and nothing writes grants for an operator. An
  engine that did not answer is not "not found" for an operator: the call reports the engine.
- **Locked Members rows.** `Members` lists the operators after the grants, as owner with `operator` set (a grant left on a person
  who became an operator is not listed twice); `Candidates` leaves them out; `SetRole` refuses an operator target with
  `ErrOperatorAccess` before any other rule. Home and the project page say "operator" where a role would be.
- **Several operators, the last one protected.** `Store.SetOperator` counts and writes in one transaction and returns
  `ErrLastOperator` (409 `conflict`); it is the only way to change the flag.
- **Users.** `GET /users`, `PUT /users/{id}` `{isOperator}`, operators only; the audit entries `operator.grant` and
  `operator.revoke` (refusals too) have no engine and no project. A change that changes nothing is not audited.
- **Settings.** An operator-only Settings entry in the sidebar, with Engines (moved; `/engines` and `/settings` land on
  `/settings/engines`) and Users. Keys: `g e`, `g u`; both are in the command menu.
- The recovery from a lost operator sign-in is written in `hub/docs/README.md`; the break-glass setting is not built.

## Tests

- Go unit tests: the bypass (an operator reaches every scanned project and no unknown one), the last-operator rule, an operator's access
  cannot be changed from Members, who may call each endpoint, the audit entries. The orphan-claiming tests go with `claimOrphans`.
- API: `GET /api/v1/users`, `PUT /api/v1/users/{id}` (operator on or off), under the same JSON and Origin rules as the rest; documented in `hub/docs/api.md`.
- Playwright: open Settings, grant operator, check the other person now sees Engines, check the last operator cannot be removed.
