# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""Stale instances: an instance whose template changed since it was spawned.

Stale is judged by the CLI against the templates in egzo.yaml (what `up` would publish); a tool that has no file (the hub) judges
against the templates `up` last published. Spawn always uses the published ones. `ps` marks a stale instance, `project up` refuses
while one exists, and `agent rm --stale` clears them. The project is up for the whole module with templates `coder` and `reviewer`;
a spec changes the file, looks, and after it the file and the instances are as they were."""

import json

import pytest

from conftest import LABEL_PREFIX
from support import custom, spec


class Templates:
    """The project file of the module, with a change that a spec applies and the module takes back."""

    def __init__(self, project, image):
        self.project = project
        self.image = image
        project.mkdir("prompts")
        (project.root / "prompt.md").write_text("be brief\n")
        self.original()
        project.up(self.document())

    def document(self, coder_env="1", reviewer_env=None, prompt=True, egress="example.com", scratch=("scratch",), coder=True):
        agents = {"reviewer": custom(self.image, env={"R": reviewer_env} if reviewer_env else {})}
        if coder:
            agents["coder"] = custom(self.image, env={"V": coder_env}, workspaces=list(scratch), **({"prompt": "./prompt.md"} if prompt else {}))
        workspaces = {name: {} for name in ("scratch", "docs")}
        return spec(egress={"default": {"allow": [egress]}}, workspaces=workspaces, agents=agents)

    def original(self):
        (self.project.root / "prompt.md").write_text("be brief\n")
        self.project.write(self.document())

    def change(self, **fields):
        self.project.write(self.document(**fields))

    def stale(self):
        return sorted(name for name, row in self.project.ps().items() if row["STALE"] == "stale")


@pytest.fixture(scope="module")
def templates(shared_project, engine):
    return Templates(shared_project, engine.agent_image())


@pytest.fixture(autouse=True)
def as_the_module_left_it(templates):
    templates.project.retire()
    templates.original()
    templates.project.up()
    yield


def test_editing_a_template_makes_its_instances_stale_and_up_refuses_until_they_are_removed(templates, engine):
    p = templates.project
    p.spawn("coder")
    p.spawn("coder", "other")
    p.spawn("reviewer")
    assert templates.stale() == [] and "stale" not in p.ok("project up --dry-run").stdout

    templates.change(coder_env="2")
    assert templates.stale() == ["coder-1", "other"], "only the instances of the edited template"
    assert p.ps_json()["coder-1"]["stale"] is True and p.ps_json()["reviewer-1"]["stale"] is False
    dry = p.sh("project up --dry-run")
    assert dry.returncode == 1, dry.stderr
    assert sorted(line for line in dry.stdout.splitlines() if line.startswith("stale:")) == ["stale: coder-1", "stale: other"]

    drift = p.sh("project check", env={"ANTHROPIC_API_KEY": "a", "GITHUB_TOKEN": "b", "DEPLOY_TOKEN": "c"})
    assert drift.returncode == 0, "drift is shown but is not an error"
    assert any(line.startswith("warn") and "up --dry-run" in line for line in drift.stdout.splitlines()), drift.stdout

    before = {c.name: c.raw["Id"] for c in engine.containers(p.name)}
    refused = p.sh("project up", timeout=300)
    assert refused.returncode == 1 and "stale" in refused.stderr and "coder-1" in refused.stderr and "reviewer-1" not in refused.stderr
    assert {c.name: c.raw["Id"] for c in engine.containers(p.name)} == before, "a refused up changes nothing"
    assert p.spawn("coder") == "coder-2"
    env = dict(item.split("=", 1) for item in engine.instance(p.name, "coder-2").raw["Config"]["Env"])
    assert env["V"] == "1", "a refused up publishes nothing: spawn still uses the old template"

    why = p.ok("agent rm --stale --dry-run").stdout
    assert "would remove coder-1" in why and "stale" in why and engine.instance(p.name, "coder-1") is not None
    p.ok("agent rm --stale --yes")
    assert p.instance_names() == ["reviewer-1"], "only the stale ones are cleared"

    p.up()
    assert p.spawn("coder") == "coder-1"
    env = dict(item.split("=", 1) for item in engine.instance(p.name, "coder-1").raw["Config"]["Env"])
    assert env["V"] == "2" and p.ps()["coder-1"]["STALE"] == ""

    # the filters add up: one stale, one stopped, one neither
    p.spawn("reviewer")  # reviewer-2
    p.ok("stop reviewer-1")
    templates.change(coder_env="3")
    p.ok("agent rm --stopped --stale --yes")
    assert p.instance_names() == ["reviewer-2"]


def test_what_makes_an_instance_stale_and_what_does_not(templates, engine):
    p = templates.project
    p.spawn("coder")
    p.spawn("reviewer")

    templates.change(reviewer_env="2")
    assert templates.stale() == ["reviewer-1"], "changing another template does not make coder-1 stale"
    templates.original()

    (p.root / "prompt.md").write_text("be verbose\n")
    assert templates.stale() == ["coder-1"], "a changed prompt"
    (p.root / "prompt.md").write_text("be brief\n")

    templates.change(egress="example.org")
    assert "coder-1" in templates.stale(), "a changed egress profile"
    templates.original()

    templates.change(scratch=("scratch", "docs"))
    assert templates.stale() == ["coder-1"], "a changed list of workspaces, for the template that lists them"
    templates.original()
    assert templates.stale() == []

    templates.change(coder=False)
    assert templates.stale() == ["coder-1"], "an instance of a template that left the file"
    refused = p.sh("project up", timeout=300)
    assert refused.returncode == 1 and "coder-1" in refused.stderr and "reviewer-1" not in refused.stderr
    p.ok("agent rm --stale --yes")
    p.up()
    gone = p.sh("agent spawn coder")
    assert gone.returncode != 0 and "coder" in gone.stderr, "the template is gone"
    assert p.ps()["reviewer-1"]["STALE"] == ""


def test_down_clears_what_was_stale_and_a_name_that_was_an_instance_starts_clean(live_project, engine, agent_image):
    p = live_project
    p.up(spec(agents={"coder": custom(agent_image, env={"V": "1"}, workspaces=[])}, workspaces={}))
    p.spawn("coder", "coder-1")
    p.spawn("coder", "ghost")
    assert engine.exec(engine.instance(p.name, "coder-1").name, "sh", "-c", "echo gone > /tmp/marker").returncode == 0
    p.ok("message send ghost", "unfinished business")
    token = dict(i.split("=", 1) for i in engine.instance(p.name, "ghost").raw["Config"]["Env"])["EGZO_TOKEN"]

    p.write(spec(agents={"coder": custom(agent_image, env={"V": "2"}, workspaces=[])}, workspaces={}))  # coder-1 is stale now
    p.ok("stop control")
    p.ok("project down --force")  # control is not running: nothing could unregister the agents
    assert engine.resources(p.name) == []
    p.up()
    assert engine.instances(p.name) == []
    assert not [r for r in engine.resources(p.name) if r.name.endswith("-home")], "down kept a home volume"

    refused = p.sh("message send coder-1 anyone there?")
    assert refused.returncode != 0 and "coder-1" in refused.stderr, "a name that was an instance no longer takes messages"
    assert p.spawn("coder", "coder-1") == "coder-1"
    p.spawn("coder", "ghost")
    assert engine.exec(engine.instance(p.name, "coder-1").name, "sh", "-c", "test ! -e /tmp/marker").returncode == 0
    assert "unfinished business" not in p.ok("message ls").stdout
    new = dict(i.split("=", 1) for i in engine.instance(p.name, "ghost").raw["Config"]["Env"])
    assert new["EGZO_TOKEN"] != token and new["V"] == "2", "the new instance has a token of its own and the new template"
    assert "stale" not in p.ok("project up --dry-run").stdout
