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
  - **A message to a person goes to one bridge:** the one of the chat account they linked last. Later, the hub may let people choose. A
    person with no linked account gets nothing, and the agent is told.
  - The project's cursor moves past the event in the same step. An event the hub could not keep holds the feed, and the hub tries it
    again: it never goes past one. Events with nothing for the outbox move the cursor at most every ten seconds; reading them again costs
    nothing.
  - The outbox keeps a message for a week. What agents say to each other, or to the operator, never leaves the project.
- **Not delivered.** The agent is told by a notice from egzo, which says why and how to send it again ([undelivered](#not-delivered)).
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
- **A message to a person** comes only to the bridge that carries it, with `bridge` and `account`, the person's account there. A message
  from a person comes to every bridge.

```json
{"seq": 42, "time": "...", "project": "p1a2b3", "agent": "coder-1", "id": "m...", "from": "agent:coder-1", "to": "user:u7",
 "kind": "question", "re": "m...", "text": "staging or production?", "choices": ["staging", "production"], "bridge": "discord",
 "account": "<the platform's id for the person>"}
```

`kind` is `request`, `question`, `resolution` (with `outcome`) or `update`.

The stream also carries the changes a bridge acts on, with `type` other than `message`:

| `type` | Fields | |
|---|---|---|
| `agent.removed` | `project`, `agent` | the agent left its project: removed, or being re-created under the same name (see `presence`) |
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
| `POST /bridge/v1/presence` `{agents: [{project, agent}]}` | is each agent still in its project, at most 1000 per call | `{agents: [{project, agent, state}]}` |
| `POST /bridge/v1/messages/{id}/undelivered` `{account, code, detail}` | a message the bridge was given did not reach the person ([below](#not-delivered)) | 204; 404 for a message not given to this bridge for that account; 503 when the agent cannot be told now |

**Presence.** Re-creating an agent (`egzo agent rm`, then `egzo agent spawn` with the same name) removes it, so `agent.removed` cannot
tell a removal from a re-creation. A bridge keeps an agent's places for a while after it left, and asks `presence` from time to time:
- `present`: it is in its project. A re-created agent is the same name in the same project, and gets its places back.
- `absent`: it is not, or its project is gone from the hub (revoked or unlinked). Also how a bridge finds a removal it never heard of,
  such as one while the hub was down.
- `unknown`: the hub has not heard from the project since it started. Ask again later; do not take it as gone.

The hub answers from what each project's link last pushed, and asks no link.

## Not delivered

Bridges are the only way people read agents' messages, so a message that does not reach its person is told to the agent: a notice from
egzo that names the message, says why, and how to send it again. The retry is the agent's. A question or request is closed as `failed`;
a result stays; an update gets no notice ([messages](../DESIGN.md#the-model)).

The codes say what happened, never on which platform: every bridge uses the same. The hub writes what the agent reads, with the person's
hub name and the platform's name from the bridge's (`discord` is Discord). `detail`, the platform's own words, goes to the audit log
(`agent.undelivered`), never to the agent. A code the hub does not know is read as `refused`.

| Code | Set by | What happened | The agent reads |
|---|---|---|---|
| `no-account` | hub | the person linked no chat account | joe has no chat account linked to the hub |
| `no-access` | hub | the person holds no role on the project, or is disabled | joe may not hear from this project |
| `person-unreachable` | bridge | the platform will not let the bot reach the person: DMs off, blocked, gone | joe cannot be reached on Discord |
| `no-place` | bridge | the bridge has nowhere to put it | there is nowhere to reach joe on Discord |
| `refused` | bridge | the platform refused it for good: permissions, a deleted channel, too long | Discord refused it |
| `unavailable` | bridge | the platform did not take it within the bridge's retry window | Discord did not take it in time |

### What every bridge does

- **Where an agent's message to a person goes:**
  1. a reply to the message it is about, if the person was part of it (they wrote it, or it was posted to them);
  2. otherwise where the person last talked to that agent, in a place the agent is still in;
  3. otherwise the platform's direct channel to the person;
  4. otherwise, report `no-place`.

  Never to another place just because the agent is there too: the person may not be.
- **Answers:** only the person a question is for answers it.
- **Failures:** report one within a bounded time, or never; silence is delivered. Retry what may succeed (the network, the platform's own
  errors) idempotently, if the platform allows it, and never what it refused for good: platforms punish that.

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
- **`/egzo agent remove`** takes an agent out of the channel; `everywhere:` takes it out of the server and deletes its role.
  **`/egzo agent list`** shows the agents of the channel, and when the role of one that left its project goes.
- **Talk to an agent:** mention its role in a channel it is in, reply to what it posted, or write in a thread started from one of its
  messages. **`/egzo say agent: text: [interrupt:]`** works anywhere. The message is yours: the hub checks it as it would your terminal.
- **What an agent says to you** mentions you, and comes:
  - as a reply to the message it is about, when you were part of it: one you wrote, or one the bot posted to you;
  - otherwise (it is about a message from elsewhere, such as the task an agent was started with) in the channel where you last talked to
    that agent, among those it is still in. It never goes to another server just because the agent is there too;
  - otherwise by **DM**: the first time an agent reaches you, such as one started unattended with a task from the hub. Reply to its
    messages in the DM to talk to it. If you turned off DMs from server members, the bot cannot reach you there, and tries again 10
    minutes later at the earliest.

  A question shows its choices as buttons, and only you can click them; a reply to it answers it.

**What the bot posts**
- Discord's Components V2 messages, a colour for each kind.
- Mentions are suppressed: an agent's text pings nobody, and a message pings only the person it is for.
- A text longer than one message is split at a paragraph, a line or a word, and a code block cut in two is closed and opened again.
  Past four messages it is attached as a file.

**When things go away**
- A channel the bot can no longer view (Discord hides it as `___hidden___`) loses its agents.
- **An agent removed from its project keeps its roles and channels for 7 days.**
  - Re-creating an agent with the same name in the same project within those 7 days reconnects the existing role: the same mention, the
    same channels. Nobody has to `/egzo agent add` it again.
  - Until then, a message to it is answered that it is not running, and until when its role stays.
  - After 7 days, its roles are deleted. `/egzo agent remove everywhere:` deletes them at once.
  - The bot asks the hub every 10 minutes, so it also finds an agent removed while the hub was down. While the hub cannot say (the
    project is offline, or the hub just started), the roles stay.
- A project that leaves the hub (revoked or unlinked) takes its agents' roles with it, at once.
- A role somebody deletes by hand takes its agent out of that server.

**When Discord does not take a post**
- Every post carries a nonce from the message's id: a post Discord may have taken (a lost connection, a 500, a 504) is sent again and
  shows once.
- What may pass later is tried again for 2 minutes, inside the few minutes Discord remembers a nonce. Messages after it wait.
- What Discord refuses for good (no access, a deleted channel, a person who does not take DMs) is never tried again: those refusals count
  toward the invalid requests Discord bans an IP for. The bridge tells the hub, which tells the agent ([not delivered](#not-delivered)).

A message the hub refuses gets **⚠️ Not delivered**, and never why: the hub's audit log says why. Someone who has not linked an account is
told to run `/egzo register`.
