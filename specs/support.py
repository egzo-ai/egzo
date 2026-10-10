# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""Small builders so specs read as the YAML they describe."""

import copy
import os
import stat

VAULT = {"main": {"backend": "env", "secrets": ["ANTHROPIC_API_KEY", "GITHUB_TOKEN", "DEPLOY_TOKEN"]}}

GIT_WORKSPACE = {"repo": {"git": {"url": "https://github.com/acme/shop.git"}}}


def spec(**sections):
    """An egzo.yaml as a dict, with the shared vault unless overridden."""
    document = {"vaults": copy.deepcopy(VAULT)}
    document.update(sections)
    return document


def agent(**fields):
    """An agent definition; claude-code unless overridden."""
    definition = {"harness": "claude-code"}
    definition.update(fields)
    return definition


def anthropic_profile(**extra):
    """The profile that lets an agent reach its model API."""
    profile = {
        "allow": ["platform.claude.com"],
        "services": {"anthropic": "main/ANTHROPIC_API_KEY"},
    }
    profile.update(extra)
    return profile


def mounts(resolved, agent_name):
    """The resolved workspaces of an agent, keyed by mount path."""
    return {m["mount"]: m for m in resolved["agents"][agent_name]["workspaces"]}


def profile(resolved, name):
    return resolved["egress"][name]


def table(output):
    """Rows of a column-aligned table (as `egzo ps` and `egzo messages` print them) as dicts."""
    import re

    lines = output.splitlines()
    if not lines:
        return []
    starts = [m.start() for m in re.finditer(r"\S+", lines[0]) if m.start() == 0 or lines[0][m.start() - 1] == " "]
    names = lines[0].split()
    rows = []
    for line in lines[1:]:
        cells = [line[a:b].strip() for a, b in zip(starts, starts[1:] + [None])]
        rows.append(dict(zip(names, cells)))
    return rows


PASS_STUB = """#!/bin/sh
# A stand-in for `pass`: serves entries from $PASS_STUB_STORE and records every call.
printf '%s\\t%s\\n' "$*" "${PASSWORD_STORE_DIR-unset}" >> "$PASS_STUB_LOG"
command=$1
shift
name=
for argument; do case $argument in -*) ;; *) name=$argument ;; esac; done
file="$PASS_STUB_STORE/$name"
case $command in
  show) if [ -d "$file" ]; then printf '%s\\n\\342\\224\\224\\342\\224\\200\\342\\224\\200 entry\\n' "$name"; elif [ -f "$file" ]; then cat "$file"; else echo "Error: $name is not in the password store." >&2; exit 1; fi ;;
  *) echo "pass stub: unsupported command $command" >&2; exit 2 ;;
esac
"""


class FakePass:
    """A `pass` on PATH that keeps its entries in a directory, so specs need no gpg key. egzo runs `pass`
    directly, so what the specs check is the call: which command, which entry name, which environment."""

    def __init__(self, root, **entries):
        self.store = root / "store"
        self.log = root / "calls.log"
        self.bin = root / "bin"
        self.bin.mkdir(parents=True)
        self.store.mkdir()
        self.log.touch()
        executable = self.bin / "pass"
        executable.write_text(PASS_STUB)
        executable.chmod(executable.stat().st_mode | stat.S_IXUSR)
        for name, value in entries.items():
            self.put(name, value)

    @property
    def env(self):
        """The environment that puts the stand-in first on PATH."""
        return {"PATH": f"{self.bin}{os.pathsep}{os.environ['PATH']}", "PASS_STUB_STORE": str(self.store), "PASS_STUB_LOG": str(self.log)}

    def put(self, name, value):
        entry = self.store / name
        entry.parent.mkdir(parents=True, exist_ok=True)
        entry.write_text(value)

    def get(self, name):
        entry = self.store / name
        return entry.read_text() if entry.exists() else None

    def calls(self):
        """Every call so far, as (arguments, PASSWORD_STORE_DIR as pass saw it)."""
        rows = [line.split("\t") for line in self.log.read_text().splitlines()]
        return [(arguments.split(), store_dir) for arguments, store_dir in rows]
