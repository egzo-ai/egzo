"""The life of an instance after spawn: ps, rm, prune and stale instances.

An instance whose template changed since it was spawned is stale: `ps` marks it, `up` refuses while one exists,
and `prune --stale` or `rm` clears it. egzo has no reaper: `prune` and `ps --json` are what a script or a timer
uses to retire instances.
"""

import json
import re
import time
from datetime import datetime, timezone

import pytest

from conftest import LABEL_PREFIX
from support import agent, spec, table

pytestmark = pytest.mark.usefixtures("engine")


def custom(image, **fields):
    return agent(harness="custom", image=image, **fields)


def started(project, image, *templates, **named):
    """`up` with the given templates (name -> env) and no instance."""
    agents = {name: custom(image, env=env) for name, env in {**{t: {} for t in templates}, **named}.items()}
    project.up(spec(agents=agents or {"coder": custom(image)}))
    return project


def rows(project, *args):
    result = project.run("ps", *args)
    assert result.returncode == 0, result.stderr
    return {row["NAME"]: row for row in table(result.stdout)}


def instances_json(project):
    result = project.run("ps", "--json")
    assert result.returncode == 0, result.stderr
    return {item["name"]: item for item in json.loads(result.stdout)}


# --- ps ---------------------------------------------------------------------------------------------------------------


def test_ps_has_the_instance_columns(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder")
    header = live_project.run("ps").stdout.splitlines()[0].split()
    assert header == ["NAME", "SERVICE", "STATE", "HEALTH", "ACTIVITY", "OPEN", "WAITING", "ACTOR", "AGE", "STALE", "STATUS"]


def test_ps_groups_an_instance_under_its_template_and_shows_who_made_it(live_project, engine, agent_image):
    started(live_project, agent_image, "coder", "reviewer")
    live_project.spawn("coder")
    live_project.spawn("reviewer", "pr-88")
    listing = rows(live_project)
    assert listing["coder-1"]["SERVICE"] == "coder" and listing["pr-88"]["SERVICE"] == "reviewer"
    assert listing["coder-1"]["ACTOR"] == "operator"
    assert listing["coder-1"]["STATE"] == "running"
    assert listing["coder-1"]["AGE"] and listing["coder-1"]["STALE"] == ""
    assert {"control", "proxy"} <= {row["SERVICE"] for row in listing.values()}


def test_ps_json_is_an_array_with_one_object_per_container(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder", "issue-1")
    items = json.loads(live_project.run("ps", "--json").stdout)
    assert isinstance(items, list)
    kinds = sorted(item["kind"] for item in items)
    assert kinds == ["agent", "control", "proxy"]
    expected = {"name", "service", "kind", "state", "health", "activity", "open", "waiting", "status", "actor", "created", "stale"}
    for item in items:
        assert expected <= set(item), item
    [one] = [item for item in items if item["kind"] == "agent"]
    assert one["name"] == "issue-1" and one["service"] == "coder" and one["state"] == "running"
    assert one["actor"] == "operator" and one["stale"] is False
    assert isinstance(one["open"], int) and isinstance(one["waiting"], bool)
    created = datetime.fromisoformat(one["created"].replace("Z", "+00:00"))
    assert abs((datetime.now(timezone.utc) - created).total_seconds()) < 600


def test_ps_json_of_a_project_without_instances_lists_the_sidecars(live_project, engine, agent_image):
    started(live_project, agent_image)
    assert sorted(item["kind"] for item in json.loads(live_project.run("ps", "--json").stdout)) == ["control", "proxy"]


def test_ps_shows_a_stopped_instance_as_exited(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder")
    assert live_project.run("stop", "coder-1").returncode == 0
    assert instances_json(live_project)["coder-1"]["state"] == "exited"
    assert rows(live_project)["coder-1"]["STATE"] == "exited"


# --- rm ---------------------------------------------------------------------------------------------------------------


def test_rm_removes_the_container_the_network_and_the_home_volume(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder")
    result = live_project.run("rm", "--force", "coder-1")
    assert result.returncode == 0, result.stderr
    assert engine.instance(live_project.name, "coder-1") is None
    assert not [r for r in engine.resources(live_project.name) if r.name.endswith("_coder-1") or r.name.endswith("_coder-1-home")]
    sidecar = [r for r in engine.containers(live_project.name) if r.labels.get(f"{LABEL_PREFIX}service") == "control"][0]
    assert f"{live_project.name}_coder-1" not in sidecar.raw["NetworkSettings"]["Networks"]


def test_a_running_instance_needs_force(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder")
    refused = live_project.run("rm", "coder-1")
    assert refused.returncode != 0
    assert "--force" in refused.stderr and "coder-1" in refused.stderr
    assert engine.instance(live_project.name, "coder-1").raw["State"]["Running"] is True


def test_a_stopped_instance_is_removed_without_force(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder")
    assert live_project.run("stop", "coder-1").returncode == 0
    assert live_project.run("rm", "coder-1").returncode == 0
    assert engine.instance(live_project.name, "coder-1") is None


def test_rm_takes_several_names_and_reports_them_as_json(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder")
    live_project.spawn("coder")
    live_project.spawn("coder")
    result = live_project.run("rm", "--force", "--json", "coder-1", "coder-3")
    assert result.returncode == 0, result.stderr
    assert sorted(json.loads(result.stdout)["removed"]) == ["coder-1", "coder-3"]
    assert [r.labels[f"{LABEL_PREFIX}instance"] for r in engine.instances(live_project.name)] == ["coder-2"]


def test_rm_of_an_unknown_name_is_an_error_that_removes_nothing(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder")
    result = live_project.run("rm", "--force", "coder-1", "ghost")
    assert result.returncode != 0
    assert "ghost" in result.stderr
    assert engine.instance(live_project.name, "coder-1") is not None, "nothing is removed when a name is wrong"


def test_rm_never_removes_a_sidecar(live_project, engine, agent_image):
    started(live_project, agent_image)
    for name in ("control", "proxy"):
        result = live_project.run("rm", "--force", name)
        assert result.returncode != 0, name
    assert {r.labels[f"{LABEL_PREFIX}kind"] for r in engine.containers(live_project.name)} == {"control", "proxy"}


def test_a_removed_name_can_be_spawned_again_with_a_fresh_home(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder", "again")
    assert in_home(engine, live_project, "again", "echo old > $HOME/marker || echo old > /tmp/marker").returncode == 0
    assert live_project.run("rm", "--force", "again").returncode == 0
    live_project.spawn("coder", "again")
    assert in_home(engine, live_project, "again", "test ! -e $HOME/marker").returncode == 0


def in_home(engine, project, name, command):
    return engine.exec(engine.instance(project.name, name).name, "sh", "-c", command)


# --- prune ------------------------------------------------------------------------------------------------------------


def test_prune_needs_a_filter(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder")
    result = live_project.run("prune", "--yes")
    assert result.returncode != 0
    assert "--stopped" in result.stderr and "--stale" in result.stderr and "--older-than" in result.stderr
    assert engine.instance(live_project.name, "coder-1") is not None


def test_prune_stopped_removes_only_stopped_instances(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder")
    live_project.spawn("coder")
    assert live_project.run("stop", "coder-1").returncode == 0
    result = live_project.run("prune", "--stopped", "--yes")
    assert result.returncode == 0, result.stderr
    assert engine.instance(live_project.name, "coder-1") is None
    assert engine.instance(live_project.name, "coder-2") is not None
    assert not [r for r in engine.resources(live_project.name) if r.name.endswith(("_coder-1", "_coder-1-home"))]


def test_prune_dry_run_lists_with_the_reason_and_changes_nothing(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder")
    live_project.spawn("coder")
    assert live_project.run("stop", "coder-1").returncode == 0
    result = live_project.run("prune", "--stopped", "--dry-run")
    assert result.returncode == 0, result.stderr
    assert re.search(r"would remove coder-1 \(.*stopped.*\)", result.stdout), result.stdout
    assert "coder-2" not in result.stdout
    assert engine.instance(live_project.name, "coder-1") is not None


def test_prune_asks_first_and_a_no_removes_nothing(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder")
    assert live_project.run("stop", "coder-1").returncode == 0
    refused = live_project.run("prune", "--stopped", input="n\n")
    assert refused.returncode != 0
    assert "coder-1" in refused.stderr + refused.stdout and "[y/N]" in refused.stderr + refused.stdout
    assert engine.instance(live_project.name, "coder-1") is not None
    accepted = live_project.run("prune", "--stopped", input="y\n")
    assert accepted.returncode == 0, accepted.stderr
    assert engine.instance(live_project.name, "coder-1") is None


def test_prune_with_nothing_to_remove_says_so_and_asks_nothing(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder")
    result = live_project.run("prune", "--stopped", input="")
    assert result.returncode == 0, result.stderr
    assert engine.instance(live_project.name, "coder-1") is not None


def test_prune_older_than_goes_by_the_creation_time(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder")
    assert live_project.run("prune", "--older-than", "24h", "--yes").returncode == 0
    assert engine.instance(live_project.name, "coder-1") is not None
    time.sleep(3)
    result = live_project.run("prune", "--older-than", "2s", "--yes")
    assert result.returncode == 0, result.stderr
    assert engine.instance(live_project.name, "coder-1") is None


def test_prune_older_than_removes_even_a_running_instance(live_project, engine, agent_image):
    """Age is a reason of its own: a script that retires week-old instances does not care whether they run."""
    started(live_project, agent_image)
    live_project.spawn("coder")
    time.sleep(3)
    dry = live_project.run("prune", "--older-than", "2s", "--dry-run")
    assert re.search(r"would remove coder-1 \(.*(older|age).*\)", dry.stdout), dry.stdout


def test_prune_older_than_needs_a_duration(live_project, engine, agent_image):
    started(live_project, agent_image)
    result = live_project.run("prune", "--older-than", "yesterday", "--yes")
    assert result.returncode != 0
    assert "older-than" in result.stderr or "duration" in result.stderr


def test_the_filters_of_prune_add_up(live_project, engine, agent_image):
    live_project.up(spec(agents={"coder": custom(agent_image, env={"V": "1"}), "reviewer": custom(agent_image)}))
    live_project.spawn("coder")  # will be stale
    live_project.spawn("reviewer")  # will be stopped
    live_project.spawn("reviewer")  # neither
    assert live_project.run("stop", "reviewer-1").returncode == 0
    live_project.write(spec(agents={"coder": custom(agent_image, env={"V": "2"}), "reviewer": custom(agent_image)}))
    assert live_project.run("prune", "--stopped", "--stale", "--yes").returncode == 0
    assert {r.labels[f"{LABEL_PREFIX}instance"] for r in engine.instances(live_project.name)} == {"reviewer-2"}


def test_prune_json_lists_what_it_removed(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder")
    live_project.spawn("coder")
    assert live_project.run("stop", "coder-1").returncode == 0
    assert live_project.run("stop", "coder-2").returncode == 0
    result = live_project.run("prune", "--stopped", "--yes", "--json")
    assert result.returncode == 0, result.stderr
    assert sorted(json.loads(result.stdout)["removed"]) == ["coder-1", "coder-2"]


# --- stale instances -------------------------------------------------------------------------------------------------
#
# Stale is judged by the CLI against the templates in egzo.yaml (what `up` would publish); a tool that has no file
# (the hub) judges against the templates `up` last published. Spawn always uses the published ones.


def make_stale(project, image):
    """One instance, `coder-1`, of a template that is then edited in the file but not published."""
    project.up(spec(agents={"coder": custom(image, env={"V": "1"})}))
    project.spawn("coder")
    project.write(spec(agents={"coder": custom(image, env={"V": "2"})}))


def test_an_instance_is_not_stale_while_its_template_is_unchanged(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder")
    assert rows(live_project)["coder-1"]["STALE"] == ""
    assert "stale" not in live_project.run("up", "--dry-run").stdout


def test_editing_a_template_makes_its_instances_stale_at_once(live_project, engine, agent_image):
    make_stale(live_project, agent_image)
    assert rows(live_project)["coder-1"]["STALE"] == "stale"
    assert instances_json(live_project)["coder-1"]["stale"] is True


def test_up_refuses_while_an_instance_is_stale_and_names_it(live_project, engine, agent_image):
    make_stale(live_project, agent_image)
    before = {c.name: c.raw["Id"] for c in engine.containers(live_project.name)}
    result = live_project.run("up", timeout=300)
    assert result.returncode == 1
    assert "stale" in result.stderr and "coder-1" in result.stderr
    assert {c.name: c.raw["Id"] for c in engine.containers(live_project.name)} == before, "a refused up changes nothing"
    assert live_project.spawn("coder") == "coder-2"
    env = dict(item.split("=", 1) for item in engine.instance(live_project.name, "coder-2").raw["Config"]["Env"])
    assert env["V"] == "1", "a refused up publishes nothing: spawn still uses the old template"


def test_up_dry_run_prints_a_stale_line_for_each_stale_instance_and_exits_zero(live_project, engine, agent_image):
    make_stale(live_project, agent_image)
    live_project.spawn("coder", "other")
    result = live_project.run("up", "--dry-run")
    assert result.returncode == 0, result.stderr
    assert sorted(line for line in result.stdout.splitlines() if line.startswith("stale:")) == ["stale: coder-1", "stale: other"]


def test_removing_the_stale_instances_lets_up_publish_the_new_template(live_project, engine, agent_image):
    make_stale(live_project, agent_image)
    assert live_project.run("rm", "--force", "coder-1").returncode == 0
    live_project.up()
    live_project.spawn("coder")
    env = dict(item.split("=", 1) for item in engine.instance(live_project.name, "coder-1").raw["Config"]["Env"])
    assert env["V"] == "2"
    assert rows(live_project)["coder-1"]["STALE"] == ""


def test_prune_stale_clears_them_and_only_them(live_project, engine, agent_image):
    live_project.up(spec(agents={"coder": custom(agent_image, env={"V": "1"}), "reviewer": custom(agent_image)}))
    live_project.spawn("coder")
    live_project.spawn("reviewer")
    live_project.write(spec(agents={"coder": custom(agent_image, env={"V": "2"}), "reviewer": custom(agent_image)}))
    result = live_project.run("prune", "--stale", "--yes")
    assert result.returncode == 0, result.stderr
    assert [r.labels[f"{LABEL_PREFIX}instance"] for r in engine.instances(live_project.name)] == ["reviewer-1"]
    live_project.up()  # nothing stale is left
    assert live_project.spawn("coder") == "coder-1"


def test_prune_stale_dry_run_says_why(live_project, engine, agent_image):
    make_stale(live_project, agent_image)
    result = live_project.run("prune", "--stale", "--dry-run")
    assert re.search(r"would remove coder-1 \(.*stale.*\)", result.stdout), result.stdout
    assert engine.instance(live_project.name, "coder-1") is not None


def test_an_instance_of_a_template_that_left_the_file_is_stale(live_project, engine, agent_image):
    live_project.up(spec(agents={"coder": custom(agent_image), "reviewer": custom(agent_image)}))
    live_project.spawn("coder")
    live_project.spawn("reviewer")
    live_project.write(spec(agents={"reviewer": custom(agent_image)}))
    listing = rows(live_project)
    assert listing["coder-1"]["STALE"] == "stale" and listing["reviewer-1"]["STALE"] == ""
    refused = live_project.run("up", timeout=300)
    assert refused.returncode == 1 and "coder-1" in refused.stderr and "reviewer-1" not in refused.stderr
    assert live_project.run("prune", "--stale", "--yes").returncode == 0
    live_project.up()
    refused = live_project.run("spawn", "coder")
    assert refused.returncode != 0 and "coder" in refused.stderr, "the template is gone"
    assert rows(live_project)["reviewer-1"]["STALE"] == ""


def test_changing_another_template_does_not_make_an_instance_stale(live_project, engine, agent_image):
    live_project.up(spec(agents={"coder": custom(agent_image), "reviewer": custom(agent_image)}))
    live_project.spawn("coder")
    live_project.write(spec(agents={"coder": custom(agent_image), "reviewer": custom(agent_image, env={"R": "2"})}))
    assert rows(live_project)["coder-1"]["STALE"] == ""
    live_project.up()
    assert rows(live_project)["coder-1"]["STALE"] == ""


def test_a_changed_prompt_makes_an_instance_stale(live_project, engine, agent_image):
    prompt = live_project.root / "prompt.md"
    prompt.write_text("be brief\n")
    live_project.up(spec(agents={"coder": custom(agent_image, prompt="./prompt.md")}))
    live_project.spawn("coder")
    prompt.write_text("be verbose\n")
    assert "stale: coder-1" in live_project.run("up", "--dry-run").stdout


def test_a_changed_egress_profile_makes_its_instances_stale(live_project, engine, agent_image):
    live_project.up(spec(egress={"default": {"allow": ["example.com"]}}, agents={"coder": custom(agent_image)}))
    live_project.spawn("coder")
    live_project.write(spec(egress={"default": {"allow": ["example.org"]}}, agents={"coder": custom(agent_image)}))
    assert "stale: coder-1" in live_project.run("up", "--dry-run").stdout


def test_a_changed_workspace_definition_makes_the_instances_that_list_it_stale(live_project, engine, agent_image):
    live_project.up(spec(workspaces={"scratch": {}}, agents={"coder": custom(agent_image, workspaces=["scratch"]), "reviewer": custom(agent_image)}))
    live_project.spawn("coder")
    live_project.spawn("reviewer")
    live_project.write(spec(workspaces={"scratch": {}, "docs": {}}, agents={
        "coder": custom(agent_image, workspaces=["scratch", "docs"]), "reviewer": custom(agent_image)}))
    listing = rows(live_project)
    assert listing["coder-1"]["STALE"] == "stale" and listing["reviewer-1"]["STALE"] == ""


def test_down_clears_stale_instances_and_up_starts_afresh(live_project, engine, agent_image):
    make_stale(live_project, agent_image)
    assert live_project.run("down").returncode == 0
    live_project.up()
    assert engine.instances(live_project.name) == []
    assert live_project.spawn("coder") == "coder-1"


# --- the life cycle commands keep an instance's identity ---------------------------------------------------------------


def test_stop_and_start_keep_the_same_container_and_its_registrations(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder")
    before = engine.instance(live_project.name, "coder-1").raw["Id"]
    assert live_project.run("stop", "coder-1").returncode == 0
    assert live_project.run("start", "coder-1", timeout=300).returncode == 0
    assert engine.instance(live_project.name, "coder-1").raw["Id"] == before
    assert live_project.run("send", "coder-1", "still registered").returncode == 0


def test_an_instance_is_not_recreated_by_up_even_when_stopped(live_project, engine, agent_image):
    started(live_project, agent_image)
    live_project.spawn("coder")
    before = engine.instance(live_project.name, "coder-1").raw["Id"]
    assert live_project.run("stop", "coder-1").returncode == 0
    live_project.up()
    after = engine.instance(live_project.name, "coder-1")
    assert after.raw["Id"] == before and after.raw["State"]["Running"] is False
