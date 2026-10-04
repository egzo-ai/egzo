"""Git workspaces: the prep container clones into the host before the agent starts.

The specs clone a small public repository through the project's proxy. Nothing runs git on the host
and no credential is ever handed to the CLI or the agent.
"""

import os
import subprocess

import pytest

from conftest import LABEL_PREFIX
from support import agent, spec

REPO = "https://github.com/octocat/Hello-World.git"

pytestmark = pytest.mark.usefixtures("github")


def custom(image, **fields):
    return agent(harness="custom", image=image, **fields)


def project_spec(image, *, workspace=None, agents=None, allow=("github.com",)):
    workspaces = {"repo": {"git": {"url": REPO}, **(workspace or {})}}
    agents = agents or {"coder": custom(image, workspaces=["repo"])}
    return spec(egress={"default": {"allow": list(allow)}}, workspaces=workspaces, agents=agents)


def up(project, document, **kwargs):
    project.write(document)
    return project.run("up", timeout=400, **kwargs)


def up_ok(project, document):
    result = up(project, document)
    assert result.returncode == 0, result.stderr
    return result


def container(engine, project, service):
    return [r for r in engine.containers(project.name) if r.labels.get(f"{LABEL_PREFIX}service") == service][0]


def in_agent(engine, project, name, command):
    return engine.exec(container(engine, project, name).name, "sh", "-c", command)


def git(path, *args):
    return subprocess.run(["git", "-C", str(path), *args], capture_output=True, text=True, check=True).stdout.strip()


def test_up_clones_a_git_workspace_for_each_agent_before_it_starts(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    clone = live_project.root / ".egzo" / "workspaces" / "repo" / "coder"
    assert (clone / "README").exists()
    assert git(clone, "remote", "get-url", "origin") == REPO
    assert "README" in in_agent(engine, live_project, "coder", "ls /workspace/repo").stdout


def test_the_clone_belongs_to_the_invoking_user_and_the_agent_can_commit(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    clone = live_project.root / ".egzo" / "workspaces" / "repo" / "coder"
    assert (clone / "README").stat().st_uid == os.getuid()
    committed = in_agent(
        engine, live_project, "coder",
        "cd /workspace/repo && git config user.email a@b.c && git config user.name a && echo x > new && git add new && git commit -qm x && git log --oneline -1",
    )
    assert committed.returncode == 0, committed.stderr
    assert git(clone, "log", "--oneline", "-1").endswith(" x")


def test_the_branch_is_checked_out(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image, workspace={"git": {"url": REPO, "branch": "test"}}))
    assert in_agent(engine, live_project, "coder", "git -C /workspace/repo branch --show-current").stdout.strip() == "test"


def test_every_agent_gets_its_own_independent_clone(live_project, engine, agent_image):
    agents = {"coder": custom(agent_image, workspaces=["repo"]), "reviewer": custom(agent_image, workspaces=["repo"])}
    up_ok(live_project, project_spec(agent_image, agents=agents))
    assert in_agent(engine, live_project, "coder", "echo mine > /workspace/repo/private").returncode == 0
    assert in_agent(engine, live_project, "reviewer", "ls /workspace/repo").stdout.split().count("private") == 0
    assert in_agent(engine, live_project, "reviewer", "test -d /workspace/repo/.git").returncode == 0


def test_the_clone_location_follows_the_path_key(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image, workspace={"path": "./work/repo"}))
    assert (live_project.root / "work" / "repo" / "coder" / "README").exists()
    assert not (live_project.root / ".egzo" / "workspaces").exists()


def test_an_existing_clone_is_never_modified_and_a_second_up_changes_nothing(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    clone = live_project.root / ".egzo" / "workspaces" / "repo" / "coder"
    (clone / "wip.txt").write_text("unpushed work\n")
    second = up_ok(live_project, project_spec(agent_image))
    assert "nothing to do" in second.stdout
    assert (clone / "wip.txt").read_text() == "unpushed work\n"


def test_the_prep_container_is_removed_and_leaves_no_trace(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    names = {r.labels.get(f"{LABEL_PREFIX}service") for r in engine.containers(live_project.name)}
    assert names == {"control", "proxy", "coder"}


def test_the_clone_goes_through_the_proxy_with_the_agents_identity(live_project, engine, agent_image):
    import json

    up_ok(live_project, project_spec(agent_image))
    audit = [json.loads(line) for line in live_project.run("proxy", "log").stdout.splitlines() if line.startswith("{")]
    allowed = [e for e in audit if e["host"] == "github.com" and e["action"] == "allow"]
    assert allowed and {e["agent"] for e in allowed} == {"coder"}


def test_a_profile_that_cannot_reach_the_git_host_stops_up_with_a_clear_error(live_project, engine, agent_image):
    result = up(live_project, project_spec(agent_image, allow=("example.com",)))
    assert result.returncode != 0
    assert "github.com" in result.stderr and "repo" in result.stderr
    assert not [r for r in engine.containers(live_project.name) if r.labels[f"{LABEL_PREFIX}service"] == "coder"]


def test_shared_mode_gives_every_agent_the_same_checkout(live_project, engine, agent_image):
    agents = {"coder": custom(agent_image, workspaces=["repo"]), "reviewer": custom(agent_image, workspaces=["repo"])}
    up_ok(live_project, project_spec(agent_image, workspace={"mode": "shared"}, agents=agents))
    assert (live_project.root / ".egzo" / "workspaces" / "repo" / "shared" / "README").exists()
    assert in_agent(engine, live_project, "coder", "echo seen > /workspace/repo/note").returncode == 0
    assert in_agent(engine, live_project, "reviewer", "cat /workspace/repo/note").stdout.strip() == "seen"


def test_worktree_mode_gives_each_agent_a_worktree_of_one_base_clone(live_project, engine, agent_image):
    agents = {"coder": custom(agent_image, workspaces=["repo"]), "reviewer": custom(agent_image, workspaces=["repo"])}
    up_ok(live_project, project_spec(agent_image, workspace={"mode": "worktree"}, agents=agents))
    status = in_agent(engine, live_project, "coder", "cd /workspace/repo && git status --short --branch && git worktree list")
    assert status.returncode == 0, status.stderr
    assert in_agent(engine, live_project, "coder", "echo mine > /workspace/repo/private").returncode == 0
    assert in_agent(engine, live_project, "reviewer", "test -e /workspace/repo/private").returncode != 0


def test_worktree_mode_does_not_expose_other_agents_worktrees(live_project, engine, agent_image):
    agents = {"coder": custom(agent_image, workspaces=["repo"]), "reviewer": custom(agent_image, workspaces=["repo"])}
    up_ok(live_project, project_spec(agent_image, workspace={"mode": "worktree"}, agents=agents))
    mounts = {m["Destination"] for m in container(engine, live_project, "coder").raw["Mounts"]}
    assert not any("reviewer" in m for m in mounts)


def test_changing_the_mode_of_an_existing_clone_is_refused_not_converted(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    result = up(live_project, project_spec(agent_image, workspace={"mode": "shared"}))
    assert result.returncode != 0
    assert "mode" in result.stderr and str(live_project.root / ".egzo" / "workspaces" / "repo") in result.stderr


def test_a_reviewer_reads_the_coders_clone_through_a_read_only_reference(live_project, engine, agent_image):
    agents = {
        "coder": custom(agent_image, workspaces=["repo"]),
        "reviewer": custom(agent_image, workspaces=["coder/repo:ro"]),
    }
    up_ok(live_project, project_spec(agent_image, agents=agents))
    assert in_agent(engine, live_project, "coder", "echo draft > /workspace/repo/draft").returncode == 0
    assert in_agent(engine, live_project, "reviewer", "cat /workspace/coder/repo/draft").stdout.strip() == "draft"
    assert in_agent(engine, live_project, "reviewer", "echo x > /workspace/coder/repo/draft").returncode != 0


def test_no_git_credential_reaches_the_clone_or_the_agent(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    config = (live_project.root / ".egzo" / "workspaces" / "repo" / "coder" / ".git" / "config").read_text()
    assert "Authorization" not in config and "token" not in config.lower()
    env = container(engine, live_project, "coder").raw["Config"]["Env"]
    assert not [e for e in env if e.startswith(("GITHUB_TOKEN", "GIT_ASKPASS", "GH_TOKEN"))]


def test_down_never_deletes_workspace_directories(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    assert live_project.run("down", "--volumes").returncode == 0
    assert (live_project.root / ".egzo" / "workspaces" / "repo" / "coder" / "README").exists()


def test_down_workspaces_removes_clean_clones_after_confirmation(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    refused = live_project.run("down", "--workspaces", input="n\n")
    assert refused.returncode != 0
    assert (live_project.root / ".egzo" / "workspaces" / "repo" / "coder").exists()
    done = live_project.run("down", "--workspaces", input="y\n")
    assert done.returncode == 0, done.stderr
    assert not (live_project.root / ".egzo" / "workspaces" / "repo" / "coder").exists()


def test_down_workspaces_refuses_a_clone_with_uncommitted_work(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    clone = live_project.root / ".egzo" / "workspaces" / "repo" / "coder"
    (clone / "wip.txt").write_text("not committed\n")
    result = live_project.run("down", "--workspaces", "--yes")
    assert result.returncode != 0
    assert "uncommitted" in result.stderr and str(clone) in result.stderr
    assert (clone / "wip.txt").exists()


def test_down_workspaces_refuses_a_clone_with_unpushed_commits(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    clone = live_project.root / ".egzo" / "workspaces" / "repo" / "coder"
    git(clone, "-c", "user.email=a@b.c", "-c", "user.name=a", "commit", "--allow-empty", "-qm", "local only")
    result = live_project.run("down", "--workspaces", "--yes")
    assert result.returncode != 0
    assert "unpushed" in result.stderr
    assert clone.exists()


def test_down_workspaces_force_removes_even_unsaved_work(live_project, engine, agent_image):
    up_ok(live_project, project_spec(agent_image))
    clone = live_project.root / ".egzo" / "workspaces" / "repo" / "coder"
    (clone / "wip.txt").write_text("not committed\n")
    assert live_project.run("down", "--workspaces", "--yes", "--force").returncode == 0
    assert not clone.exists()
