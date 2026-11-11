# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""Docker inside an agent: a template that keeps its capabilities (`capabilities: [ALL]`), on the harness image with Docker inside
(harness/docker), whose entrypoint starts the daemon with sudo before the harness. The daemon runs in the agent's own sandbox, with no
host socket and no --privileged; that needs an engine whose runtime is sysbox, so the spec is unsupported on every other platform.

One project is up for the module, with that template; the spec builds an image from nothing it has to download (the egzo binary
on scratch) and runs it."""

import time

import pytest

from support import agent, anthropic_profile, spec

API_KEY = "sk-ant-egzo-spec-not-a-real-key-0000000000"


@pytest.fixture(scope="module")
def sandbox(shared_project, engine, egzo):
    p = shared_project
    p.env.update({"ANTHROPIC_API_KEY": API_KEY})
    image = engine.docker_harness_image("claude-code", egzo.binary)
    p.up(spec(egress={"default": anthropic_profile()}, agents={"docker": agent(image=image, capabilities=["ALL"])}), timeout=900)
    return p


def in_agent(engine, project, name, script):
    return engine.exec(engine.instance(project.name, name).name, "sh", "-c", script)


def test_an_agent_that_keeps_its_capabilities_runs_docker_in_its_own_sandbox(sandbox, engine, request):
    if not engine.sysbox:
        request.applymarker(pytest.mark.xfail(strict=True, reason="Docker inside an agent needs an engine whose runtime is sysbox"))
    p = sandbox
    name = p.spawn("docker")

    deadline = time.monotonic() + 60
    while (answer := in_agent(engine, p, name, "docker version --format '{{.Server.Version}}'")).returncode != 0:
        assert time.monotonic() < deadline, "the daemon did not start:\n" + answer.stderr + in_agent(engine, p, name, "cat /var/log/dockerd.log").stdout[-3000:]
        time.sleep(1)

    # an image made of the egzo binary alone: nothing to pull, so it does not depend on the proxy
    ran = in_agent(engine, p, name, """
        set -e
        mkdir -p /tmp/nested && cp /usr/local/bin/egzo /tmp/nested/
        printf 'FROM scratch\\nCOPY egzo /egzo\\nENTRYPOINT ["/egzo", "version"]\\n' > /tmp/nested/Dockerfile
        docker build -q -t nested /tmp/nested >/dev/null
        docker run --rm nested
    """)
    assert ran.returncode == 0, ran.stderr
    assert ran.stdout.strip() == p.sh("version").stdout.strip(), "the nested container ran the egzo binary"
