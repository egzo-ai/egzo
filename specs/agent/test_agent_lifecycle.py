# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""The life of an instance after `egzo agent spawn`: `ps`, `agent rm` with its filters, `start`, `stop`, and what is left of an
instance whose container was removed by hand.

One project is up for the whole module (`sandbox`: templates `coder` and `reviewer` on the spec agent image) and every spec starts
from no instance (what a spec leaves is removed after it). Staleness, which changes the templates, is in test_agent_stale.py."""

import json
import re
import time
from datetime import datetime, timezone

import pytest

from conftest import LABEL_PREFIX
from support import custom, spec

PS_COLUMNS = ["NAME", "SERVICE", "STATE", "HEALTH", "ACTIVITY", "OPEN", "WAITING", "ACTOR", "AGE", "STALE", "PORTS", "STATUS"]


@pytest.fixture(scope="module")
def sandbox(shared_project, engine):
    image = engine.agent_image()
    shared_project.up(spec(agents={"coder": custom(image), "reviewer": custom(image)}))
    return shared_project


@pytest.fixture(autouse=True)
def start_from_no_instance(sandbox):
    sandbox.retire()
    yield


def in_instance(engine, project, name, command):
    return engine.exec(engine.instance(project.name, name).name, "sh", "-c", command)


def leftovers(engine, project, name):
    return [r.name for r in engine.resources(project.name) if r.name.endswith((f"_{name}", f"_{name}-home"))]


def test_ps_lists_the_sidecars_and_the_instances_under_their_template_with_what_each_is_doing(sandbox):
    sandbox.spawn("coder", "issue-1")
    sandbox.spawn("reviewer", "pr-88")
    assert sandbox.ok("ps").stdout.splitlines()[0].split() == PS_COLUMNS

    listing = sandbox.ps()
    assert listing["issue-1"]["SERVICE"] == "coder" and listing["pr-88"]["SERVICE"] == "reviewer"
    assert listing["issue-1"]["ACTOR"] == "operator" and listing["issue-1"]["STATE"] == "running"
    assert listing["issue-1"]["AGE"] and listing["issue-1"]["STALE"] == ""
    assert {"control", "proxy"} <= {row["SERVICE"] for row in listing.values()}

    items = json.loads(sandbox.ok("ps --json").stdout)
    assert sorted(item["kind"] for item in items) == ["agent", "agent", "control", "proxy"]
    expected = {"name", "service", "kind", "state", "health", "activity", "open", "waiting", "status", "actor", "created", "stale"}
    for item in items:
        assert expected <= set(item), item
    one = next(item for item in items if item["name"] == "issue-1")
    assert one["service"] == "coder" and one["state"] == "running" and one["actor"] == "operator" and one["stale"] is False
    assert isinstance(one["open"], int) and isinstance(one["waiting"], bool)
    created = datetime.fromisoformat(one["created"].replace("Z", "+00:00"))
    assert abs((datetime.now(timezone.utc) - created).total_seconds()) < 600

    sandbox.ok("stop issue-1")
    assert sandbox.ps_json()["issue-1"]["state"] == "exited" and sandbox.ps()["issue-1"]["STATE"] == "exited"


def test_rm_removes_an_instance_whole_and_refuses_what_it_should(sandbox, engine):
    for _ in range(3):
        sandbox.spawn("coder")

    refused = sandbox.sh("agent rm coder-1")
    assert refused.returncode != 0 and "--force" in refused.stderr and "coder-1" in refused.stderr, "a running instance needs --force"
    assert engine.instance(sandbox.name, "coder-1").raw["State"]["Running"] is True

    result = sandbox.ok("agent rm --force --json coder-1 coder-3")
    assert sorted(json.loads(result.stdout)["removed"]) == ["coder-1", "coder-3"]
    assert [r.labels[f"{LABEL_PREFIX}instance"] for r in engine.instances(sandbox.name)] == ["coder-2"]
    assert not leftovers(engine, sandbox, "coder-1"), "the network and the home volume go with the container"
    control = next(c for c in engine.containers(sandbox.name) if c.labels.get(f"{LABEL_PREFIX}service") == "control")
    assert f"{sandbox.name}_coder-1" not in control.raw["NetworkSettings"]["Networks"]

    unknown = sandbox.sh("agent rm --force coder-2 ghost")
    assert unknown.returncode != 0 and "ghost" in unknown.stderr
    assert engine.instance(sandbox.name, "coder-2") is not None, "nothing is removed when a name is wrong"

    for name in ("control", "proxy"):
        assert sandbox.sh("agent rm --force", name).returncode != 0, f"{name} is not an instance"
    assert {r.labels[f"{LABEL_PREFIX}kind"] for r in engine.containers(sandbox.name)} >= {"control", "proxy"}

    sandbox.ok("stop coder-2")
    sandbox.ok("agent rm coder-2")  # a stopped instance needs no --force
    assert engine.instance(sandbox.name, "coder-2") is None


def test_a_removed_name_is_spawned_again_with_nothing_of_the_old_instance(sandbox, engine):
    sandbox.spawn("coder", "again")
    assert in_instance(engine, sandbox, "again", "echo old > /tmp/marker").returncode == 0
    first = dict(i.split("=", 1) for i in engine.instance(sandbox.name, "again").raw["Config"]["Env"])["EGZO_TOKEN"]
    first_id = engine.instance(sandbox.name, "again").labels[f"{LABEL_PREFIX}instance-id"]
    sandbox.ok("agent rm --force again")
    sandbox.spawn("coder", "again")
    assert in_instance(engine, sandbox, "again", "test ! -e /tmp/marker").returncode == 0
    second = dict(i.split("=", 1) for i in engine.instance(sandbox.name, "again").raw["Config"]["Env"])["EGZO_TOKEN"]
    assert second != first, "what leaked from the first instance opens the second"
    assert engine.instance(sandbox.name, "again").labels[f"{LABEL_PREFIX}instance-id"] != first_id, "the same name, another instance"


def test_rm_with_a_filter_asks_says_why_and_removes_only_what_matches(sandbox, engine):
    for name in ("s1", "s2"):
        sandbox.spawn("coder", name)

    needs = sandbox.sh("agent rm --yes")
    assert needs.returncode != 0, "a filter is needed"
    assert all(flag in needs.stderr for flag in ("--stopped", "--stale", "--older-than"))
    assert engine.instance(sandbox.name, "s1") is not None

    sandbox.ok("stop s1")
    dry = sandbox.ok("agent rm --stopped --dry-run")
    assert re.search(r"would remove s1 \(.*stopped.*\)", dry.stdout), dry.stdout
    assert "s2" not in dry.stdout and engine.instance(sandbox.name, "s1") is not None

    refused = sandbox.sh("agent rm --stopped", input="n\n")
    assert refused.returncode != 0 and "s1" in refused.stderr + refused.stdout and "[y/N]" in refused.stderr + refused.stdout
    assert engine.instance(sandbox.name, "s1") is not None
    sandbox.ok("agent rm --stopped", input="y\n")
    assert engine.instance(sandbox.name, "s1") is None and engine.instance(sandbox.name, "s2") is not None
    assert not leftovers(engine, sandbox, "s1")

    sandbox.ok("agent rm --stopped", input="")  # nothing to remove: says so, asks nothing
    assert engine.instance(sandbox.name, "s2") is not None

    sandbox.spawn("coder", "s3")
    sandbox.ok("stop s2")
    sandbox.ok("stop s3")
    removed = sandbox.ok("agent rm --stopped --yes --json").stdout
    assert sorted(json.loads(removed)["removed"]) == ["s2", "s3"]

    # --older-than goes by the creation time, and removes a running instance too
    sandbox.spawn("coder", "s4")
    sandbox.ok("agent rm --older-than 24h --yes")
    assert engine.instance(sandbox.name, "s4") is not None
    invalid = sandbox.sh("agent rm --older-than yesterday --yes")
    assert invalid.returncode != 0 and ("older-than" in invalid.stderr or "duration" in invalid.stderr)
    time.sleep(3)
    dry = sandbox.ok("agent rm --older-than 2s --dry-run")
    assert re.search(r"would remove s4 \(.*(older|age).*\)", dry.stdout), dry.stdout
    sandbox.ok("agent rm --older-than 2s --yes")
    assert engine.instance(sandbox.name, "s4") is None


def test_stop_and_start_keep_an_instance_and_what_is_left_after_a_container_is_removed_by_hand(sandbox, engine):
    sandbox.spawn("coder")
    before = engine.instance(sandbox.name, "coder-1").raw["Id"]
    sandbox.ok("stop coder-1")
    sandbox.ok("start coder-1", timeout=300)
    assert engine.instance(sandbox.name, "coder-1").raw["Id"] == before, "the same container comes back"
    sandbox.ok("message send coder-1", "still registered")

    sandbox.ok("stop coder-1")
    sandbox.up()
    after = engine.instance(sandbox.name, "coder-1")
    assert after.raw["Id"] == before and after.raw["State"]["Running"] is False, "up does not recreate a stopped instance"

    # the container is removed by hand: its network is left, and the name stays taken
    engine.run("rm", "-f", sandbox.container("coder-1"))
    assert engine.instance(sandbox.name, "coder-1") is None and leftovers(engine, sandbox, "coder-1")
    taken = sandbox.sh("agent spawn coder coder-1")
    assert taken.returncode == 17 and "coder-1" in taken.stderr
    sandbox.ok("agent rm --stopped --yes")
    assert not leftovers(engine, sandbox, "coder-1"), "a prune clears what a removed container left behind"

    sandbox.spawn("coder", "coder-1")
    engine.run("rm", "-f", sandbox.container("coder-1"))
    sandbox.ok("agent rm coder-1")
    assert not leftovers(engine, sandbox, "coder-1")
    assert sandbox.sh("message send coder-1 hi").returncode != 0, "no longer registered with control"
    assert sandbox.spawn("coder", "coder-1") == "coder-1", "the name is free again"


def test_agent_ls_lists_the_instances_and_not_the_sidecars_and_rm_takes_names_or_filters(sandbox, engine):
    assert sandbox.instance_names() == []
    sandbox.spawn("coder")
    sandbox.spawn("coder", "issue-7")
    assert sandbox.instance_names() == ["coder-1", "issue-7"]
    assert [row["name"] for row in json.loads(sandbox.ok("agent ls --json").stdout)] == ["coder-1", "issue-7"]
    assert any(name.endswith("control-1") for name in sandbox.ps()) and any(name.endswith("proxy-1") for name in sandbox.ps())

    neither = sandbox.sh("agent rm")
    assert neither.returncode != 0 and "--stale" in neither.stderr
    both = sandbox.sh("agent rm coder-1 --stopped")
    assert both.returncode != 0 and "not both" in both.stderr
    assert sandbox.instance_names() == ["coder-1", "issue-7"]

    sandbox.ok("agent stop coder-1")
    assert engine.instance(sandbox.name, "coder-1").raw["State"]["Running"] is False
    sandbox.ok("agent start coder-1")
    assert engine.instance(sandbox.name, "coder-1").raw["State"]["Running"] is True
