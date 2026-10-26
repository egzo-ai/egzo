# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""`build:` on an agent: `project up` builds the image from a Dockerfile, through the engine's build endpoint, and the agent runs it.

The image is tagged by content, so an unchanged context is not built again and a changed one makes the agent's instances stale. egzo
needs no docker or podman binary for it, and no buildx. The Dockerfiles here start from local images (the specs' agent image, the
session image) so that a build needs no network, except the spec about a base image that is not on the engine."""

import secrets

import pytest

from conftest import LABEL_PREFIX
from support import agent, spec

pytestmark = pytest.mark.usefixtures("engine")

# what the specs' Dockerfiles start from: the agent image, given as a build argument
FROM_BASE = "ARG BASE\nFROM ${BASE}\n"


def builder(base, **fields):
    """A custom agent built from the context `agent` of the project, starting from base (passed as the build argument BASE)."""
    build = {"context": "./agent", **fields}
    build["args"] = {"BASE": base, **build.get("args", {})}
    return agent(harness="custom", build=build)


def write_context(project, dockerfile, files=None, name="Dockerfile", context="agent"):
    directory = project.mkdir(context)
    (directory / name).write_text(dockerfile)
    for path, text in (files or {}).items():
        (directory / path).parent.mkdir(parents=True, exist_ok=True)
        (directory / path).write_text(text)
    return directory


def built_images(engine, project):
    """The images egzo built for the project: repository:tag, by the project label egzo puts on them."""
    listed = engine.run("image", "ls", "--filter", f"label={LABEL_PREFIX}project={project.name}", "--format", "{{.Repository}}:{{.Tag}}")
    assert listed.returncode == 0, listed.stderr
    return sorted(line for line in listed.stdout.split() if line)


def image_of(engine, project, instance):
    return engine.instance(project.name, instance).raw["Config"]["Image"]


def read(engine, project, instance, path):
    result = engine.exec(project.container(instance), "cat", path)
    assert result.returncode == 0, result.stderr
    return result.stdout.strip()


# --- the file ---


def test_build_and_image_on_one_agent_is_refused_naming_both(project):
    write_context(project, "FROM scratch\n")
    result = project.config(spec(agents={"coder": agent(harness="custom", image="alpine:3", build={"context": "./agent"})}))
    assert result.returncode != 0
    assert 'agent "coder"' in result.stderr and "build" in result.stderr and "image" in result.stderr


def test_a_missing_context_or_dockerfile_is_refused_naming_the_path(project):
    result = project.config(spec(agents={"coder": agent(harness="custom", build={"context": "./nowhere"})}))
    assert result.returncode != 0 and "./nowhere" in result.stderr

    write_context(project, "FROM scratch\n")
    result = project.config(spec(agents={"coder": agent(harness="custom", build={"context": "./agent", "dockerfile": "Missing.dockerfile"})}))
    assert result.returncode != 0 and "Missing.dockerfile" in result.stderr


def test_a_dockerfile_outside_its_context_and_an_argument_egzo_sets_are_refused(project):
    write_context(project, "FROM scratch\n")
    (project.root / "Dockerfile").write_text("FROM scratch\n")
    result = project.config(spec(agents={"coder": agent(harness="custom", build={"context": "./agent", "dockerfile": "../Dockerfile"})}))
    assert result.returncode != 0 and "../Dockerfile" in result.stderr

    result = project.config(spec(agents={"coder": agent(build={"context": "./agent", "args": {"EGZO_HARNESS_IMAGE": "x"}})}))
    assert result.returncode != 0 and "EGZO_HARNESS_IMAGE" in result.stderr


def test_the_custom_harness_takes_a_build_instead_of_an_image(project):
    write_context(project, "FROM scratch\n")
    resolved = project.resolved(spec(agents={"coder": agent(harness="custom", build={"context": "./agent", "args": {"A": "1"}, "target": "dev"})}))
    build = resolved["agents"]["coder"]["build"]
    assert build["context"] == "./agent" and build["dockerfile"] == "Dockerfile" and build["args"] == {"A": "1"} and build["target"] == "dev"


# --- building ---


def test_up_builds_the_image_and_the_agent_runs_it(live_project, engine, agent_image):
    write_context(live_project, FROM_BASE + "COPY note.txt /note.txt\nRUN echo built > /marker\n", {"note.txt": "from the context\n"})
    result = live_project.up(spec(agents={"coder": builder(agent_image)}))
    (image,) = built_images(engine, live_project)
    assert f"build image {image}" in result.stdout, result.stdout
    live_project.spawn("coder")
    assert image_of(engine, live_project, "coder-1") == image
    assert read(engine, live_project, "coder-1", "/marker") == "built"
    assert read(engine, live_project, "coder-1", "/note.txt") == "from the context"


def test_a_second_up_with_nothing_changed_does_not_build_again(live_project, engine, agent_image):
    write_context(live_project, FROM_BASE + "RUN echo built > /marker\n")
    live_project.up(spec(agents={"coder": builder(agent_image)}))
    before = built_images(engine, live_project)
    again = live_project.up()
    assert "build image" not in again.stdout, again.stdout
    assert built_images(engine, live_project) == before


def test_a_changed_file_argument_or_dockerfile_makes_the_instances_stale_and_up_keeps_only_the_latest_image(live_project, engine, agent_image):
    write_context(live_project, FROM_BASE + "COPY note.txt /note.txt\n", {"note.txt": "one\n"})
    document = spec(agents={"coder": builder(agent_image)})
    live_project.up(document)
    live_project.spawn("coder")
    assert live_project.ps_json()["coder-1"]["stale"] is False

    changes = [
        lambda: (live_project.root / "agent" / "note.txt").write_text("two\n"),
        lambda: live_project.write(spec(agents={"coder": builder(agent_image, args={"EXTRA": "1"})})),
        lambda: (live_project.root / "agent" / "Dockerfile").write_text(FROM_BASE + "COPY note.txt /note.txt\nRUN true\n"),
    ]
    for change in changes:
        change()
        assert live_project.ps_json()["coder-1"]["stale"] is True
        refused = live_project.sh("project up", timeout=300)
        assert refused.returncode != 0 and "stale" in refused.stderr
        live_project.ok("agent rm --stale --yes")
        live_project.up()
        live_project.spawn("coder")
        assert live_project.ps_json()["coder-1"]["stale"] is False
        assert built_images(engine, live_project) == [image_of(engine, live_project, "coder-1")], "only the latest image is kept"
    assert read(engine, live_project, "coder-1", "/note.txt") == "two"


def test_dockerignore_is_honoured(live_project, engine, agent_image):
    write_context(
        live_project,
        FROM_BASE + "COPY . /context/\n",
        {".dockerignore": "ignored/\n*.log\n!keep.log\n", "kept.txt": "a\n", "ignored/secret.txt": "x\n", "debug.log": "x\n", "keep.log": "k\n"},
    )
    live_project.up(spec(agents={"coder": builder(agent_image)}))
    live_project.spawn("coder")
    listing = engine.exec(live_project.container("coder-1"), "ls", "-a", "/context")
    assert sorted(listing.stdout.split()) == sorted([".", "..", ".dockerignore", "Dockerfile", "keep.log", "kept.txt"]), listing.stdout

    (live_project.root / "agent" / "ignored" / "secret.txt").write_text("changed\n")
    (live_project.root / "agent" / "debug.log").write_text("changed\n")
    assert live_project.ps_json()["coder-1"]["stale"] is False, "a change in an ignored file changes nothing"
    assert "build image" not in live_project.up().stdout


def test_a_dockerfile_with_a_cache_mount_and_a_heredoc_builds(live_project, engine, agent_image):
    write_context(
        live_project,
        FROM_BASE + "RUN --mount=type=cache,target=/cache echo cached > /cache/c && cp /cache/c /from-cache\n"
        "RUN <<EOF\necho heredoc > /from-heredoc\nEOF\n",
    )
    live_project.up(spec(agents={"coder": builder(agent_image)}))
    live_project.spawn("coder")
    assert read(engine, live_project, "coder-1", "/from-cache") == "cached"
    assert read(engine, live_project, "coder-1", "/from-heredoc") == "heredoc"


def test_a_base_image_that_is_not_on_the_engine_is_pulled(live_project, engine, internet):
    base = "docker.io/library/busybox:1.36.1"
    engine.run("image", "rm", "-f", base)
    assert engine.run("image", "inspect", base).returncode != 0
    write_context(live_project, f'FROM {base}\nRUN echo built > /marker\nCMD ["sleep", "infinity"]\n')
    live_project.up(spec(agents={"coder": agent(harness="custom", build={"context": "./agent"})}))
    live_project.spawn("coder")
    assert read(engine, live_project, "coder-1", "/marker") == "built"


def test_a_syntax_line_is_refused_on_docker_with_what_to_do(live_project, engine, agent_image):
    if engine.cli != "docker":
        pytest.skip("Podman builds with buildah, which reads the Dockerfile itself")
    write_context(live_project, "# syntax=docker/dockerfile:1\n" + FROM_BASE)
    live_project.write(spec(agents={"coder": builder(agent_image)}))
    result = live_project.sh("project up", timeout=300)
    assert result.returncode != 0
    assert "syntax" in result.stderr and "not supported" in result.stderr, result.stderr


def test_a_failing_step_shows_its_own_output(live_project, engine, agent_image):
    write_context(live_project, FROM_BASE + "RUN echo the-step-said-this && exit 3\n")
    live_project.write(spec(agents={"coder": builder(agent_image)}))
    result = live_project.sh("project up", timeout=300)
    assert result.returncode != 0
    assert "the-step-said-this" in result.stdout + result.stderr
    assert built_images(engine, live_project) == []


def test_the_harness_image_reaches_the_build_and_the_built_agent_runs_the_harness(live_project, engine, session_image):
    version = live_project.sh("version").stdout.split()[-1]
    prefix = f"egzo-spec-local/{secrets.token_hex(4)}-"
    harness = f"{prefix}opencode:{version}"
    assert engine.run("tag", session_image, harness).returncode == 0
    try:
        live_project.env["EGZO_HARNESS_PREFIX"] = prefix
        write_context(
            live_project,
            "ARG EGZO_HARNESS_IMAGE\nFROM ${EGZO_HARNESS_IMAGE}\nARG EGZO_HARNESS_IMAGE\nRUN echo \"$EGZO_HARNESS_IMAGE\" > /harness-image\n",
        )
        live_project.up(spec(agents={"coder": agent(harness="opencode", build={"context": "./agent"}, env={"FAKE_TUI": "cat"})}))
        live_project.spawn("coder", timeout=600)
        assert read(engine, live_project, "coder-1", "/harness-image") == harness
        assert engine.instance(live_project.name, "coder-1").raw["State"]["Running"], "the session holder runs from the built image"
    finally:
        engine.run("image", "rm", "-f", harness)


def test_a_spawn_after_the_image_was_removed_says_to_run_up_again(live_project, engine, agent_image):
    write_context(live_project, FROM_BASE)
    live_project.up(spec(agents={"coder": builder(agent_image)}))
    for image in built_images(engine, live_project):
        assert engine.run("image", "rm", "-f", image).returncode == 0
    result = live_project.sh("agent spawn coder", timeout=300)
    assert result.returncode != 0
    assert "egzo project up" in result.stderr, result.stderr
    assert engine.instances(live_project.name) == []


def test_down_removes_the_images_it_built(live_project, engine, agent_image):
    write_context(live_project, FROM_BASE)
    live_project.up(spec(agents={"coder": builder(agent_image)}))
    assert len(built_images(engine, live_project)) == 1
    live_project.ok("project down --force")
    assert built_images(engine, live_project) == []
