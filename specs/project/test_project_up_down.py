# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""`egzo project up` and `egzo project down`: reconciliation of the project file with the engine, in one story. `up` makes the
sidecars and publishes the templates and starts no agent; a second one changes nothing; and `down` removes what egzo made."""

from conftest import LABEL_PREFIX
from support import custom, spec


def test_up_makes_the_sidecars_and_no_agent_and_down_removes_everything_but_refuses_while_an_agent_runs(live_project, engine, agent_image):
    p = live_project
    p.write(spec(agents={"coder": custom(agent_image), "reviewer": custom(agent_image)}))
    dry = p.sh("project up --dry-run")
    assert dry.returncode == 1 and "control" in dry.stdout and "would" in dry.stdout
    assert engine.resources(p.name) == [], "a dry run creates nothing"

    p.up()
    assert sorted(r.labels.get(f"{LABEL_PREFIX}kind") for r in engine.containers(p.name)) == ["control", "proxy"]
    assert not [r for r in engine.resources(p.name) if r.kind == "network" and r.name.endswith(("_coder-1", "_coder", "_reviewer"))]
    assert "nothing to do" in p.up().stdout and engine.instances(p.name) == []

    first = p.spawn("coder")
    id_before = engine.instance(p.name, first).raw["Id"]
    p.up()
    p.ok("project up --recreate", timeout=300)
    assert engine.instance(p.name, first).raw["Id"] == id_before, "up does not touch a running instance"

    refused = p.sh("project down")
    assert refused.returncode != 0 and first in refused.stderr and "--force" in refused.stderr
    assert len(engine.containers(p.name)) == 3, "a refused down removes nothing"
    p.ok("stop", first)
    p.ok("project down")
    assert engine.resources(p.name) == [], "down removes everything egzo made, once the agents are stopped"

    p.up()
    p.spawn("coder")
    p.spawn("coder", "issue-7")
    p.ok("project down --force")
    assert engine.resources(p.name) == [], "down --force removes the instances with their networks, the home volumes, the control state and the CA"
    assert p.sh("project down").returncode == 0 and engine.resources(p.name) == [], "down of a project that is not up changes nothing"


def test_a_container_that_claims_the_project_with_labels_egzo_never_writes_is_not_the_projects(live_project, engine, agent_image):
    p = live_project
    p.write(spec(agents={"coder": custom(agent_image)}))
    p.up()
    foreign = f"{p.name}-foreign"
    engine.run("run", "-d", "--name", foreign, "--entrypoint", "sleep", "--label", f"{LABEL_PREFIX}project={p.name}",
               "--label", f"{LABEL_PREFIX}kind=agent", "--label", f"{LABEL_PREFIX}instance=Not Egzo's", agent_image, "600")
    try:
        assert "Not Egzo" not in p.ok("ps").stdout, "no instance egzo makes has this name"
        p.ok("project down")
        assert engine.run("inspect", foreign).returncode == 0, "down removes what egzo made, and only that"
    finally:
        engine.run("rm", "-f", foreign)
