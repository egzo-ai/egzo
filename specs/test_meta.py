# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""Specs for the spec suite itself: done / broken / unsupported / skipped, and no way to hide a todo."""

import pytest


@pytest.fixture
def suite(pytester):
    pytester.makeconftest('pytest_plugins = ["spec_status"]')
    return pytester


def test_a_failing_spec_is_broken_and_fails_the_run(suite):
    suite.makepyfile(
        test_area="""
        def test_not_built_yet():
            assert False
        """
    )
    result = suite.runpytest()
    result.assert_outcomes(failed=1)
    assert result.ret != 0
    result.stdout.fnmatch_lines(["*area*done*broken*unsupported*skipped*", "area *0 *1 *0 *0"])


def test_there_is_no_todo_marker(suite):
    """A spec for an unimplemented feature must fail, so no marker may turn it green."""
    suite.makepyfile(
        test_area="""
        import pytest

        @pytest.mark.todo("later")
        def test_not_built_yet():
            assert False
        """
    )
    result = suite.runpytest()
    result.assert_outcomes(failed=1)
    assert result.ret != 0


def test_a_platform_xfail_is_unsupported_not_done_and_does_not_fail_the_run(suite):
    suite.makepyfile(
        test_area="""
        import pytest

        @pytest.mark.xfail(strict=True, reason="this platform has no gVisor")
        def test_needs_gvisor():
            assert False
        """
    )
    result = suite.runpytest()
    result.assert_outcomes(xfailed=1)
    assert result.ret == 0
    result.stdout.fnmatch_lines(["area *0 *0 *1 *0"])


def test_a_pass_on_a_platform_said_to_lack_the_feature_is_broken(suite):
    suite.makepyfile(
        test_area="""
        import pytest

        @pytest.mark.xfail(strict=True, reason="this platform has no gVisor")
        def test_needs_gvisor():
            assert True
        """
    )
    result = suite.runpytest()
    result.assert_outcomes(failed=1)
    result.stdout.fnmatch_lines(["*XPASS(strict)*", "area *0 *1 *0 *0"])


def test_summary_counts_each_status_per_area(suite):
    suite.makepyfile(
        test_alpha="""
        import pytest

        def test_done():
            pass

        def test_broken():
            assert False
        """,
        test_beta="""
        import pytest

        @pytest.mark.xfail(strict=True, reason="unsupported platform")
        def test_unsupported():
            assert False
        """,
    )
    result = suite.runpytest()
    result.stdout.fnmatch_lines(
        [
            "*spec status*",
            "alpha *1 *1 *0 *0",
            "beta *0 *0 *1 *0",
            "total *1 *1 *1 *0",
            "1/3 specs done",
        ]
    )


def test_skipped_spec_is_not_counted_as_done(suite):
    suite.makepyfile(
        test_area="""
        import pytest

        @pytest.mark.skip(reason="does not apply")
        def test_not_for_this_platform():
            pass
        """
    )
    result = suite.runpytest()
    result.stdout.fnmatch_lines(["area *0 *0 *0 *1"])


def test_every_command_line_a_spec_writes_is_a_command_egzo_has(egzo):
    """The specs write commands as the shell would take them (`project.sh("agent spawn coder -m hi")`). Each one written as a
    literal is checked against the CLI's own help, so a spec cannot name a command or a flag that was renamed or removed."""
    import ast
    from pathlib import Path

    import clitree

    problems = []
    for path in sorted(Path(__file__).parent.rglob("*.py")):
        for node in ast.walk(ast.parse(path.read_text())):
            if not (isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute) and node.func.attr in ("sh", "ok") and node.args):
                continue
            first = node.args[0]
            if isinstance(first, ast.JoinedStr):  # an f-string: what is substituted is one word
                text = "".join(v.value if isinstance(v, ast.Constant) else "X" for v in first.values)
            elif isinstance(first, ast.Constant) and isinstance(first.value, str):
                text = first.value
            else:
                continue
            import shlex

            wrong = clitree.problem(egzo.binary, shlex.split(text))
            if wrong:
                problems.append(f"{path.name}:{node.lineno}: egzo {text}: {wrong}")
    assert not problems, "\n".join(problems)
