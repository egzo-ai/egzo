# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""What an instance is made of: one container on an internal network of its own, joined only by the control sidecar and the proxy;
who it runs as and what it may do; its limits; and the workspace volumes it mounts.

One project is up for the whole module (`sandbox`), with templates for each kind of agent these specs need, and every spec starts
from no instance. What goes in and out of a workspace (`workspace put|get`, git) is in the workspace specs."""

import os

import pytest

from conftest import LABEL_PREFIX
from support import custom, spec


@pytest.fixture(scope="module")
def sandbox(shared_project, engine):
    image = engine.agent_image()
    (shared_project.root / "prompt.md").write_text("be brief\n")
    shared_project.up(spec(
        workspaces={"data": {}, "scratch": {}, "docs": {}},
        agents={
            "coder": custom(image, workspaces=["data"], prompt="./prompt.md"),
            "reviewer": custom(image, workspaces=["data"]),
            "solo": custom(image, workspaces=["scratch"]),
            "reader": custom(image, workspaces=["docs:ro"]),
            "limited": custom(image, resources={"cpus": 1, "memory": "256m"}),
            "small": custom(image, resources={"pids": 64, "memory": "256m"}),
            "plain": custom(image),
        },
    ))
    return shared_project


@pytest.fixture(autouse=True)
def start_from_no_instance(sandbox):
    sandbox.retire()
    yield


def run_in(engine, project, name, *command):
    return engine.exec(engine.instance(project.name, name).name, *command)


def test_each_instance_has_a_network_of_its_own_and_cannot_reach_the_internet_or_another_instance(sandbox, engine):
    p = sandbox
    coder, reviewer = p.spawn("coder"), p.spawn("reviewer")
    container = engine.instance(p.name, coder)
    assert set(container.raw["NetworkSettings"]["Networks"]) == {f"{p.name}_{coder}"}
    for name in (coder, reviewer):
        raw = next(r for r in engine.resources(p.name) if r.kind == "network" and r.name == f"{p.name}_{name}").raw
        assert raw.get("Internal", raw.get("internal")) is True, name

    assert run_in(engine, p, coder, "wget", "-T", "3", "-q", "-O", "/dev/null", "http://1.1.1.1/").returncode != 0
    assert run_in(engine, p, coder, "wget", "-T", "3", "-q", "-O", "/dev/null", "http://example.com/").returncode != 0

    control = next(r for r in engine.containers(p.name) if r.labels.get(f"{LABEL_PREFIX}service") == "control")
    resolved = run_in(engine, p, coder, "getent", "hosts", "control")
    assert resolved.returncode == 0, resolved.stderr
    assert resolved.stdout.split()[0] == control.raw["NetworkSettings"]["Networks"][f"{p.name}_{coder}"]["IPAddress"]

    reviewer_ip = engine.instance(p.name, reviewer).raw["NetworkSettings"]["Networks"][f"{p.name}_{reviewer}"]["IPAddress"]
    assert engine.exec(engine.instance(p.name, reviewer).name, "nc", "-l", "-p", "8080", "-e", "/bin/echo", detach=True).returncode == 0
    # the listener works from its own container, so a failure from the coder is isolation
    assert run_in(engine, p, reviewer, "nc", "-w", "3", reviewer_ip, "8080").returncode == 0
    assert run_in(engine, p, coder, "nc", "-w", "3", reviewer_ip, "8080").returncode != 0


def test_an_instance_runs_unprivileged_as_the_invoking_user_with_limits_and_binds_nothing_from_the_host(sandbox, engine):
    p = sandbox
    coder, small, limited, solo = p.spawn("coder"), p.spawn("small"), p.spawn("limited"), p.spawn("solo")

    assert run_in(engine, p, coder, "sh", "-c", "echo $(id -u):$(id -g)").stdout.strip() == f"{engine.uid}:{engine.gid}"
    status = run_in(engine, p, coder, "grep", "-E", "CapEff|NoNewPrivs", "/proc/self/status").stdout
    assert "CapEff:\t0000000000000000" in status and "NoNewPrivs:\t1" in status

    mounts = engine.instance(p.name, coder).raw["Mounts"]
    assert mounts and all(m["Type"] == "volume" for m in mounts), [(m["Type"], m["Destination"]) for m in mounts]

    default = engine.instance(p.name, coder).raw["HostConfig"]
    assert default["PidsLimit"] and 0 < default["PidsLimit"] <= 100000, "an agent cannot fork without limit"
    host = engine.instance(p.name, small).raw["HostConfig"]
    assert host["PidsLimit"] == 64 and host["Memory"] == host["MemorySwap"] > 0, "swap must not double a memory limit"
    host = engine.instance(p.name, limited).raw["HostConfig"]
    assert host["Memory"] == 256 * 1024 * 1024 and host.get("NanoCpus", host.get("NanoCPUs")) == 1_000_000_000

    assert run_in(engine, p, solo, "pwd").stdout.strip() == "/workspace/scratch", "a single workspace is the working directory"
    assert engine.instance(p.name, solo).labels[f"{LABEL_PREFIX}agent.workspaces"] == "scratch"

    for service in ("control", "proxy"):  # the sidecars: unprivileged, limited, and still working
        sidecar = next(r for r in engine.containers(p.name) if r.labels.get(f"{LABEL_PREFIX}service") == service)
        user = sidecar.raw["Config"]["User"]
        assert user and user.split(":")[0] not in ("0", "root"), f"{service} runs as {user!r}"
        host = sidecar.raw["HostConfig"]
        assert host["Memory"] > 0 and host["PidsLimit"] and host["PidsLimit"] > 0, f"{service} is not limited"
        dropped = set(host["CapDrop"])  # Podman lists the default capabilities it drops instead of ALL
        assert host["ReadonlyRootfs"] and ("ALL" in dropped or {"CAP_CHOWN", "CAP_KILL", "CAP_NET_BIND_SERVICE"} <= dropped)
    p.ok("proxy log")
    p.ok("events")
    p.up()  # a second `up` on existing volumes
    p.spawn("coder")  # and another instance, whose token comes from the same control volume


def test_workspaces_are_volumes_that_instances_share_and_write_as_the_invoking_user(sandbox, engine):
    p = sandbox
    volume = [r for r in engine.resources(p.name) if r.name == p.volume("scratch")]
    assert [(r.kind, r.labels[f"{LABEL_PREFIX}kind"], r.labels[f"{LABEL_PREFIX}service"]) for r in volume] == [("volume", "workspace", "scratch")]
    assert f"{LABEL_PREFIX}instance" not in volume[0].labels, "a directory workspace belongs to the project, not to an instance"
    assert not (p.root / "workspaces").exists(), "nothing is made in the project directory"

    coder, reviewer = p.spawn("coder"), p.spawn("reviewer")
    # looked at from the agent: Podman gives a volume to whichever container mounts it, so a helper container would only see itself
    assert run_in(engine, p, coder, "stat", "-c", "%u:%g", "/workspace/data").stdout.strip() == f"{engine.uid}:{engine.gid}"
    assert run_in(engine, p, coder, "sh", "-c", "echo one > /workspace/data/a").returncode == 0
    both = run_in(engine, p, reviewer, "sh", "-c", "echo two >> /workspace/data/a && cat /workspace/data/a")
    assert both.stdout.split() == ["one", "two"], "two agents share a declared workspace, both writable"
    owner = engine.in_volumes({p.volume("data"): "/w"}, "stat -c %u /w/a")
    assert owner.stdout.strip() == str(engine.uid)

    # removing or stopping an instance leaves the workspace
    p.ok("agent stop", coder)
    p.ok("agent start", coder)
    assert run_in(engine, p, coder, "cat", "/workspace/data/a").stdout.split() == ["one", "two"], "start gives a stopped agent its workspace back"
    p.ok("agent rm --force", coder, reviewer)
    assert engine.volume_exists(p.volume("data")), "rm never deletes a directory workspace"
    assert run_in(engine, p, p.spawn("coder"), "cat", "/workspace/data/a").stdout.split() == ["one", "two"]


def test_a_read_only_workspace_cannot_be_written_by_the_agent(sandbox, engine, tmp_path):
    p = sandbox
    source = tmp_path / "readme.txt"
    source.write_text("from the host")
    p.ok("workspace put docs", source, "readme.txt")
    reader = p.spawn("reader")
    assert run_in(engine, p, reader, "cat", "/workspace/docs/readme.txt").stdout.strip() == "from the host"
    assert run_in(engine, p, reader, "touch", "/workspace/docs/new-file").returncode != 0


def test_a_directory_workspace_survives_down_and_a_new_up_finds_it_and_egzo_leaves_nothing_in_the_project_directory(live_project, engine, agent_image):
    (live_project.root / "prompt.md").write_text("be brief\n")
    document = spec(workspaces={"scratch": {}}, agents={"coder": custom(agent_image, workspaces=["scratch"], prompt="./prompt.md")})
    live_project.write(document)
    before = live_project.left_in_directory()
    live_project.up()
    name = live_project.spawn("coder")
    assert engine.exec(engine.instance(live_project.name, name).name, "sh", "-c", "echo kept > /workspace/scratch/note").returncode == 0
    down = live_project.ok("project down --force")
    assert "scratch" in down.stdout, "down says which workspaces it keeps"
    assert [r.name for r in engine.resources(live_project.name)] == [live_project.volume("scratch")]
    live_project.up()
    name = live_project.spawn("coder")
    assert engine.exec(engine.instance(live_project.name, name).name, "cat", "/workspace/scratch/note").stdout == "kept\n"
    live_project.ok("project down --force")
    assert live_project.left_in_directory() == before


def test_agents_run_under_the_engines_default_runtime_which_is_gvisor_only_on_a_gvisor_platform(sandbox, engine):
    """egzo never picks a runtime: an agent runs under whatever the engine's default is. Proved from inside the agent."""
    p = sandbox
    plain = p.spawn("plain")
    assert ("gvisor" in run_in(engine, p, plain, "cat", "/proc/version").stdout.lower()) == engine.gvisor
    if engine.gvisor:
        assert "Starting gVisor" in run_in(engine, p, plain, "dmesg").stdout
