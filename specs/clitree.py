# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""The command tree of the real `egzo` binary, read from its own `--help`, so a spec that writes a command line as a
string can be checked against it: an unknown command or flag is a failure of the spec, not of egzo."""

import re
import subprocess

_trees = {}


class Node:
    def __init__(self, path, flags, children, aliases, alias_names):
        self.alias_names = alias_names  # the other names this command answers to
        self.path = path
        self.flags = flags  # long names and short letters this command accepts, global ones included
        self.children = children  # name -> Node
        self.aliases = aliases  # alias -> name


def _help(binary, path):
    return subprocess.run([binary, *path, "--help"], capture_output=True, text=True, env={"NO_COLOR": "1"}).stdout


def _section(text, title):
    found = re.search(rf"^{title}:\n(.*?)(?:\n\n|\Z)", text, re.S | re.M)
    return found.group(1) if found else ""


def _walk(binary, path, inherited):
    text = _help(binary, path)
    flags = set(inherited)
    for line in (_section(text, "Flags") + "\n" + _section(text, "Global Flags")).splitlines():
        for short, long in re.findall(r"(?:^\s+-(\w), )?--([\w-]+)", line):
            flags.add("--" + long)
            if short:
                flags.add("-" + short)
        found = re.match(r"\s+-(\w)[ ,]", line)
        if found:
            flags.add("-" + found.group(1))
    children, aliases = {}, {}
    for line in _section(text, "Available Commands").splitlines():
        if line.split():
            name = line.split()[0]
            children[name] = _walk(binary, [*path, name], flags)
            for alias in children[name].alias_names:
                aliases[alias] = name
    found = re.search(r"^Aliases:\n\s+(.*)$", text, re.M)
    alias_names = [n.strip() for n in found.group(1).split(",")][1:] if found else []
    return Node(path, flags, children, aliases, alias_names)


def tree(binary):
    if binary not in _trees:
        _trees[binary] = _walk(binary, [], set())
    return _trees[binary]


def problem(binary, argv):
    """Why this command line is not one egzo has, or None. Only what the help tree can tell is checked: the command
    path (flags come after it), and that every `-x` or `--flag` before a `--` is one the command knows."""
    node = tree(binary)
    for word in argv:
        if word == "--" or word.startswith("-") or not node.children:
            break
        name = node.aliases.get(word, word)
        if name not in node.children:
            where = f"`egzo {' '.join(node.path)}`" if node.path else "egzo"
            return f"{where} has no command {word!r} (it has: {', '.join(sorted(node.children))})"
        node = node.children[name]
    for word in argv:
        if word == "--":
            break
        flag = word.split("=", 1)[0]
        if word.startswith("-") and len(word) > 1 and not word[1].isdigit() and flag not in node.flags:
            return f"`egzo {' '.join(node.path)}` has no flag {flag}"
    return None
