# The hub API

The web app is a client of this API and nothing else: everything under `/api/v1`, JSON in and out. The shapes are in
`hub/internal/web/api.go` and, as the front end sees them, `hub/web/src/lib/types.ts`. Times are RFC 3339.

**Authentication.** Two ways in, both ending in the same session cookie (12 hours; `__Host-egzo_session` over https,
`egzo_session` over plain http):

- **The starter account's password**: `POST /auth/login` `{username, password}` (outside `/api/v1`) answers `{ok}` and sets the
  cookie. Every failure is 401 `bad_login` "Wrong username or password." After three failures in a row an account waits, longer
  after each further one (429 `too_many`, with `Retry-After`). 404 while local login is off.
- **Single sign-on**: `/login/start?next=`, then `/auth/callback`, which sets the cookie and redirects to `next`; a failed sign-in
  redirects to `/login?error=...`.

`GET /signin` needs no session and says which ways exist: `{local, sso, redirect}` (`sso` is the button's name, "" without single
sign-on; `redirect` is the callback address to register at the provider). Every other API call without a valid session answers
401. A call that changes anything must send `Content-Type: application/json` and the `Origin` of the hub's own address (a browser
does); anything else is refused (415, 403).

**Errors** are `{"code": "...", "message": "..."}` with a real status: `unauthorized` (401), `forbidden` (403), `not_found` (404; also
for a project the person has no role on, so its existence is not leaked; and for a project that is not approved), `bad_request`, `invalid` (422), `expired` (410),
`conflict`, `busy` and `offline` (409), and `link_error` (502).

| Call | Answer |
|---|---|
| `GET /session` | `{user, identities: [identity], hasPassword, sso, issuer, hub}`, `hub` being the address projects name in `hub:`. A `user` is `{id, username, name, email, emailVerified, picture, isOperator, disabled}`: the username is the hub's; name, email and picture (https only, fetched by the browser) come from the provider, for display. An `identity` is `{id, issuer, email, name, current, lastLoginAt}`; `current` is false for an account of a former provider |
| `POST /logout` | `{ok}`; the session's terminals close |
| `POST /me/link` | `{url}`: send the browser there to sign in at the provider; the callback links that account to the signed-in user and returns to `/settings/account?linked=1` (or `?error=`). Refused when the account belongs to somebody else, or when the user already has one at that provider |
| `DELETE /me/identities/{id}` | `{ok}`; the sessions made with it end. 409 `conflict` when it is the last way to sign in |
| `GET /projects` | `{projects: [{id, name, host, role, operator, offlineSince, feed, up, agents: [{name, template, running, activity, status, waiting}]}]}` from what the links pushed (the agents' status from the control sidecars' logs); it feeds the home page and the sidebar and calls no engine |
| `GET /p/{id}` | the project, from what its link pushed: `{id, name, host, role, operator, offlineSince, feed, up, templates, services, agents: [{name, template, state, running, activity, status, waiting, stale, creator, createdAt, canOperate, holder, denied, task}], canSpawn, canManage, canAudit, ignored}`. `task` is what the hub does on the agent (a task, below), or the failure of what it did that nobody dismissed; absent when neither. An agent with a `task` and an empty `state` is one the engine does not have: a spawn that runs or failed, or a removal that is ending. `ignored` (for whoever may audit) lists the containers that carry the project's label with labels egzo never writes, and why: they are not shown as part of it. `denied` is how many hosts the proxy refused the agent in the last hour and that it has not reached since (0 for an agent the user may not operate) |
| `GET .../events?agent=&limit=` | `{events: [{time, type, agent, actor, text}]}`, newest first |
| `POST .../spawn` `{template, name}` | `202 {task}`, at once: the hub runs the spawn as a task, in the background (it goes on if the request goes away), and the agent is in the project from then on. `name` is required (400 without); 409 `conflict` when an agent has it, `busy` when a task runs on that name. Spawns and removals of a project run one at a time, in order |
| `GET .../tasks?agent=` | the project's activity, or one agent's: `{tasks: [task]}`, the latest 100, newest first. A task is `{id, verb, agent, template, creator, state, step, error, startedAt, endedAt, canDismiss}`: `verb` is `spawn`, `start`, `stop` or `rm`; `state` is `running`, `done`, `failed`, or `interrupted` (it ran when the hub stopped); `step` is what it does now, or did last (`create container ...`); `creator` is who asked. The hub keeps tasks for 30 days, and a failure nobody dismissed until it is |
| `GET .../tasks/{id}` | `{task}`: whoever asked for it follows it until it ends |
| `DELETE .../tasks/{id}` | takes an ended task off its agent (a failure, once read); the activity keeps it. For who may operate its agent (`403` otherwise), `409` while it runs, `404` once it is off. Recorded in the audit log as `task.dismiss`. A new task on the agent takes the failure off by itself |
| `GET .../spec` | `{text}` |
| `GET .../members` (owner) | `{members: [{user, role, operator}], candidates: [user], roles, me}`; an operator is listed last, as `owner` with `operator: true` |
| `POST .../members` `{user, role}` (owner) | `{ok}`; `role` is `user`, `administrator`, `owner` or `none`. Refused (403) for an operator: their access is not a role |
| `GET .../audit` (administrator) | `{entries: [{at, actor, action, agent, detail, allowed}]}` |
| `GET .../network?agent=` | what the project's proxy recorded, or one agent's (the instance of that name that exists now; 404 for none): `{feed, denials: [{instance, agent, claimedAgent, client, userAgent, creator, protocol, host, server, code, event, decision, profile, text, count, first, last, fixed}], events: [{time, conn, protocol, event, decision, mode, agent, instance, profile, claimedAgent, client, userAgent, host, server, method, path, status, command, reason: {code, text}, bytesUp, bytesDown, durationMs}], ignored: {count, last}}`. `ignored`, for an administrator and only when there are any, counts the lines of the proxy's log that are not events the proxy writes (a name or a text out of its rules), and says why the last one was not: they are not shown or counted. `denials` are the groups, the most recently seen first, `fixed` null while open; `events` are the hub's latest, newest first. A user gets the agents they may operate; an administrator every agent, and the refusals with no `instance`. The codes are those of [the proxy's audit trail](../roadmap/proxy-audit.md) |
| `GET .../a/{agent}` | `{project, agent}` (the project without its other agents) |
| `POST .../a/{agent}/{start,stop,rm}` | `202 {task}`, at once, like a spawn: the agent shows the task while it runs. 409 `busy` while another task runs on the agent, saying which and who asked |
| `GET .../a/{agent}/logs` | `{text}` |
| `GET .../a/{agent}/terminal[?takeover=1]` | the terminal websocket (below) |
| `GET /links` (operator) | `{links: [link]}`: every registration, the newest first. A link is `{id, name, host, release, templates, state, owner, message, createdAt, offlineSince}`; `state` is `approved` or `revoked`, and `offlineSince` is null while it is connected |
| `POST /links/{id}/revoke` `{message}` (operator) | `{ok}`: the project leaves the hub, what runs over its link ends, its link is refused and shows `message` |
| `DELETE /links/{id}` (operator) | `{ok}`: forgets a project that was revoked. 409 `conflict` for one that is not |
| `POST /links/codes` (operator) | `{id, code, expiresAt, hub}`: a link code, for `egzo project register`. This is the only time `code` is seen. `hub` is the address projects name in `hub:` |
| `GET /links/codes` (operator) | `{codes: [{id, createdBy, createdAt, expiresAt}]}`: the codes still good, the newest first, without their values |
| `DELETE /links/codes/{id}` (operator) | `{ok}`: cancels a code. 404 for one that was used, cancelled or has expired |
| `GET /users` (operator) | `{users: [user + {createdAt, lastLoginAt, hasPassword, identities}], me}`, disabled users included |
| `PUT /users/{id}` `{isOperator}` or `{disabled}` (operator) | `{ok}`. 409 `conflict` when it would remove the last operator, disable an operator, or make a disabled user an operator; setting the value a person already has changes nothing. Disabling ends the person's sessions and closes their terminals |
| `DELETE /users/{id}/identities/{identity}` (operator) | `{ok}`: frees an account at a provider from a **disabled** user, so it can be linked to another (409 otherwise) |

`.../` is `/p/{id}`. What needs the project's link answers 409 `offline` while the project is offline; the pages keep working from what the
hub last heard, and `offlineSince` says since when.

**`activity` and `feed`.** An agent's `activity` is `starting`, `idle`, `working`, `blocked`, `stopped` (its container is not running), or
`unknown` (it runs, and the hub cannot read what it is doing). A project's `feed` is `{state, message}` and says how far `activity`,
`status` and `waiting` can be trusted: `live` (status lines are arriving), `connecting` (nothing yet), `stale` (it went quiet for
longer than three heartbeats) `unavailable` (the sidecar is not running, or writes no status), or `offline` (the link is not connected), with the reason in
`message`. Unless the feed is `live`, every running agent is `unknown`.

## The terminal websocket

Binary frames are the terminal's bytes in both directions. Text frames are JSON control messages. Up: `{"type":"resize","cols":N,"rows":N}`
(the first one sizes the session; the hub waits two seconds for it). Down: `{"type":"busy","holder":"..."}` (somebody else holds it,
the socket closes), `{"type":"taken","holder":"..."}` (somebody took it over, the socket closes), `{"type":"error","message":"..."}`.
The Origin must be the hub's own.

## Everything else

`GET /healthz` is `ok`. Any other `GET` returns the app's `index.html`; any other path under `/api/v1/` is a 404 in JSON.
