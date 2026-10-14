# The hub API

The web app is a client of this API and nothing else: everything under `/api/v1`, JSON in and out. The shapes are in
`hub/internal/web/api.go` and, as the front end sees them, `hub/web/src/lib/types.ts`. Times are RFC 3339.

**Authentication.** The browser logs in with OIDC (`/login/start`, then `/auth/callback`, which set the session cookie and redirect
back; a failed login redirects to `/login?error=...`). Every API call without a valid session answers 401. A call that changes
anything must send `Content-Type: application/json` and the `Origin` of the hub's own address (a browser does); anything else
is refused (415, 403).

**Errors** are `{"code": "...", "message": "..."}` with a real status: `unauthorized` (401), `forbidden` (403), `not_found` (404; also
for a project the person has no role on, so its existence is not leaked), `bad_request`, `invalid` (422), `expired` (410),
`operation_failed` and `engine_error` (502).

| Call | Answer |
|---|---|
| `GET /session` | `{user: {id, name, email, isOperator}, engines: [{name, up, err}]}` |
| `POST /logout` | `{ok}` |
| `GET /projects` | `{projects: [{engine, name, role, up, agents: [{name, template, running, activity, status, waiting}]}], unreachable: [{name, err}]}` from the last scan; it feeds the home page and the sidebar and calls no engine |
| `GET /e/{engine}/p/{project}` | the project, read live: `{engine, name, role, up, templates, services, agents: [{name, template, state, running, activity, status, waiting, stale, creator, createdAt, canOperate, holder}], canSpawn, canManage, canAudit}` |
| `GET .../events?agent=&limit=` | `{events: [{time, type, agent, actor, text}]}`, newest first |
| `POST .../spawn` `{template, name}` | `{agent}` |
| `GET .../spec` | `{text}` |
| `GET .../members` (owner) | `{members: [{user, role}], candidates: [user], roles, me}` |
| `POST .../members` `{user, role}` (owner) | `{ok}`; `role` is `user`, `administrator`, `owner` or `none` |
| `GET .../audit` (administrator) | `{entries: [{at, actor, action, agent, detail, allowed}]}` |
| `GET .../a/{agent}` | `{project, agent}` (the project without its other agents) |
| `POST .../a/{agent}/{start,stop,rm}` | `{agent, verb}` |
| `GET .../a/{agent}/logs` | `{text}` |
| `GET .../a/{agent}/terminal[?takeover=1]` | the terminal websocket (below) |
| `GET /engines` (operator) | `{engines: [{name, host, up, err}]}` |
| `POST /engines` `{name, host, sshUser, ca, cert, key, sshKey}` (operator) | `{saved}`, or `{pending: {name, host, fingerprint, token}}` when an ssh host key must be confirmed first. Credentials are never returned |
| `POST /engines/confirm` `{token}` (operator) | `{saved}`; the token lives ten minutes and works once |
| `DELETE /engines/{name}` (operator) | `{removed}` |

`.../` is `/e/{engine}/p/{project}`.

## The terminal websocket

Binary frames are the terminal's bytes in both directions. Text frames are JSON control messages. Up: `{"type":"resize","cols":N,"rows":N}`
(the first one sizes the session; the hub waits two seconds for it). Down: `{"type":"busy","holder":"..."}` (somebody else holds it,
the socket closes), `{"type":"taken","holder":"..."}` (somebody took it over, the socket closes), `{"type":"error","message":"..."}`.
The Origin must be the hub's own.

## Everything else

`GET /healthz` is `ok`. Any other `GET` returns the app's `index.html`; any other path under `/api/v1/` is a 404 in JSON.
