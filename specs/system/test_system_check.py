# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""`egzo system check`: what egzo depends on, outside any project: the engine, its isolation, the log driver, git in the image."""

import re


def lines_of(result):
    return [line for line in result.stdout.splitlines() if line.strip()]


def test_system_check_reports_the_engine_its_isolation_its_log_and_git_and_needs_no_project(tmp_path, engine, egzo):
    # an image to try the log driver with: Podman cannot name its default driver without running something
    result = egzo.run("system", "check", cwd=tmp_path, env={**engine.env, "EGZO_IMAGE": engine.image_for(egzo.binary)})
    assert result.returncode == 0, result.stdout + result.stderr
    lines = lines_of(result)
    assert all(re.match(r"(ok|warn|fail)\s", line) for line in lines), result.stdout
    assert any(line.startswith("ok") and "engine" in line for line in lines)
    assert "internal networks work" in result.stdout, "the engine can make the internal networks isolation needs"
    assert re.search(r"(ok|warn)\s+.*gVisor|runsc", result.stdout) and re.search(r"(ok|warn)\s+.*(rootless|rootful)", result.stdout)
    assert not re.search(r"^(ok|warn|fail)\s+git.*host", result.stdout, re.M), "it asks nothing of the host's git"
    assert re.search(r"^(ok|warn|fail)\s+git.*(image|prep)", result.stdout, re.M), "it says what it found about git in the image"

    # egzo reads container logs (git jobs, `egzo logs`, the agents' status), so a driver that cannot be read is a problem
    log = [line for line in lines if re.match(r"(ok|warn|fail)\s+logs?\b", line)]
    assert log and log[0].startswith("ok"), result.stdout
    assert re.search(r"json-file|local|journald|k8s-file", log[0]), f"the line must name the driver: {log[0]}"
    assert re.search(r"read", log[0]) and re.search(r"follow|stream", log[0]), log[0]


def test_system_check_names_the_log_driver_even_when_there_is_no_image_to_try_it_with_and_fails_when_no_engine_answers(tmp_path, engine, egzo):
    without_image = egzo.run("system", "check", cwd=tmp_path, env={**engine.env, "EGZO_IMAGE": "egzo-spec-no-such-image:0"})
    log = [line for line in lines_of(without_image) if re.match(r"(ok|warn|fail)\s+logs?\b", line)]
    assert log and not log[0].startswith("fail"), without_image.stdout

    no_engine = egzo.run("system", "check", cwd=tmp_path, env={"DOCKER_HOST": "unix:///nonexistent.sock"})
    assert no_engine.returncode != 0 and "fail" in no_engine.stdout and "engine" in no_engine.stdout
