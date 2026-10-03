#!/usr/bin/env python3
"""Remove @pytest.mark.todo from every spec that now passes.

    pytest --spec-json=status.json ; python3 promote.py status.json

A parametrized spec is promoted only when all of its cases pass. Run the suite again afterwards.
"""

import json
import re
import sys
from collections import defaultdict
from pathlib import Path

TODO = re.compile(r"^\s*@pytest\.mark\.todo(\(.*\))?\s*$")


def promotable(status):
    cases = defaultdict(list)
    for nodeid, outcome in status.items():
        path, _, rest = nodeid.partition("::")
        cases[(path, rest.split("[")[0])].append(outcome)
    return sorted(key for key, outcomes in cases.items() if all(o == "promote" for o in outcomes))


def promote(path, function):
    source = Path(path)
    lines = source.read_text().splitlines(keepends=True)
    for index, line in enumerate(lines):
        if re.match(rf"^def {re.escape(function)}\(", line):
            cursor = index - 1
            while cursor >= 0 and lines[cursor].lstrip().startswith("@"):
                if TODO.match(lines[cursor]):
                    del lines[cursor]
                    source.write_text("".join(lines))
                    return True
                cursor -= 1
            return False
    return False


def main():
    status = json.loads(Path(sys.argv[1]).read_text())
    root = Path(__file__).parent
    done = 0
    for path, function in promotable(status):
        if promote(root / Path(path).name, function):
            done += 1
            print(f"promoted {Path(path).name}::{function}")
    print(f"{done} spec(s) promoted")


if __name__ == "__main__":
    main()
