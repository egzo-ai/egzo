"""pytest plugin implementing the spec status model.

The suite is the specification, so running it must answer "what is done, what is todo, what is
broken" without reading anything else.

    pass                       done     the behaviour exists and works
    xfail  (@pytest.mark.todo) todo     specified, not implemented yet
    fail                       broken   implemented (or expected to be) and wrong
    strict XPASS               promote  a todo spec passes now: remove its @todo marker

A todo spec runs as a *strict* xfail, so it can never rot: when the feature lands, the spec starts
passing, the run fails, and the author is forced to promote it to a regular spec.
"""

import json

import pytest

COLUMNS = ["done", "todo", "broken", "promote", "skipped"]


def pytest_addoption(parser):
    parser.addoption("--spec-json", metavar="PATH", help="write the outcome of every spec as JSON (nodeid -> status)")


def pytest_configure(config):
    config.addinivalue_line(
        "markers",
        "todo(reason=None): specified but not implemented yet; runs as a strict xfail",
    )
    config.addinivalue_line(
        "markers",
        "engine: needs a container engine; runs against the one scenario chosen with --engine",
    )
    config.pluginmanager.register(SpecStatus(config), "spec-status")


def _classify(report):
    if report.skipped:
        return "todo" if hasattr(report, "wasxfail") else "skipped"
    if report.failed:
        return "promote" if "XPASS(strict)" in str(report.longrepr) else "broken"
    if hasattr(report, "wasxfail"):
        return "promote"
    return "done"


def _area(nodeid):
    stem = nodeid.split("::")[0].rsplit("/", 1)[-1].removesuffix(".py")
    return stem.removeprefix("test_")


class SpecStatus:
    """Collects one outcome per spec and prints the status table."""

    def __init__(self, config):
        self.config = config
        self.status = {}

    @pytest.hookimpl(trylast=True)
    def pytest_collection_modifyitems(self, items):
        for item in items:
            marker = item.get_closest_marker("todo")
            if marker is not None:
                reason = marker.args[0] if marker.args else marker.kwargs.get("reason", "not implemented")
                item.add_marker(pytest.mark.xfail(reason=f"todo: {reason}", strict=True))
            if "engine" in getattr(item, "fixturenames", ()):
                item.add_marker(pytest.mark.engine)

    def pytest_runtest_logreport(self, report):
        if report.when == "teardown":
            if report.failed:
                self.status[report.nodeid] = "broken"
            return
        if report.when == "setup" and report.outcome == "passed":
            return
        self.status[report.nodeid] = _classify(report)

    def pytest_sessionfinish(self):
        path = self.config.getoption("--spec-json")
        if path:
            with open(path, "w") as out:
                json.dump(self.status, out, indent=1, sort_keys=True)

    def pytest_terminal_summary(self, terminalreporter):
        if not self.status:
            return
        areas = {}
        for nodeid, outcome in self.status.items():
            areas.setdefault(_area(nodeid), dict.fromkeys(COLUMNS, 0))[outcome] += 1

        terminalreporter.write_sep("=", "spec status")
        width = max(len(area) for area in areas) + 2
        terminalreporter.write_line("area".ljust(width) + "".join(c.rjust(9) for c in COLUMNS))
        totals = dict.fromkeys(COLUMNS, 0)
        for area in sorted(areas):
            terminalreporter.write_line(area.ljust(width) + "".join(str(areas[area][c]).rjust(9) for c in COLUMNS))
            for c in COLUMNS:
                totals[c] += areas[area][c]
        terminalreporter.write_line("total".ljust(width) + "".join(str(totals[c]).rjust(9) for c in COLUMNS))
        terminalreporter.write_line(f"{totals['done']}/{sum(totals.values())} specs done")
        if totals["promote"]:
            terminalreporter.write_line("promote: a todo spec passes now, remove its @pytest.mark.todo marker")
