# Roadmap: monitoring agents' internet access

**Status.** Open, not designed. Split out of [hub.md](hub.md), where the proxy audit log was listed on the project page and
never built.

Users need a way to monitor agents' internet access and allowed/denied requests.

Everything else is open: where it shows (hub UI, CLI, both), what is recorded and for how long, filtering and search,
live view versus history, per agent versus per project, and who may see it (see the roles in hub.md). The proxy already
writes an audit log in core; see also "Audit sampling" in [review-leftovers.md](review-leftovers.md).
