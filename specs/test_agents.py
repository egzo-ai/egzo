# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""Agents: one container each, on their own internal network, joined only by the control sidecar."""

import json

import pytest

from conftest import LABEL_PREFIX
from support import agent, spec


def custom(image, **fields):
    """An agent running any image: the custom harness."""
    return agent(harness="custom", image=image, **fields)


def up(live_project, document):
    """`up`, then one instance of every template in the document: `coder-1`, `reviewer-1`, ..."""
    live_project.up(document)
    for template in document.get("agents", {}):
        live_project.spawn(template)


def container(engine, project, name):
    """The container of an instance (`coder-1`) or of a sidecar (`control`, `proxy`)."""
    found = [
        r for r in engine.containers(project.name)
        if r.labels.get(f"{LABEL_PREFIX}instance") == name
        or (r.labels.get(f"{LABEL_PREFIX}kind") != "agent" and r.labels.get(f"{LABEL_PREFIX}service") == name)
    ]
    assert len(found) == 1, f"expected one {name} container, found {len(found)}"
    return found[0]


def networks_of(resource):
    return set(resource.raw["NetworkSettings"]["Networks"])


def ip_of(resource, network):
    return resource.raw["NetworkSettings"]["Networks"][network]["IPAddress"]


def network(engine, project, name):
    found = [r for r in engine.resources(project.name) if r.kind == "network" and r.name == f"{project.name}_{name}"]
    assert len(found) == 1, f"no network {project.name}_{name}"
    return found[0]


def test_each_agent_gets_its_own_internal_network(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image), "reviewer": custom(agent_image)}))
    coder = container(engine, live_project, "coder-1")
    for name in ("coder-1", "reviewer-1"):
        raw = network(engine, live_project, name).raw
        assert raw.get("Internal", raw.get("internal")) is True, name
    assert networks_of(coder) == {f"{live_project.name}_coder-1"}


def test_an_agent_cannot_reach_the_internet(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image)}))
    name = container(engine, live_project, "coder-1").name
    assert engine.exec(name, "wget", "-T", "3", "-q", "-O", "/dev/null", "http://1.1.1.1/").returncode != 0
    assert engine.exec(name, "wget", "-T", "3", "-q", "-O", "/dev/null", "http://example.com/").returncode != 0


def test_an_agent_reaches_the_control_sidecar_by_name(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image)}))
    name = container(engine, live_project, "coder-1").name
    control = container(engine, live_project, "control")
    resolved = engine.exec(name, "getent", "hosts", "control")
    assert resolved.returncode == 0, resolved.stderr
    assert resolved.stdout.split()[0] == ip_of(control, f"{live_project.name}_coder-1")


def test_agents_cannot_reach_each_other(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image), "reviewer": custom(agent_image)}))
    coder = container(engine, live_project, "coder-1")
    reviewer = container(engine, live_project, "reviewer-1")
    reviewer_ip = ip_of(reviewer, f"{live_project.name}_reviewer-1")

    assert engine.exec(reviewer.name, "nc", "-l", "-p", "8080", "-e", "/bin/echo", detach=True).returncode == 0
    # the listener works from its own container, so a failure from the coder is isolation
    assert engine.exec(reviewer.name, "nc", "-w", "3", reviewer_ip, "8080").returncode == 0
    assert engine.exec(coder.name, "nc", "-w", "3", reviewer_ip, "8080").returncode != 0


def test_down_removes_the_agent_networks_too(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image)}))
    assert live_project.sh("project down --force").returncode == 0
    assert not [r for r in engine.resources(live_project.name) if r.kind in ("container", "network")]


def test_a_directory_workspace_is_a_volume_made_by_up_owned_by_the_invoking_user(live_project, engine, agent_image):
    import os

    live_project.up(spec(workspaces={"scratch": {}}, agents={"coder": custom(agent_image, workspaces=["scratch"])}))
    volume = [r for r in engine.resources(live_project.name) if r.name == live_project.volume("scratch")]
    assert [(r.kind, r.labels[f"{LABEL_PREFIX}kind"], r.labels[f"{LABEL_PREFIX}service"]) for r in volume] == [("volume", "workspace", "scratch")]
    assert f"{LABEL_PREFIX}instance" not in volume[0].labels, "a directory workspace belongs to the project, not to an instance"
    owner = engine.in_volumes({live_project.volume("scratch"): "/w"}, "stat -c %u:%g /w")
    assert owner.stdout.strip() == f"{os.getuid()}:{os.getgid()}"
    assert not (live_project.root / "workspaces").exists(), "nothing is made in the project directory"


def test_a_directory_workspace_survives_down_and_a_new_up_finds_it(live_project, engine, agent_image):
    document = spec(workspaces={"scratch": {}}, agents={"coder": custom(agent_image, workspaces=["scratch"])})
    up(live_project, document)
    assert exec_as(engine, live_project, "coder-1", "sh", "-c", "echo kept > /workspace/scratch/note").returncode == 0
    down = live_project.sh("project down --force")
    assert down.returncode == 0, down.stderr
    assert "scratch" in down.stdout, "down says which workspaces it keeps"
    assert [r.name for r in engine.resources(live_project.name)] == [live_project.volume("scratch")]
    up(live_project, document)
    assert exec_as(engine, live_project, "coder-1", "cat", "/workspace/scratch/note").stdout == "kept\n"


def test_rm_never_deletes_a_directory_workspace(live_project, engine, agent_image):
    up(live_project, spec(workspaces={"data": {}}, agents={"coder": custom(agent_image, workspaces=["data"])}))
    assert exec_as(engine, live_project, "coder-1", "sh", "-c", "echo mine > /workspace/data/f").returncode == 0
    assert live_project.sh("agent rm --force coder-1").returncode == 0
    assert engine.volume_exists(live_project.volume("data"))
    assert live_project.spawn("coder") == "coder-1"
    assert exec_as(engine, live_project, "coder-1", "cat", "/workspace/data/f").stdout == "mine\n"


def test_agent_start_gives_a_stopped_agent_back_the_workspace_it_had(live_project, engine, agent_image):
    up(live_project, spec(workspaces={"data": {}}, agents={"coder": custom(agent_image, workspaces=["data"])}))
    assert exec_as(engine, live_project, "coder-1", "sh", "-c", "echo mine > /workspace/data/f").returncode == 0
    assert live_project.sh("agent stop coder-1").returncode == 0
    started = live_project.sh("agent start coder-1")
    assert started.returncode == 0, started.stderr
    assert exec_as(engine, live_project, "coder-1", "cat", "/workspace/data/f").stdout == "mine\n"


def test_a_read_only_workspace_cannot_be_written_by_the_agent(live_project, engine, agent_image):
    live_project.up(spec(workspaces={"docs": {}}, agents={"coder": custom(agent_image, workspaces=["docs:ro"])}))
    assert live_project.run("workspace", "put", "docs", str(_file(live_project, "readme.txt", "from the host")), "readme.txt").returncode == 0
    live_project.spawn("coder")
    name = container(engine, live_project, "coder-1").name
    assert engine.exec(name, "cat", "/workspace/docs/readme.txt").stdout.strip() == "from the host"
    assert engine.exec(name, "touch", "/workspace/docs/new-file").returncode != 0


def _file(project, name, text):
    path = project.root / "inbox" / name
    path.parent.mkdir(exist_ok=True)
    path.write_text(text)
    return path


def test_an_agent_container_binds_nothing_from_the_host(live_project, engine, agent_image):
    (live_project.root / "prompt.md").write_text("be brief\n")
    up(live_project, spec(workspaces={"data": {}}, agents={"coder": custom(agent_image, workspaces=["data"], prompt="./prompt.md")}))
    mounts = container(engine, live_project, "coder-1").raw["Mounts"]
    assert mounts and all(m["Type"] == "volume" for m in mounts), [(m["Type"], m["Destination"]) for m in mounts]


def test_egzo_leaves_nothing_in_the_project_directory(live_project, engine, agent_image):
    """Everything egzo makes is in the engine, so a project directory is only what the user wrote."""
    (live_project.root / "prompt.md").write_text("be brief\n")
    document = spec(workspaces={"data": {}}, agents={"coder": custom(agent_image, workspaces=["data"], prompt="./prompt.md")})
    live_project.write(document)
    before = live_project.left_in_directory()
    up(live_project, document)
    live_project.spawn("coder", "second")
    assert live_project.sh("agent rm --force second").returncode == 0
    assert live_project.sh("project down --force").returncode == 0
    assert live_project.left_in_directory() == before


def test_a_single_workspace_is_the_working_directory(live_project, engine, agent_image):
    up(live_project, spec(workspaces={"scratch": {}}, agents={"coder": custom(agent_image, workspaces=["scratch"])}))
    name = container(engine, live_project, "coder-1").name
    assert engine.exec(name, "pwd").stdout.strip() == "/workspace/scratch"
    assert container(engine, live_project, "coder-1").labels[f"{LABEL_PREFIX}agent.workspaces"] == "scratch"


def test_resources_limit_the_agent_container(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image, resources={"cpus": 1, "memory": "256m"})}))
    host = container(engine, live_project, "coder-1").raw["HostConfig"]
    assert host["Memory"] == 256 * 1024 * 1024
    assert host.get("NanoCpus", host.get("NanoCPUs")) == 1_000_000_000


def no_agent_container(engine, project):
    return not [r for r in engine.containers(project.name) if r.labels.get(f"{LABEL_PREFIX}kind") == "agent"]


def test_an_agent_with_runtime_runsc_really_runs_under_gvisor(live_project, engine, agent_image, gvisor_agents):
    """Proved from inside the agent, not from what egzo asked for: an engine that drops the runtime
    would still say `runsc` in `inspect`."""
    up(live_project, spec(agents={"coder": custom(agent_image, runtime="runsc")}))
    name = container(engine, live_project, "coder-1").name
    assert "gvisor" in engine.exec(name, "cat", "/proc/version").stdout.lower()
    assert "Starting gVisor" in engine.exec(name, "dmesg").stdout


def test_an_agent_without_a_runtime_does_not_run_under_gvisor(live_project, engine, agent_image):
    """The control for the spec above: on a platform that has gVisor, agents only get it when they ask."""
    up(live_project, spec(agents={"coder": custom(agent_image)}))
    name = container(engine, live_project, "coder-1").name
    assert "gvisor" not in engine.exec(name, "cat", "/proc/version").stdout.lower()


def spawn_refused(live_project, template="coder"):
    live_project.up()
    return live_project.run("agent", "spawn", template, timeout=300)


def test_a_project_that_requires_gvisor_cannot_start_an_agent_on_a_platform_without_it(live_project, engine, agent_image):
    if engine.gvisor:
        pytest.skip(f"{engine.name} has gVisor; this spec is about platforms without it")
    live_project.write(spec(agents={"coder": custom(agent_image, runtime="runsc")}))
    result = spawn_refused(live_project)
    assert result.returncode != 0, "the agent started without the runtime it asked for: this host has gVisor or egzo dropped it"
    assert "runsc" in result.stderr
    assert no_agent_container(engine, live_project)


def test_an_unknown_runtime_never_starts_the_agent(live_project, engine, agent_image):
    live_project.write(spec(agents={"coder": custom(agent_image, runtime="no-such-runtime")}))
    result = spawn_refused(live_project)
    assert result.returncode != 0
    assert "no-such-runtime" in result.stderr
    assert no_agent_container(engine, live_project)
    assert not [r for r in engine.resources(live_project.name) if r.name.endswith("_coder-1")], "a failed spawn left its network behind"
    if engine.cli == "docker":
        assert "daemon.json" in result.stderr  # how to register a runtime


def test_podman_refuses_a_runtime_because_it_cannot_apply_or_verify_it(live_project, engine, agent_image):
    """Podman's Docker-compatible API cannot select a runtime: egzo must not start the agent without it."""
    if engine.cli != "podman":
        pytest.skip("this is about Podman")
    live_project.write(spec(agents={"coder": custom(agent_image, runtime="runsc")}))
    result = spawn_refused(live_project)
    assert result.returncode != 0
    assert "Podman" in result.stderr and "runsc" in result.stderr
    assert no_agent_container(engine, live_project)


# --- who an agent runs as ---------------------------------------------------------------------


def exec_as(engine, project, name, *command):
    return engine.exec(container(engine, project, name).name, *command)


def test_agents_run_as_the_invoking_user_not_as_root(live_project, engine, agent_image):
    import os

    up(live_project, spec(agents={"coder": custom(agent_image)}))
    result = exec_as(engine, live_project, "coder-1", "sh", "-c", "echo $(id -u):$(id -g)")
    assert result.stdout.strip() == f"{os.getuid()}:{os.getgid()}"


def test_an_agent_writes_a_workspace_as_the_invoking_user(live_project, engine, agent_image):
    import os

    up(live_project, spec(workspaces={"data": {}}, agents={"coder": custom(agent_image, workspaces=["data"])}))
    written = exec_as(engine, live_project, "coder-1", "sh", "-c", "echo hi > /workspace/data/from-agent && ls -ln /workspace/data/from-agent")
    assert written.returncode == 0, written.stderr
    owner = engine.in_volumes({live_project.volume("data"): "/w"}, "cat /w/from-agent && stat -c %u /w/from-agent")
    assert owner.stdout.split() == ["hi", str(os.getuid())]


def test_two_agents_share_a_declared_workspace_both_writable(live_project, engine, agent_image):
    up(live_project, spec(workspaces={"scratch": {}}, agents={
        "coder": custom(agent_image, workspaces=["scratch"]),
        "reviewer": custom(agent_image, workspaces=["scratch"]),
    }))
    assert exec_as(engine, live_project, "coder-1", "sh", "-c", "echo one > /workspace/scratch/a").returncode == 0
    assert exec_as(engine, live_project, "reviewer-1", "sh", "-c", "echo two >> /workspace/scratch/a && cat /workspace/scratch/a").stdout.split() == ["one", "two"]


def test_agents_have_no_capabilities_and_cannot_gain_privileges(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image)}))
    status = exec_as(engine, live_project, "coder-1", "grep", "-E", "CapEff|NoNewPrivs", "/proc/self/status").stdout
    assert "CapEff:\t0000000000000000" in status
    assert "NoNewPrivs:\t1" in status


# --- containers are limited and the sidecars are not root --------------------------------------------------------


def test_the_sidecars_run_unprivileged_with_limits(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image)}))
    for service in ("control", "proxy"):
        raw = container(engine, live_project, service).raw
        user = raw["Config"]["User"]
        assert user and user.split(":")[0] not in ("0", "root"), f"{service} runs as {user!r}"
        host = raw["HostConfig"]
        assert host["Memory"] > 0 and host["PidsLimit"] and host["PidsLimit"] > 0, f"{service} is not limited"
        assert host["ReadonlyRootfs"] and "ALL" in host["CapDrop"]


def test_an_agent_cannot_fork_without_limit(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image), "small": custom(agent_image, resources={"pids": 64, "memory": "256m"})}))
    default = container(engine, live_project, "coder-1").raw["HostConfig"]
    assert default["PidsLimit"] and 0 < default["PidsLimit"] <= 100000
    small = container(engine, live_project, "small-1").raw["HostConfig"]
    assert small["PidsLimit"] == 64
    assert small["Memory"] == small["MemorySwap"] > 0, "swap must not double a memory limit"


def test_the_sidecars_still_work_unprivileged(live_project, engine, agent_image):
    """The control and proxy volumes belong to the sidecars' user: up converges, ps answers, a token is issued."""
    up(live_project, spec(agents={"coder": custom(agent_image)}))
    assert live_project.sh("ps").returncode == 0
    assert live_project.sh("proxy log").returncode == 0
    assert live_project.sh("events").returncode == 0
    live_project.up()  # a second `up` on existing volumes
    live_project.spawn("coder")  # and a second instance, whose token comes from the same control volume
