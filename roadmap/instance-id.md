# Roadmap: every instance has an id of its own

**Status.** Built (code, specs, unit tests). A prerequisite of [proxy-audit.md](proxy-audit.md). No backward compatibility: an
instance spawned before this has no id, and the proxy refuses to bind it; `egzo agent rm` it and spawn again.

## The problem

An instance is known by its name only. Names come back: an auto-chosen name skips numbers while its branch exists, but a name
passed by hand (`egzo agent spawn coder issue-9`) can be spawned, removed and spawned again. Whatever records something about an
instance by name merges the old one into the new one:

- the proxy's audit trail says `"agent": "issue-9"`, for every instance that was ever called that;
- the hub, which groups what it learns by agent, cannot tell a new `issue-9` from the one that was removed an hour ago.

Nothing public tells two instances of one name apart. The control sidecar keeps a nonce per registration, and a new instance of the
name gets a new one, but the nonce is an input of the agent's token: giving key material a second job as a public id is a mistake.
The container id is the engine's, and egzo may make another container for the same instance.

## The decision

- **Spawn makes an id**, a [ULID](https://github.com/ulid/spec): 26 characters of Crockford base32, the time it was made first, so
  ids sort by creation. It is never reused, and nothing derives it from the name.
- **It is the label `ai.egzo.instance-id`** on the instance's container and its network, the two things a spawn makes for this
  instance alone. A home volume or a worktree volume can outlive one instance and be taken up by the next of the name (`--reuse`),
  so it does not carry the id. The network is created first, so an instance whose container is gone still has its id.
- **The proxy binding carries it**, and every audit event of the instance has it as `instance`, next to `agent` (the name, for
  people). A binding without an id is refused. A git job that runs as no instance (the base clone at `project up`) gets a fresh id
  for each run.
- **`egzo ps --json`, `agent ls --json` and `agent spawn --json` show it** as `id`. The hub reads the label like the others.

## Not done

- The control sidecar's registrations and messages stay keyed by name: a name is unique among the instances that exist, and a
  removal unregisters it. The id matters for history, which outlives the instance.
- An environment variable with the id inside the container: nothing in the container needs it yet.
