"""pytest plugin implementing the spec status model.

The suite is the specification, so running it must answer "what is done, what is not, what does not
apply" without reading anything else.

    pass                       done         the behaviour exists and works
    fail                       broken       the spec fails: the feature is not built, or it is wrong
    xfail                      unsupported  the reference platform cannot do it (gVisor without gVisor)
    skip                       skipped      the spec does not apply to this platform

There is no marker for "not implemented yet": that spec fails, and the failing specs are the todo list.
xfail is only for platforms, and strict, so a pass on a platform said to lack the feature is reported.
"""

import json

import pytest

COLUMNS = ["done", "broken", "unsupported", "skipped"]


def pytest_addoption(parser):
    parser.addoption("--spec-json", metavar="PATH", help="write the outcome of every spec as JSON (nodeid -> status)")


def pytest_configure(config):
    config.addinivalue_line(
        "markers",
        "engine: needs a container engine; runs against the one scenario chosen with --engine",
    )
    config.pluginmanager.register(SpecStatus(config), "spec-status")


def _classify(report):
    if report.skipped:
        return "unsupported" if hasattr(report, "wasxfail") else "skipped"
    if report.failed:
        return "broken"
    if hasattr(report, "wasxfail"):
        return "broken"  # passed although this platform is said to lack the feature
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
        terminalreporter.write_line("area".ljust(width) + "".join(c.rjust(13) for c in COLUMNS))
        totals = dict.fromkeys(COLUMNS, 0)
        for area in sorted(areas):
            terminalreporter.write_line(area.ljust(width) + "".join(str(areas[area][c]).rjust(13) for c in COLUMNS))
            for c in COLUMNS:
                totals[c] += areas[area][c]
        terminalreporter.write_line("total".ljust(width) + "".join(str(totals[c]).rjust(13) for c in COLUMNS))
        terminalreporter.write_line(f"{totals['done']}/{sum(totals.values())} specs done")
