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
