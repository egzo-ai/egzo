# Roadmap: executable docs

**Status.** Idea. Follows the command-literal specs of [specs-overhaul.md](specs-overhaul.md); not part of it.

The documentation shows many shell sessions and YAML examples (`docs/*.md`, `README.md`). They were written from the design and
the code and not all executed, and they drift: a housekeeping pass on 2026-10-15 found stale commands in several pages that
only a person reading them noticed.

The idea: run the `console` blocks of the docs (the lines starting with `$ egzo`) and the YAML examples (through
`egzo project check --resolved`) in the spec suite, the way doctest does, so a command or a key that no longer exists fails
a spec.

Open points: how to mark a block that cannot run (needs a real API key, the internet, a registry), how to fix the project
a page's examples run in, whether the output shown after a command is compared or only that the command is accepted, and which
tier of the suite it belongs to (most blocks need no engine: `--help` and `project check` are enough for a first version).
This also covers part of the documentation review in [initial-release.md](initial-release.md).
