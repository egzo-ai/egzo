# Roadmap: hub discovery

**Status.** Open, not designed. Split out of [hub.md](hub.md).

Today the hub finds projects and agents by labels, per engine, on a timer (`EGZO_SCAN_INTERVAL`). A change made from the CLI
(`project up`, a spawn, a removal) shows up only at the next scan. Orphan-project claiming also runs on every scan.

The need: the hub should show what is on the engines without surprising or stale pages, and without hammering engines.

Everything below is open. Pick the smallest thing that works.

- A manual **Refresh** button on the engine list or the project list page, rescanning one engine or all of them.
- Engine events (docker events) to rescan on change, with the timer as a fallback.
- Per-engine scan intervals, or backing off for an unreachable engine.
- Showing when an engine was last scanned.
- Anything else that makes the page trustworthy about freshness.
