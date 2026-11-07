# Chat bridges

**Status.** Being built (#145). The hub's side is here: the bridge API, the outbox and the checks on every message. Still to come:
- the first bridge, `egzo-discord` (#152);
- linking an account, "may add", and change events (#151);
- rich messages (#177).

Until a bridge exists, a person cannot reach any of this.

A **bridge** connects a chat platform to the hub's agents. People talk to an agent from a channel, and answer what it asks them there.
The hub is not a chat: it never shows a message's text ([the hub](hub.md#messages)). It is the broker: it keeps, relays and checks.

## Running one

Each bridge is its own service in the hub's `compose.yaml`, commented out until you want it. To add one:
1. Uncomment its service.
2. Give it a secret, in the hub's `EGZO_BRIDGES` and in the bridge's own settings.
3. Run `docker compose up -d`.

| Variable | Default | |
|---|---|---|
| `EGZO_BRIDGES` | none | the bridges that may connect, `name:secret` pairs separated by commas, like `discord:<32+ characters>`. None: no bridge API |

The bridge API is served on the hub's port, under `/bridge/v1`. A bridge sends `Authorization: Bearer <its secret>`, and the secret names
the bridge. The API takes no cookies and no other credentials.

## How a message travels

- **Out.** A project's link pushes the control sidecar's events to the hub ([the link](reference/hub-link.md#messages)).
  - A message with a hub user at one end goes into the hub's **outbox**: an agent asking a user a question, answering their request, or
    telling them how it goes, or what a user said. A message for a user who holds no role on the project, or is disabled, stays out:
    an agent addresses anybody, and only the project's people hear from it.
  - The project's cursor moves past the event in the same step. An event the hub could not keep holds the feed, and the hub tries it
    again: it never goes past one. Events with nothing for the outbox move the cursor at most every ten seconds; reading them again costs
    nothing.
  - The outbox keeps a message for a week. What agents say to each other, or to the operator, never leaves the project.
- **In.** A bridge posts what a user says, with the hub user id the person is linked to. The hub checks it, then hands it to the project's
  link with `send` (or `resolve`, for an answer) as `user:<id>`.

## The checks

The bridge vets the platform's side: the person is in the channel, linked, and the agent was added there. The hub checks the person, on
every message:

- the user exists and is not disabled, and holds a role on the project;
- they may **chat** with this agent: a `user` with the agents they spawned, an `admin` or `owner` with any agent of the project. It is
  the rule of the terminal ([roles](hub-access.md#roles)): chat gives nobody more than they have. A viewer never talks to an agent;
- the project is online. Nothing queues: a message for a project that is offline is dropped.

A message that fails is **dropped and audited**: `agent.message` or `agent.answer`, refused, with the reason and the bridge's name. The
bridge gets `{"error": "not delivered"}` (503 when the project is offline, 403 otherwise) and never the reason, and the person is told
nothing. A message that goes through is audited too, with its id and never its text.

A message carries `key`, the platform's id for it. The same key from the same user is the same message, so a bridge that retries after a
dropped connection sends nothing twice. An answer names the question it answers instead.

## The API

**`GET /bridge/v1/messages?after=<seq>`** is a stream of JSON lines, one per message, that stays open for what comes next.
- **Where it starts.**
  - `after` is the `seq` of the last message the bridge handled, so it resumes there and loses nothing.
  - `-1` (or none) means only what comes next. `0` is the whole outbox, a week of every project's conversations: a new bridge starts
    with `-1`.
  - A `seq` past the end means the hub's database was made anew, so the bridge gets everything the outbox has.
- **Keepalives.** A quiet stream sends an empty line every 15 seconds.
- **Every bridge reads everything,** and picks what it relays to which place.

```json
{"seq": 42, "time": "...", "project": "p1a2b3", "agent": "coder-1", "id": "m...", "from": "agent:coder-1", "to": "user:u7",
 "kind": "question", "re": "m...", "text": "staging or production?", "choices": ["staging", "production"]}
```

`kind` is `request`, `question`, `resolution` (with `outcome`) or `update`.

**`POST /bridge/v1/messages`** is what a user says. It answers `{"id"}`, or `{"error": "not delivered"}`:

```json
{"project": "p1a2b3", "agent": "coder-1", "user": "u7", "text": "deploy it", "key": "<the platform's message id>", "interrupt": false}
{"project": "p1a2b3", "agent": "coder-1", "user": "u7", "text": "staging", "answer": "<the question's id>", "outcome": "done"}
```

## Trust

A bridge is trusted: it reads everything agents and users say to each other, and it names the hub user a message comes from.
- **What a compromised bridge can do:** speak as any user, to any agent that user may chat with.
- **What it cannot do:** reach links, secrets, or anything else of the hub.

Bridges are few, and run next to the hub.
