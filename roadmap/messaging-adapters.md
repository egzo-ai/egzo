# Roadmap: messaging adapters (NATS, egzo-discord)

**Status.** Designed, not built. Split out of [hub.md](hub.md), which is built (the hub and its web UI). Nothing in the hub
depends on this: the operator API and the typed event stream the adapters need are in core already. The hub gains an
embedded NATS server, two HTTP calls (link and expose) and a second check on every inbound message; each adapter is its
own binary and image in `hub/`, an optional service of the compose stack.

Code layout, tests and the hub's model are in hub.md ("Code layout and rules"); adapters live in `hub/cmd/egzo-<platform>/`
(`hub/cmd/egzo-discord/` first) and `hub/internal/`, and are unit-tested, not spec-driven.

Messaging is out of the web UI. Agents are reached from chat platforms through **adapters**: one per platform
(`egzo-discord` first), each its own binary and image in this repository, an optional service of the compose stack.
The adapter is the **external gateway**: it is trusted to vet what comes in from its platform and decides what goes
out. The hub never learns a platform's concepts (guilds, roles, workspaces). Modeled on Scion's Discord plugin
(`inspiration/scion/extras/scion-discord`, studied, not copied): its register flow and question buttons are kept; its
project-level channel links, `@all` and single trust domain are dropped.

## Transport: one topic per agent

The hub embeds a **NATS** server (JetStream for durable consumers). To verify: that it embeds cleanly in the hub
binary, and JetStream's storage on a volume.

    agent.<engine>.<project>.<agent>.out    the hub publishes what the agent says: messages, questions, status
    agent.<engine>.<project>.<agent>.in     adapters publish what users say and answer; the hub forwards it to the agent
    hub.control                             the hub publishes: agent.removed, user.deleted, grant.changed

- The **agent is the topic**. The hub is only the relay between an agent (through the operator API) and its topic. It
  is the only subscriber to the engines' sidecar streams; adapters never see an engine.
- Every adapter subscribes to all agents' `.out` and `.control`, and selects what it relays to which place. The hub
  does not track what is exposed where. Adapters are trusted with all agent traffic.
- Static broker permissions, the same for every adapter: subscribe to `.out` and `hub.control`, publish to `.in`.
- Names are `[a-z0-9_-]`, so they are safe subject tokens.

## The hub API (two calls, from the adapter)

An authenticated HTTP API (`/v1`, a token per adapter) for the two moments an adapter needs the hub:
- **Link**: the adapter asks for a code for an external user; the user, logged in on the web UI, confirms it; the
  adapter polls for the result and gets the hub user id. The adapter stores the pair; the hub keeps nothing.
- **Expose**: the adapter asks whether hub user U may expose agent A (*administrator* on its project). Yes or no.

Nothing else goes through the API.

## Who owns what

| Adapter | Hub |
|---|---|
| vets what comes in; decides what goes out | users, roles, RBAC |
| exposes agents to places (agent → place) | the second check on every message `.in` |
| links external users to hub users, keeps the mapping | confirms link codes, answers expose checks |
| its own state (SQLite on a volume): roles, webhooks, message → agent map, pending questions | relays agent ↔ topic, publishes lifecycle events |

## Messages

- **Inbound**: the adapter vets the platform side (the sender is in the place, linked, the agent is exposed there) and
  publishes to the agent's `.in` with the hub user id, the text and the platform message id. The hub **double-checks**
  before relaying: the user exists, holds any role on the agent's project, the agent exists. If so it sends to the
  agent with `actor = user:<id>`. If not, the message is **dropped and audited**. There is no reply: external users
  do not get a refusal, the message simply does not go through. The platform message id makes it idempotent.
- **Outbound**: the adapter renders the agent's `.out` for its platform, only for places the agent is exposed to.
  Answers to a question and interrupts go `.in` under the same check.
- **Stale state**: the adapter reacts to `hub.control` (drop an exposure on `agent.removed`, a link on `user.deleted`,
  update on `grant.changed`) and reconciles with the hub on reconnect. A stale adapter can still relay output to a
  place after a hub user is deleted; the second check on `.in` stops the other direction. Accepted.
- **Capabilities**: an adapter handles what its platform supports (threads, buttons for question choices, rich text,
  per-agent identity, message length). Hub messages are one neutral model: text, choices, attachments.

## Who decides what

- **The place** (who can see and speak): the platform. A private channel protects an agent: the adapter only receives
  what it can view.
- **The person**: the hub. A sender must hold **any role on the agent's project**. Answering a question and
  interrupting use the same permission. Decided knowingly: a `user` can steer another user's agent through a shared
  channel; exposing it is an administrator's act, per channel. The stricter fallback: chat follows attach.

## Trust

A compromised adapter reads every agent's output and can speak as any hub user it names, to any agent that user may
chat with. It cannot reach engines, secrets or the hub's data beyond the two API calls. Accepted: adapters are trusted
and few. The hub does not record links or exposures, so the web UI cannot list or revoke where an agent is exposed.

## egzo-discord

One bot per guild. `/egzo register` is the link flow. Agents are exposed one at a time, as roles:

    /egzo agent add id:<engine:project/agent> [name:<role name>]    # run in the channel; default engine implied
    /egzo agent remove [--all]                                       # out of this channel, or out of every channel
    /egzo agent rename|list

- Adding calls the hub's expose check, then creates, once per guild, a **role** for the agent (mentionable, no
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
- Adding or removing needs Discord's Manage Roles and, via the hub, *administrator* on the project.
- Bot permissions: Manage Roles (its role above the agent roles), Send Messages, Create Public Threads, Embed Links,
  Read Message History, View Channels (granted per channel for private ones), Use Application Commands. Reading text
  needs the privileged **Message Content** intent (to verify what arrives without it).

## Decided

- Messaging is not in the web UI. Roles are the hub's alone (hub.md).
- One NATS topic per agent (`.in`, `.out`), adapters subscribe to all agents; the hub only relays. Adapters are trusted.
- Two HTTP calls from adapters to the hub: link and expose. Everything else is on the bus.
- The hub double-checks every message `.in`; a refused message is dropped and audited, with no reply.
- Chat permission: any role on the project, in a place the agent was exposed to; the platform decides the place.
- Ids are `[engine:]project/agent` (no colon: the default engine) in commands, `actor` strings and audit; principal
  `agent:[engine:]project/name`; subjects use the full form with dots. Core's `project/agent` stays valid.
- No `via` field in core: the hub audits the platform identity from the message `.in`.
- Discord: enable the privileged Message Content intent. Without it content is empty except for messages that mention
  the bot, DMs, the bot's own messages and replies to the bot's messages; the docs do not say whether a role mention
  counts, so the role design cannot rely on it. Self-hosted bots in under 100 guilds need no verification (to re-check).
- An adapter is an optional service of the compose stack, in this repository, with its own binary and image.
- Adapter credentials come from the environment: one secret per adapter (`EGZO_ADAPTERS=discord:<secret>`), from which the
  hub derives the NATS user (static permissions) and the HTTP API token. Prototype the embedded NATS first.

## Open questions

None left. To verify while building: embed `nats-server` in the hub binary with JetStream on a volume (prototype first);
test whether the Discord bot receives content for a role mention without the Message Content intent.
