# Roadmap: messaging adapters (NATS, egzo-discord)

**Status.** Planned, not built. Nothing is being built now: this page says how messaging will work on what the hub has become, and what it will need. Split out
of [hub.md](hub.md).

Messaging is out of the web UI. Agents are reached from chat platforms through **adapters**: one per platform (`egzo-discord` first), each its own binary and image
in this repository, an optional service of the compose stack. The adapter is the **external gateway**: it is trusted to vet what comes in from its platform and
decides what goes out. The hub never learns a platform's concepts (guilds, roles, workspaces). Modeled on Scion's Discord plugin
(`inspiration/scion/extras/scion-discord`, studied, not copied): its register flow and question buttons are kept; its project-level channel links, `@all` and single
trust domain are dropped.

Code layout, tests and the hub's model are in hub.md ("Code layout and rules"); adapters live in `hub/cmd/egzo-<platform>/` (`hub/cmd/egzo-discord/` first) and
`hub/internal/`, and are unit-tested, not spec-driven.

## What the link means for messaging

This page used to rest on the operator API and the event stream being in core already, with the hub reaching them through the engine. The hub has no engine now. It
reaches a project only [through its link](hub-link.md), and only with the link's verbs, so messaging needs two additions there. Neither exists yet.

| Needed | Today | To add |
|---|---|---|
| The hub sends a message to an agent | No verb does. Control has `POST /messages`, with an actor | A verb, `send`: agent, text, actor, and an id that makes it idempotent |
| The hub hears what agents say | The feeds are `project`, `status` and `audit`. `events` is a verb, and the timelines ask it every 15 seconds. Control has `GET /events`, a live stream | A feed, `events`, following that stream |

- **The `events` feed also removes the last periodic command**: the timelines read it instead of asking (hub.md, "Not yet").
- **A link that was away loses nothing.** An event has a sequence number, so the feed resumes from the last one the hub has. What an agent said while the project was
  offline reaches `.out` late, and in order.
- **`send` fails while a project is offline.** The link is the only way in, so nothing queues for it: the message is dropped and audited, like one the hub refuses. The
  hub does not hold people's messages for later.
- **It is a new power of the hub over a project.** A hub can already attach to an agent, which is more, so linking to a hub trusts it with this. `hub: { allow: [...] }`
  ([hub-link.md](hub-link.md), "Open questions") would let a project turn it off.
- **Versions.** An older link does not know `send`. That turns off chat for that project and nothing else ([hub-link.md](hub-link.md#versions)).

## Transport: one topic per agent

The hub embeds a **NATS** server (JetStream for durable consumers). To verify: that it embeds cleanly in the hub binary, and JetStream's storage on a volume.

    agent.<project>.<agent>.out    the hub publishes what the agent says: messages, questions, status
    agent.<project>.<agent>.in     adapters publish what users say and answer; the hub forwards it to the agent
    hub.control                    the hub publishes: agent.removed, user.disabled, role.changed

- `<project>` is the id of the registration: two projects named `web` on two machines are two projects, and a project's name can change.
- The **agent is the topic**. The hub is only the relay between an agent (through its link) and its topic. Adapters never see a link.
- Every adapter subscribes to all agents' `.out` and to `hub.control`, and selects what it relays to which place. The hub does not track what is added where. Adapters
  are trusted with all agent traffic.
- Static broker permissions, the same for every adapter: subscribe to `.out` and `hub.control`, publish to `.in`.
- Ids and names are `[a-z0-9_-]`, so they are safe subject tokens.

## The hub API (two calls, from the adapter)

An authenticated HTTP API (`/v1`, a token per adapter) for the two moments an adapter needs the hub:

- **Link an account**: the adapter asks for a code for an external user; the user, signed in on the web UI, confirms it; the adapter polls for the result and gets the
  hub user id. The adapter stores the pair; the hub keeps nothing.
- **May add**: the adapter asks whether hub user U may add agent A to a place (`admin` on its project). Yes or no.

Nothing else goes through the API.

## Who owns what

| Adapter | Hub |
|---|---|
| vets what comes in; decides what goes out | users, roles, RBAC |
| adds agents to places (agent → place) | the second check on every message `.in` |
| links external users to hub users, keeps the mapping | confirms link codes, answers "may add" |
| its own state (SQLite on a volume): roles, webhooks, message → agent map, pending questions | relays agent ↔ topic, publishes lifecycle events |

## Messages

- **Inbound**: the adapter vets the platform side (the sender is in the place, linked, the agent is in that place) and publishes to the agent's `.in` with the hub user
  id, the text and the platform message id. The hub **double-checks** before relaying: the user exists and is not disabled, may talk to this agent, and the agent exists.
  If so it calls `send` with `actor = user:<id>`. If not, the message is **dropped and audited**. There is no reply: external users do not get a refusal, the message
  simply does not go through. The platform message id makes it idempotent.
- **Outbound**: the adapter renders the agent's `.out` for its platform, only for places the agent is in. Answers to a question and interrupts go `.in` under the same check.
- **Stale state**: the adapter reacts to `hub.control` (drop an agent from a place on `agent.removed`, a link on `user.disabled`, update on `role.changed`) and reconciles
  with the hub on reconnect. A stale adapter can still relay output to a place after a user is disabled; the second check on `.in` stops the other direction. Accepted.
- **Capabilities**: an adapter handles what its platform supports (threads, buttons for question choices, rich text, per-agent identity, message length). Hub messages are
  one neutral model: text, choices, attachments.
- **Addresses in a channel.** An agent hands out an address by saying it, so one can land in a channel. A [preview](agent-ports.md) still asks whoever opens it to sign in. A
  [local port](declared-local-ports.md) has no sign-in, so its address reaches everyone in that channel: it is as open as `local_ports.bind` makes it.

## Who decides what

- **The place** (who can see and speak): the platform. A private channel protects an agent: the adapter only receives what it can view.
- **The person**: the hub. **A viewer can never talk to an agent**, in chat or anywhere else: an agent can do a great deal, and a viewer is the web proxy only, often a person
  from outside. Chat needs the `user` role or above. Answering a question and interrupting use the same permission, and the hub checks it on every message.
- **A `user` and another person's agent.** A `user` of the project can talk to its other agents, not only their own. This is a sensitive point: the `user` role operates and
  attaches to its own agents only, so chat reaches agents a terminal does not. It needs careful evaluation before building.

## Trust

A compromised adapter reads every agent's output and can speak as any hub user it names, to any agent that user may talk to. It cannot reach links, secrets or the hub's data
beyond the two API calls. Accepted: adapters are trusted and few. The hub does not record which places an agent is in, so the web UI cannot list or revoke them.

## egzo-discord

One bot per guild. `/egzo register` is the link flow. Agents are added one at a time, as roles:

    /egzo agent add id:<project/agent> [name:<role name>]    # run in the channel; the id autocompletes
    /egzo agent remove [--all]                                       # out of this channel, or out of every channel
    /egzo agent rename|list

- Adding calls the hub's "may add" check, then creates, once per guild, a **role** for the agent (mentionable, no
  members, named after the agent unless `name` is given) and records that the agent is **in this channel**. The
  binding is `role → agent` plus a **set of channels**; the first channel is the **home channel**, where unprompted
  events (a question asked while idle) go. The bot never adopts an existing role, refuses a name it already uses, and
  warns about a human-made role of the same name.
- `@coder` autocompletes and highlights; the bot receives `<@&roleid>`. Nobody holds the role, so a mention pings
  nobody. Several mentions route to several agents. No `@all`, no default agent: plain text with no mention and no
  context is ignored, and so is a mention in a channel the agent is not in. No direct messages (a role does not exist
  in a DM).
- A **reply** to an agent's message goes to that agent; a **thread** opened in a channel the agent is in goes to it.
  `/egzo say agent:<autocomplete> text` is the fallback and where `interrupt` is an option.
- Replies go to the channel or thread of the message that addressed the agent. The sender is forwarded with the text.
- The 250-role limit and a clash of names are the operator's problem: the command says what to rename. The role shows
  in the `@` picker of every channel, so an agent's name is visible to the guild; accepted.
- On `agent.removed` the bot deletes the role (or marks it dead if it cannot).
- Adding or removing needs Discord's Manage Roles and, via the hub, `admin` on the project.
- Bot permissions: Manage Roles (its role above the agent roles), Send Messages, Create Public Threads, Embed Links,
  Read Message History, View Channels (granted per channel for private ones), Use Application Commands. Reading text
  needs the privileged **Message Content** intent (to verify what arrives without it).

## Decided

- Messaging is not in the web UI. Roles are the hub's alone (hub.md).
- One NATS topic per agent (`.in`, `.out`), adapters subscribe to all agents; the hub only relays. Adapters are trusted.
- Two HTTP calls from adapters to the hub: link an account, and may add. Everything else is on the bus.
- The hub double-checks every message `.in`; a refused message is dropped and audited, with no reply.
- A viewer can never talk to an agent. Chat needs `user` or above, in a place the agent was added to; the platform decides the place. A `user` can talk to other agents of the
  project, not only their own (a sensitive point, see "Who decides what").
- The hub reaches agents through the link: a `send` verb and an `events` feed. Nothing reaches a project any other way.
- Ids are `<project>/<agent>`, with the id of the registration, in commands, `actor` strings and audit; principal `agent:<project>/<name>`.
- No `via` field in core: the hub audits the platform identity from the message `.in`.
- Discord: enable the privileged Message Content intent. Without it content is empty except for messages that mention the bot, DMs, the bot's own messages and replies to the
  bot's messages; the docs do not say whether a role mention counts, so the role design cannot rely on it. Self-hosted bots in under 100 guilds need no verification (to re-check).
- An adapter is an optional service of the compose stack, in this repository, with its own binary and image.
- Adapter credentials come from the environment: one secret per adapter (`EGZO_ADAPTERS=discord:<secret>`), from which the hub derives the NATS user (static permissions) and
  the HTTP API token. Prototype the embedded NATS first.

## Open questions

- What a `user` talking to another person's agent can lead to, and whether it needs limits (above). Evaluate carefully before building.
- To verify while building: embed `nats-server` in the hub binary with JetStream on a volume (prototype first); test whether the Discord bot receives content for a role
  mention without the Message Content intent.
