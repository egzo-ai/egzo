"""Agents: one container each, on their own internal network, joined only by the control sidecar."""

import json

import pytest

from conftest import LABEL_PREFIX
from support import agent, spec


def custom(image, **fields):
    """An agent running any image: the custom harness."""
    return agent(harness="custom", image=image, **fields)


def up(live_project, document):
    live_project.write(document)
    result = live_project.run("up", timeout=300)
    assert result.returncode == 0, result.stderr


def container(engine, project, service):
    found = [r for r in engine.containers(project.name) if r.labels.get(f"{LABEL_PREFIX}service") == service]
    assert len(found) == 1, f"expected one {service} container, found {len(found)}"
    return found[0]


def networks_of(resource):
    return set(resource.raw["NetworkSettings"]["Networks"])


def ip_of(resource, network):
    return resource.raw["NetworkSettings"]["Networks"][network]["IPAddress"]


def network(engine, project, name):
    found = [r for r in engine.resources(project.name) if r.kind == "network" and r.name == f"{project.name}_{name}"]
    assert len(found) == 1, f"no network {project.name}_{name}"
    return found[0]


def test_each_agent_runs_in_its_own_container_with_agent_labels(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image), "reviewer": custom(agent_image)}))
    for name in ("coder", "reviewer"):
        labels = container(engine, live_project, name).labels
        assert labels[f"{LABEL_PREFIX}kind"] == "agent"
        assert labels[f"{LABEL_PREFIX}agent.harness"] == "custom"


def test_each_agent_gets_its_own_internal_network(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image), "reviewer": custom(agent_image)}))
    coder = container(engine, live_project, "coder")
    for name in ("coder", "reviewer"):
        raw = network(engine, live_project, name).raw
        assert raw.get("Internal", raw.get("internal")) is True, name
    assert networks_of(coder) == {f"{live_project.name}_coder"}


def test_the_control_sidecar_joins_every_agent_network(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image), "reviewer": custom(agent_image)}))
    control = container(engine, live_project, "control")
    assert {f"{live_project.name}_coder", f"{live_project.name}_reviewer"} <= networks_of(control)


def test_an_agent_cannot_reach_the_internet(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image)}))
    name = container(engine, live_project, "coder").name
    assert engine.exec(name, "wget", "-T", "3", "-q", "-O", "/dev/null", "http://1.1.1.1/").returncode != 0
    assert engine.exec(name, "wget", "-T", "3", "-q", "-O", "/dev/null", "http://example.com/").returncode != 0


def test_an_agent_reaches_the_control_sidecar_by_name(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image)}))
    name = container(engine, live_project, "coder").name
    control = container(engine, live_project, "control")
    resolved = engine.exec(name, "getent", "hosts", "control")
    assert resolved.returncode == 0, resolved.stderr
    assert resolved.stdout.split()[0] == ip_of(control, f"{live_project.name}_coder")


def test_agents_cannot_reach_each_other(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image), "reviewer": custom(agent_image)}))
    coder = container(engine, live_project, "coder")
    reviewer = container(engine, live_project, "reviewer")
    reviewer_ip = ip_of(reviewer, f"{live_project.name}_reviewer")

    assert engine.exec(reviewer.name, "nc", "-l", "-p", "8080", "-e", "/bin/echo", detach=True).returncode == 0
    # the listener works from its own container, so a failure from the coder is isolation
    assert engine.exec(reviewer.name, "nc", "-w", "3", reviewer_ip, "8080").returncode == 0
    assert engine.exec(coder.name, "nc", "-w", "3", reviewer_ip, "8080").returncode != 0


def test_removing_an_agent_removes_its_container_and_network_only(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image), "reviewer": custom(agent_image)}))
    control_id = container(engine, live_project, "control").raw["Id"]
    reviewer_id = container(engine, live_project, "reviewer").raw["Id"]

    up(live_project, spec(agents={"reviewer": custom(agent_image)}))
    names = {r.name for r in engine.resources(live_project.name)}
    assert f"{live_project.name}_coder" not in names
    assert f"{live_project.name}-coder-1" not in names
    assert container(engine, live_project, "reviewer").raw["Id"] == reviewer_id
    control = container(engine, live_project, "control")
    assert control.raw["Id"] == control_id
    assert f"{live_project.name}_coder" not in networks_of(control)


def test_changing_one_agent_recreates_only_that_agent(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image), "reviewer": custom(agent_image)}))
    before = {name: container(engine, live_project, name).raw["Id"] for name in ("control", "coder", "reviewer")}

    up(live_project, spec(agents={"coder": custom(agent_image, env={"FOO": "bar"}), "reviewer": custom(agent_image)}))
    after = {name: container(engine, live_project, name).raw["Id"] for name in ("control", "coder", "reviewer")}
    assert after["coder"] != before["coder"]
    assert after["reviewer"] == before["reviewer"]
    assert after["control"] == before["control"]


def test_down_removes_the_agent_networks_too(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image)}))
    assert live_project.run("down").returncode == 0
    assert not [r for r in engine.resources(live_project.name) if r.kind in ("container", "network")]


def test_a_shared_workspace_volume_is_shared_between_agents(live_project, engine, agent_image):
    up(
        live_project,
        spec(
            workspaces={"scratch": {}},
            agents={"coder": custom(agent_image, workspaces=["scratch"]), "reviewer": custom(agent_image, workspaces=["scratch"])},
        ),
    )
    coder = container(engine, live_project, "coder").name
    reviewer = container(engine, live_project, "reviewer").name
    assert engine.exec(coder, "sh", "-c", "echo shared > /workspace/scratch/note").returncode == 0
    assert engine.exec(reviewer, "cat", "/workspace/scratch/note").stdout.strip() == "shared"


def test_workspace_volumes_are_labelled_and_survive_down(live_project, engine, agent_image):
    up(live_project, spec(workspaces={"scratch": {}}, agents={"coder": custom(agent_image, workspaces=["scratch"])}))
    volume = [r for r in engine.resources(live_project.name) if r.kind == "volume" and r.name.endswith("_scratch")]
    assert volume and volume[0].labels[f"{LABEL_PREFIX}project"] == live_project.name
    assert live_project.run("down").returncode == 0
    assert [r for r in engine.resources(live_project.name) if r.kind == "volume" and r.name.endswith("_scratch")]


def test_a_single_workspace_is_the_working_directory(live_project, engine, agent_image):
    up(live_project, spec(workspaces={"scratch": {}}, agents={"coder": custom(agent_image, workspaces=["scratch"])}))
    name = container(engine, live_project, "coder").name
    assert engine.exec(name, "pwd").stdout.strip() == "/workspace/scratch"
    assert container(engine, live_project, "coder").labels[f"{LABEL_PREFIX}agent.workspaces"] == "scratch"


def test_a_read_only_host_directory_cannot_be_written(live_project, engine, agent_image):
    docs = live_project.mkdir("docs")
    (docs / "readme.txt").write_text("from the host")
    up(live_project, spec(agents={"coder": custom(agent_image, workspaces=["./docs:ro"])}))
    name = container(engine, live_project, "coder").name
    assert engine.exec(name, "cat", "/workspace/docs/readme.txt").stdout.strip() == "from the host"
    assert engine.exec(name, "touch", "/workspace/docs/new-file").returncode != 0


def test_a_read_write_host_directory_can_be_written(live_project, engine, agent_image):
    notes = live_project.mkdir("notes")
    up(live_project, spec(agents={"coder": custom(agent_image, workspaces=["./notes"])}))
    name = container(engine, live_project, "coder").name
    written = engine.exec(name, "sh", "-c", "echo hi > /workspace/notes/from-agent")
    if written.returncode != 0 and not engine.rootless:
        # rootful engines: root without capabilities cannot write a directory owned by someone else.
        # Which user agents run as, so they can write host directories, is undecided.
        pytest.xfail("agents cannot write host directories on rootful engines yet")
    assert written.returncode == 0, written.stderr
    assert (notes / "from-agent").read_text().strip() == "hi"
    engine.exec(name, "rm", "/workspace/notes/from-agent")


def test_resources_limit_the_agent_container(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image, resources={"cpus": 1, "memory": "256m"})}))
    host = container(engine, live_project, "coder").raw["HostConfig"]
    assert host["Memory"] == 256 * 1024 * 1024
    assert host.get("NanoCpus", host.get("NanoCPUs")) == 1_000_000_000


def test_runtime_runs_the_agent_under_that_oci_runtime(live_project, engine, agent_image):
    if engine.cli != "docker":
        pytest.skip("Podman's Docker-compatible API cannot select the runtime: see the next spec")
    assert engine.has_runtime("runsc"), "gVisor (runsc) must be registered with Docker"
    up(live_project, spec(agents={"coder": custom(agent_image, runtime="runsc")}))
    resource = container(engine, live_project, "coder")
    assert resource.raw["HostConfig"]["Runtime"] == "runsc"
    assert "gvisor" in engine.exec(resource.name, "uname", "-r").stdout


def test_an_unregistered_runtime_fails_with_a_hint_on_docker(live_project, engine, agent_image):
    if engine.cli != "docker":
        pytest.skip("only Docker rejects an unknown runtime")
    live_project.write(spec(agents={"coder": custom(agent_image, runtime="no-such-runtime")}))
    result = live_project.run("up")
    assert result.returncode != 0
    assert "no-such-runtime" in result.stderr and "daemon.json" in result.stderr


def test_podman_warns_that_the_runtime_cannot_be_applied_or_verified(live_project, engine, agent_image):
    if engine.cli != "podman":
        pytest.skip("only Podman ignores the requested runtime")
    live_project.write(spec(agents={"coder": custom(agent_image, runtime="runsc")}))
    result = live_project.run("up", timeout=300)
    assert result.returncode == 0, result.stderr
    assert "warning" in result.stderr.lower()
    assert "runsc" in result.stderr and "podman" in result.stderr.lower() and "containers.conf" in result.stderr


# --- who an agent runs as ---------------------------------------------------------------------


def exec_as(engine, project, name, *command):
    return engine.exec(container(engine, project, name).name, *command)


@pytest.mark.todo("agents run as the invoking user")
def test_agents_run_as_the_invoking_user_not_as_root(live_project, engine, agent_image):
    import os

    up(live_project, spec(agents={"coder": custom(agent_image)}))
    result = exec_as(engine, live_project, "coder", "sh", "-c", "echo $(id -u):$(id -g)")
    assert result.stdout.strip() == f"{os.getuid()}:{os.getgid()}"


@pytest.mark.todo("agents run as the invoking user")
def test_an_agent_writes_a_host_directory_workspace_as_the_invoking_user(live_project, engine, agent_image):
    import os

    shared = live_project.mkdir("data")
    up(live_project, spec(agents={"coder": custom(agent_image, workspaces=["./data"])}))
    written = exec_as(engine, live_project, "coder", "sh", "-c", "echo hi > /workspace/data/from-agent && ls -ln /workspace/data/from-agent")
    assert written.returncode == 0, written.stderr
    assert (shared / "from-agent").read_text() == "hi\n"
    assert (shared / "from-agent").stat().st_uid == os.getuid()


@pytest.mark.todo("agents run as the invoking user")
def test_an_agent_writes_a_declared_volume_workspace(live_project, engine, agent_image):
    up(live_project, spec(workspaces={"scratch": {}}, agents={"coder": custom(agent_image, workspaces=["scratch"])}))
    written = exec_as(engine, live_project, "coder", "sh", "-c", "echo hi > /workspace/scratch/note && cat /workspace/scratch/note")
    assert written.stdout.strip() == "hi", written.stderr


@pytest.mark.todo("agents run as the invoking user")
def test_two_agents_share_a_declared_volume_workspace_both_writable(live_project, engine, agent_image):
    up(live_project, spec(workspaces={"scratch": {}}, agents={
        "coder": custom(agent_image, workspaces=["scratch"]),
        "reviewer": custom(agent_image, workspaces=["scratch"]),
    }))
    assert exec_as(engine, live_project, "coder", "sh", "-c", "echo one > /workspace/scratch/a").returncode == 0
    assert exec_as(engine, live_project, "reviewer", "sh", "-c", "echo two >> /workspace/scratch/a && cat /workspace/scratch/a").stdout.split() == ["one", "two"]


def test_agents_have_no_capabilities_and_cannot_gain_privileges(live_project, engine, agent_image):
    up(live_project, spec(agents={"coder": custom(agent_image)}))
    status = exec_as(engine, live_project, "coder", "grep", "-E", "CapEff|NoNewPrivs", "/proc/self/status").stdout
    assert "CapEff:\t0000000000000000" in status
    assert "NoNewPrivs:\t1" in status
