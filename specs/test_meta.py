"""Specs for the spec suite itself: the todo / done / broken model must hold."""

import pytest


@pytest.fixture
def suite(pytester):
    pytester.makeconftest('pytest_plugins = ["spec_status"]')
    return pytester


def test_failing_todo_is_reported_as_todo_and_does_not_fail_the_run(suite):
    suite.makepyfile(
        test_area="""
        import pytest

        @pytest.mark.todo
        def test_not_built_yet():
            assert False
        """
    )
    result = suite.runpytest()
    result.assert_outcomes(xfailed=1)
    assert result.ret == 0


def test_passing_todo_fails_the_run_and_asks_for_promotion(suite):
    suite.makepyfile(
        test_area="""
        import pytest

        @pytest.mark.todo
        def test_now_works():
            assert True
        """
    )
    result = suite.runpytest()
    result.assert_outcomes(failed=1)
    result.stdout.fnmatch_lines(["*XPASS(strict)*", "*promote*"])


def test_failing_regular_spec_is_broken(suite):
    suite.makepyfile(
        test_area="""
        def test_regression():
            assert False
        """
    )
    result = suite.runpytest()
    result.assert_outcomes(failed=1)
    result.stdout.fnmatch_lines(["*area*done*todo*broken*", "area *0 *0 *1 *0 *0"])


def test_summary_counts_each_status_per_area(suite):
    suite.makepyfile(
        test_alpha="""
        import pytest

        def test_done():
            pass

        @pytest.mark.todo
        def test_todo():
            assert False
        """,
        test_beta="""
        import pytest

        @pytest.mark.todo
        def test_another_todo():
            assert False
        """,
    )
    result = suite.runpytest()
    result.stdout.fnmatch_lines(
        [
            "*spec status*",
            "alpha *1 *1 *0 *0 *0",
            "beta *0 *1 *0 *0 *0",
            "total *1 *2 *0 *0 *0",
            "1/3 specs done",
        ]
    )


def test_skipped_spec_is_not_counted_as_done_or_todo(suite):
    suite.makepyfile(
        test_area="""
        import pytest

        @pytest.mark.skip(reason="no engine")
        def test_needs_engine():
            pass
        """
    )
    result = suite.runpytest()
    result.stdout.fnmatch_lines(["area *0 *0 *0 *0 *1"])
