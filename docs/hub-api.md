# The hub API

The web app is a client of this API and nothing else: everything under `/api/v1`, JSON in and out. The shapes are in
`hub/internal/web/api.go` and, as the front end sees them, `hub/web/src/lib/types.ts`. Times are RFC 3339.

**Authentication.** Two ways in, both ending in the same session cookie (12 hours):

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
for a project the person has no role on, so its existence is not leaked; an operator gets it for a project that is not on a scan), `bad_request`, `invalid` (422), `expired` (410),
`conflict` (409), `operation_failed` and `engine_error` (502).

| Call | Answer |
|---|---|
| `GET /session` | `{user, engines: [{name, up, err}], identities: [identity], hasPassword, sso, issuer}`. A `user` is `{id, username, name, email, emailVerified, picture, isOperator, disabled}`: the username is the hub's; name, email and picture (https only, fetched by the browser) come from the provider, for display. An `identity` is `{id, issuer, email, name, current, lastLoginAt}`; `current` is false for an account of a former provider |
| `POST /logout` | `{ok}`; the session's terminals close |
| `POST /me/link` | `{url}`: send the browser there to sign in at the provider; the callback links that account to the signed-in user and returns to `/settings/account?linked=1` (or `?error=`). Refused when the account belongs to somebody else, or when the user already has one at that provider |
| `DELETE /me/identities/{id}` | `{ok}`; the sessions made with it end. 409 `conflict` when it is the last way to sign in |
| `GET /projects` | `{projects: [{engine, name, role, operator, feed, up, agents: [{name, template, running, activity, status, waiting}]}], unreachable: [{name, err}]}` from what the hub holds (the engines' last listing, kept current by their events, and the agents' status from the control sidecars' logs); it feeds the home page and the sidebar and calls no engine |
| `GET /e/{engine}/p/{project}` | the project, from what the hub holds (the templates from the control sidecar, kept for five minutes): `{engine, name, role, operator, feed, up, templates, services, spawns, agents: [{name, template, state, running, activity, status, waiting, stale, creator, createdAt, canOperate, holder, denied}], canSpawn, canManage, canAudit, ignored}`. `ignored` (for whoever may audit) lists the containers that carry the project's label with labels egzo never writes, and why: they are not shown as part of it. `denied` is how many hosts the proxy refused the agent in the last hour and that it has not reached since (0 for an agent the user may not operate) |
| `GET .../events?agent=&limit=` | `{events: [{time, type, agent, actor, text}]}`, newest first |
| `POST .../spawn` `{template, name}` | `202 {spawn: {id, template, name, creator, state, startedAt}}`, at once: the hub runs the spawn in the background (it goes on if the request goes away), one at a time per project and in order. The project lists it in `spawns` (`state` `spawning`, then `ready` with `agent`, or `failed` with `error`) for ten minutes after it ended. A hub that restarts forgets the spawns it was running |
| `GET .../spec` | `{text}` |
| `GET .../members` (owner) | `{members: [{user, role, operator}], candidates: [user], roles, me}`; an operator is listed last, as `owner` with `operator: true` |
| `POST .../members` `{user, role}` (owner) | `{ok}`; `role` is `user`, `administrator`, `owner` or `none`. Refused (403) for an operator: their access is not a role |
| `GET .../audit` (administrator) | `{entries: [{at, actor, action, agent, detail, allowed}]}` |
| `GET .../network?agent=` | what the project's proxy recorded, or one agent's (the instance of that name that exists now; 404 for none): `{feed, denials: [{instance, agent, creator, protocol, host, server, code, event, decision, profile, text, count, first, last, fixed}], events: [{time, conn, protocol, event, decision, mode, agent, instance, profile, claimedAgent, client, userAgent, host, server, method, path, status, command, reason: {code, text}, bytesUp, bytesDown, durationMs}], ignored: {count, last}}`. `ignored`, for an administrator and only when there are any, counts the lines of the proxy's log that are not events the proxy writes (a name or a text out of its rules), and says why the last one was not: they are not shown or counted. `denials` are the groups, the most recently seen first, `fixed` null while open; `events` are the hub's latest, newest first. A user gets the agents they may operate; an administrator every agent, and the refusals with no `instance`. The codes are those of [the proxy's audit trail](../roadmap/proxy-audit.md) |
| `GET .../a/{agent}` | `{project, agent}` (the project without its other agents) |
| `POST .../a/{agent}/{start,stop,rm}` | `{agent, verb}` |
| `GET .../a/{agent}/logs` | `{text}` |
| `GET .../a/{agent}/terminal[?takeover=1]` | the terminal websocket (below) |
| `GET /engines` (operator) | `{engines: [{name, host, up, err}]}` |
| `POST /engines` `{name, host, sshUser, ca, cert, key, sshKey}` (operator) | `{saved}`, or `{pending: {name, host, fingerprint, token}}` when an ssh host key must be confirmed first. Credentials are never returned |
| `POST /engines/confirm` `{token}` (operator) | `{saved}`; the token lives ten minutes and works once |
| `DELETE /engines/{name}` (operator) | `{removed}` |
| `GET /users` (operator) | `{users: [user + {createdAt, lastLoginAt, hasPassword, identities}], me}`, disabled users included |
| `PUT /users/{id}` `{isOperator}` or `{disabled}` (operator) | `{ok}`. 409 `conflict` when it would remove the last operator, disable an operator, or make a disabled user an operator; setting the value a person already has changes nothing. Disabling ends the person's sessions and closes their terminals |
| `DELETE /users/{id}/identities/{identity}` (operator) | `{ok}`: frees an account at a provider from a **disabled** user, so it can be linked to another (409 otherwise) |

`.../` is `/e/{engine}/p/{project}`.

**`activity` and `feed`.** An agent's `activity` is `starting`, `idle`, `working`, `blocked`, `stopped` (its container is not running), or
`unknown` (it runs, and the hub cannot read what it is doing). A project's `feed` is `{state, message}` and says how far `activity`,
`status` and `waiting` can be trusted: `live` (status lines are arriving), `connecting` (nothing yet), `stale` (it went quiet for
longer than three heartbeats) or `unavailable` (the log cannot be followed, or the sidecar writes no status), with the reason in
`message`. Unless the feed is `live`, every running agent is `unknown`.

## The terminal websocket

Binary frames are the terminal's bytes in both directions. Text frames are JSON control messages. Up: `{"type":"resize","cols":N,"rows":N}`
(the first one sizes the session; the hub waits two seconds for it). Down: `{"type":"busy","holder":"..."}` (somebody else holds it,
the socket closes), `{"type":"taken","holder":"..."}` (somebody took it over, the socket closes), `{"type":"error","message":"..."}`.
The Origin must be the hub's own.

## Everything else

`GET /healthz` is `ok`. Any other `GET` returns the app's `index.html`; any other path under `/api/v1/` is a 404 in JSON.
