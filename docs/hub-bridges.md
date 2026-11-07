# Chat bridges

**Status.** Built (#145):
- the hub's side: the bridge API, the outbox, account linking and the checks on every message;
- the first bridge, [`egzo-discord`](#discord).

Rich messages, where an agent writes for the platform, are next (#177).

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

## Linking an account

A person links their chat account to their hub user once:
1. They run the bridge's register command (on Discord, `/egzo register`).
2. The bridge asks the hub for a code and shows the person an address, only to them: `<hub>/chat-link#<code>`.
3. They open it, sign in if they are not, and confirm which account it is.

The code is after the `#`, so a browser sends it to no server and no log holds it. It is good for 15 minutes and once.

From then on, the hub knows whose that account is. A bridge names the **chat account** a message comes from, never a hub user, so it
can only speak for accounts people linked. A person sees their chat accounts on their **Account** page, and removes one there; an operator sees everyone's under Settings, Users. Linking
the same chat account again moves it to whoever confirms last.

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

- the chat account is linked, and its user exists, is not disabled, and holds a role on the project;
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

The stream also carries the changes a bridge acts on, with `type` other than `message`:

| `type` | Fields | |
|---|---|---|
| `agent.removed` | `project`, `agent` | the agent is gone: a bridge drops it from its places |
| `project.removed` | `project` | the project left the hub: all its agents with it |
| `account.linked`, `account.unlinked` | `user`, `bridge`, `account` | read the accounts again |
| `user.disabled` | `user` | the hub refuses what that user says from now on |
| `role.changed` | `project`, `user` | what that user may chat with changed |

**`POST /bridge/v1/messages`** is what a person says. It answers `{"id"}`, or `{"error": "not delivered"}`:

```json
{"project": "p1a2b3", "agent": "coder-1", "account": "<the platform's id for the person>", "text": "deploy it", "key": "<the platform's message id>", "interrupt": false}
{"project": "p1a2b3", "agent": "coder-1", "account": "<the platform's id for the person>", "text": "staging", "answer": "<the question's id>", "outcome": "done"}
```

The other calls:

| Call | | Answers |
|---|---|---|
| `POST /bridge/v1/codes` `{account, name}` | a link code for a person's account | `{code, url, expires}`: show the person `url`, only to them |
| `GET /bridge/v1/accounts` | the accounts linked through this bridge: who `user:<id>` is on the platform | `[{account, name, user, username}]` |
| `GET /bridge/v1/agents?account=` | the agents the person may chat with | `[{project, project_name, agent, template, state, admin}]`, 403 for an account nobody linked |
| `POST /bridge/v1/may-add` `{account, project, agent}` | may the person add the agent to a place: an admin of its project | `{allowed}` |

## Trust

A bridge is trusted: it reads everything agents and users say to each other, and it names the chat account a message comes from.
- **What a compromised bridge can do:** speak as any person who linked an account through it, to any agent they may chat with.
- **What it cannot do:** reach links, secrets, or anything else of the hub.

Bridges are few, and run next to the hub.

## Discord

`egzo-discord` is in the hub's image, run as its own service with the entrypoint `/egzo-discord`. It reaches Discord on its gateway, so it
needs no address of its own, and the hub on the bridge API.

### Setting it up

1. **Make the bot.** At [discord.com/developers/applications](https://discord.com/developers/applications):
   - **New Application**.
   - On **Bot**, **Reset Token** and keep it; turn on **Message Content Intent**; leave **Public Bot** off.
   - A bot that reaches fewer than 10,000 people turns the intent on itself, with no review.
2. **Add it to your server.** On **OAuth2**, **URL Generator**:
   - **Scopes:** `bot` and `applications.commands`.
   - **Bot permissions:** View Channels, Send Messages, Send Messages in Threads, Create Public Threads, Read Message History, Embed Links,
     Attach Files and Manage Roles.

   Open the address it makes. For a private channel, give the bot View Channel there.
3. **Run it.** In the hub's `.env`, set `EGZO_DISCORD_TOKEN`, a secret of yours as `EGZO_DISCORD_BRIDGE_SECRET` (`openssl rand -hex 32`), and
   `EGZO_BRIDGES=discord:<that secret>`. Uncomment the `discord` service and its volume in `compose.yaml`, then `docker compose up -d`.

| Variable | Default | |
|---|---|---|
| `EGZO_DISCORD_TOKEN` | required | the bot's token |
| `EGZO_BRIDGE_SECRET` | required | the bridge's secret, as `EGZO_BRIDGES` on the hub names it |
| `EGZO_HUB_URL` | `http://hub:8080` | where the hub is, on the compose network |
| `EGZO_BRIDGE_DATA` | `/data` | its state, SQLite, on a volume |

### Using it

- **`/egzo register`** links your Discord account to your hub user (see [Linking an account](#linking-an-account)).
- **`/egzo agent add agent:<project/agent> [name:]`**, in a channel, adds an agent there.
  - It needs Discord's Manage Roles, and admin on the agent's project on the hub.
  - The bot makes a role for the agent, named after it unless `name` says otherwise. Nobody holds the role, so a mention pings nobody.
  - The first channel an agent is added to is its **home**.
- **`/egzo agent remove`** takes an agent out of the channel; `everywhere:` takes it out of the server and deletes its role.
  **`/egzo agent list`** shows the agents of the channel.
- **Talk to an agent:** mention its role in a channel it is in, reply to what it posted, or write in a thread started from one of its
  messages. **`/egzo say agent: text: [interrupt:]`** works anywhere. The message is yours: the hub checks it as it would your terminal.
- **What an agent says to you** comes as a reply where you spoke to it, or in its home channel, mentioning you:
  - a question shows its choices as buttons;
  - a reply to it answers it.

**What the bot posts**
- Discord's Components V2 messages, a colour for each kind.
- Mentions are suppressed: an agent's text pings nobody, and a message pings only the person it is for.
- A text longer than one message is split at a paragraph, a line or a word, and a code block cut in two is closed and opened again.
  Past four messages it is attached as a file.

**When things go away**
- A channel the bot can no longer view (Discord hides it as `___hidden___`) loses its agents.
- An agent removed on the hub loses its roles.
- A role somebody deletes by hand takes its agent out of that server.

A message the hub refuses gets **⚠️ Not delivered**, and never why: the hub's audit log says why. Someone who has not linked an account is
told to run `/egzo register`.
