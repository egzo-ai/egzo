# Roadmap: hub discovery

**Status.** Mostly built; what is left is open. Split out of [hub.md](hub.md).

**Built.** The hub does not poll the engines. It follows each engine's event stream and lists that engine again when a container of
an egzo project is made, started, stopped or removed (a change from the CLI shows up within a fraction of a second), and it keeps
a slow timer only as a fallback for a missed event (`EGZO_SCAN_INTERVAL`, 60 seconds). An engine whose event stream does not work
is listed every 5 seconds, and the stream is retried with a delay that grows from one second to a minute. What the agents are
doing is not discovered by listing at all: see [the status feed](../known-issues/container-logging-follows-the-engine-default.md).
Operators hold every project without a grant, so nothing is claimed on a scan any more ([hub-settings.md](hub-settings.md)).

The need that remains: the page should say how fresh it is, and let a person ask for a look.

Everything below is open. Pick the smallest thing that works.

- A manual **Refresh** button on the engine list or the project list page, rescanning one engine or all of them.
- Showing when an engine was last scanned, and whether its events are arriving (the hub knows: it is what decides the short timer).
- A status feed for a project whose control sidecar is older than the hub, or whose log cannot be read, is already shown as
  unavailable; the Engines page could say the same per engine.
- Sleeping the fallback timer while nobody is signed in, so an idle hub makes no engine call at all.
- Anything else that makes the page trustworthy about freshness.
