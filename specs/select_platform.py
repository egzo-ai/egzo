"""Choose the reference platform to run the specs on, from what this machine has.

    python select_platform.py          # print the report and the chosen platform

The specs test egzo on one reference platform per run (see conftest.py). On a CI machine the platform is
named (`--engine` or EGZO_ENGINE). On a developer machine, with none named, this picks the most capable
platform the machine can be, in this order:

    docker-gvisor     Docker with gVisor (runsc) registered
    podman-rootless   rootless Podman
    docker            Docker
    podman            rootful Podman

and the run prints what it found, what it chose, and how to override it.
"""

import os
import sys
from dataclasses import dataclass

ORDER = ["docker-gvisor", "podman-rootless", "docker", "podman"]


@dataclass
class Selection:
    engine: object  # the chosen Engine, or None when nothing was usable
    report: str


def select(engines, requested=None):
    """The platform to use: the requested one, or the best available. Never raises: with nothing usable
    the selection has no engine and a report saying what to set up."""
    by_name = {e.name: e for e in engines}
    if requested:
        return Selection(by_name[requested], f"egzo specs: platform {requested} (chosen with --engine / EGZO_ENGINE)")

    lines = ["egzo specs: no platform named (--engine / EGZO_ENGINE), choosing from this machine"]
    chosen = None
    for name in ORDER:
        if name not in by_name:
            continue
        usable, note = by_name[name].available()
        mark = "found" if usable else "not found"
        lines.append(f"  {name:<16} {mark}" + (f": {note}" if note else ""))
        if usable and chosen is None:
            chosen = by_name[name]
    if chosen is None:
        lines.append("  nothing usable. Set one up, then pass --engine NAME (or set EGZO_ENGINE):")
        for name in ORDER:
            if name in by_name:
                lines.append(f"    {name}: {by_name[name].setup}")
        return Selection(None, "\n".join(lines))
    lines.append(f"  -> {chosen.name} (the first found, in the order above)")
    lines.append(f"  override: pytest --engine {{{','.join(n for n in ORDER if n in by_name)}}}   or   EGZO_ENGINE=<name>")
    lines.append("  A platform that is not what this machine is makes the specs for the missing feature fail: that is the point.")
    return Selection(chosen, "\n".join(lines))


if __name__ == "__main__":
    sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
    import conftest

    selection = select(conftest.ENGINES, os.environ.get("EGZO_ENGINE"))
    print(selection.report)
    sys.exit(0 if selection.engine else 1)
