# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""What a hub reads of a project's files through the link (docs/hub.md, "Files"): the `workspaces` verb lists the workspace volumes, and
`files` is a stream to a file server that shows one of them, read only, from a helper container the link makes. The server speaks HTTP, GET
only: a path that ends with a slash lists a directory as JSON, any other path sends a regular file's bytes.

The hub is fixtures/fakehub, core's real hub side of the protocol. The worktree of a removed agent is a volume made by hand with the labels
egzo gives one, so no repository has to be cloned: the git workspace's remote is on a reserved domain the project may not reach, so `up`
fails to clone its base, says so and goes on, and the remote is still the one the project names. No spec here reaches the internet."""

import json

import pytest

from conftest import LABEL_PREFIX
from hubkit import eventually
from support import custom, spec

TIMEOUT = 30
REPO = "https://code.test/team/repo.git"  # .test is reserved: it names nothing, and egress does not allow it anyway


@pytest.fixture(scope="module")
def linked(shared_project, engine, fake_hub):
    p = shared_project
    p.up(spec(hub={"url": fake_hub.url, "ca": str(fake_hub.ca)}, egress={"default": {"allow": ["example.com"]}},
              workspaces={"docs": {}, "repo": {"git": {"url": REPO, "branch": "master"}}},
              agents={"writer": custom(engine.agent_image(), workspaces=["docs"]), "coder": custom(engine.agent_image(), workspaces=["repo"])}))
    p.ok("project register", input=fake_hub.code("alice") + "\n")
    assert eventually(lambda: fake_hub.verb(p.name, "workspaces").get("result") is not None, seconds=TIMEOUT), "the link never came"
    made = engine.in_volumes({p.volume("docs"): "/w"}, (
        "mkdir -p /w/notes/deep && printf 'alpha\\n' > /w/notes/a.txt && printf '<script>alert(1)</script>' > /w/page.html"
        " && ln -s /etc/passwd /w/out && ln -s .. /w/notes/up && mkfifo /w/pipe && chmod -R a+rX /w"))
    assert made.returncode == 0, made.stderr
    labels = [f"{LABEL_PREFIX}project={p.name}", f"{LABEL_PREFIX}kind=workspace", f"{LABEL_PREFIX}service=coder",
              f"{LABEL_PREFIX}workspace=repo", f"{LABEL_PREFIX}instance=gone", f"{LABEL_PREFIX}actor=user:7"]
    worktree = p.worktree_volume("repo", "gone")  # what `agent rm` leaves of an instance's worktree
    assert engine.run("volume", "create", *[a for label in labels for a in ("--label", label)], worktree).returncode == 0
    assert engine.in_volumes({worktree: "/w"}, "printf 'not committed\\n' > /w/wip.txt && chmod a+r /w/wip.txt").returncode == 0
    return p


def files(fake_hub, project, path, workspace="docs", instance="", method="GET", headers=""):
    """One request to the file server of a workspace; returns (status, headers, body), or the link's error as a string."""
    request = f"{method} {path} HTTP/1.1\r\nHost: files\r\nConnection: close\r\n{headers}\r\n"
    answer = fake_hub.verb(project.name, "files", Workspace=workspace, Instance=instance, Input=request, Seconds=TIMEOUT)
    if answer.get("error"):
        return answer["error"]
    head, _, body = answer["result"].partition("\r\n\r\n")
    status_line, *lines = head.split("\r\n")
    fields = {k.strip().lower(): v.strip() for k, v in (line.split(":", 1) for line in lines)}
    return int(status_line.split()[1]), fields, body


def helpers(engine, project):
    return [c for c in engine.containers(project.name) if c.labels.get(f"{LABEL_PREFIX}kind") == "files"]


def test_workspaces_lists_directories_and_git_workspaces_with_the_worktrees_their_agents_left(linked, fake_hub):
    listed = {w["Name"]: w for w in fake_hub.must(linked.name, "workspaces")}
    assert listed["docs"]["Kind"] == "directory" and listed["docs"]["Worktrees"] == []
    assert listed["repo"]["Kind"] == "git"
    assert listed["repo"]["Worktrees"] == [{"Instance": "gone", "State": "removed", "Actor": "user:7"}]
    assert (listed["repo"].get("Remote"), listed["repo"].get("Branch")) == (REPO, "master"), "where a git workspace clones from"
    assert "Remote" not in listed["docs"] and "Branch" not in listed["docs"]


def test_a_directory_lists_as_json_and_a_file_comes_as_its_bytes(linked, fake_hub):
    status, fields, body = files(fake_hub, linked, "/")
    assert status == 200 and fields["content-type"] == "application/json"
    listing = json.loads(body)
    assert [(e["name"], e["type"]) for e in listing["entries"]] == [("notes", "dir"), ("out", "symlink"), ("page.html", "file")], "a pipe is not listed"
    assert listing["truncated"] is False

    status, fields, body = files(fake_hub, linked, "/page.html")
    assert (status, body) == (200, "<script>alert(1)</script>") and fields["content-type"] == "application/octet-stream", "never typed by its content"
    status, _, body = files(fake_hub, linked, "/notes/a.txt", headers="Range: bytes=1-3\r\n")
    assert (status, body) == (206, "lph")

    status, fields, _ = files(fake_hub, linked, "/notes")
    assert status == 301 and fields["location"] == "notes/", "a directory is asked for with its slash"
    assert files(fake_hub, linked, "/notes/a.txt/")[0] == 404
    assert files(fake_hub, linked, "/nothing")[0] == 404


def test_links_pipes_and_paths_out_of_the_workspace_are_refused_and_block_nothing(linked, fake_hub):
    for path in ("/out", "/notes/up/", "/notes/up/page.html", "/pipe", "/../etc/passwd", "/notes/../../etc/passwd"):
        answer = files(fake_hub, linked, path)
        assert not isinstance(answer, str) and answer[0] in (403, 404) and "root:" not in answer[2], path


def test_nothing_is_written(linked, fake_hub, engine):
    for method in ("PUT", "DELETE", "POST", "PROPFIND", "MKCOL"):
        status, fields, _ = files(fake_hub, linked, "/notes/a.txt", method=method, headers="Content-Length: 0\r\n")
        assert status == 405 and fields["allow"] == "GET, HEAD", method
    assert engine.in_volumes({linked.volume("docs"): "/w"}, "cat /w/notes/a.txt").stdout == "alpha\n"


def test_the_helper_mounts_the_volume_read_only_with_no_network_no_capability_and_bounds(linked, fake_hub, engine):
    assert files(fake_hub, linked, "/")[0] == 200
    [helper] = [h for h in helpers(engine, linked) if h.labels.get(f"{LABEL_PREFIX}workspace") == "docs"]
    host = helper.raw["HostConfig"]
    assert host["NetworkMode"] == "none" and host["ReadonlyRootfs"] is True and host["CapDrop"] and not host.get("CapAdd")
    assert "no-new-privileges" in str(host["SecurityOpt"]) and host["Memory"] > 0 and host["PidsLimit"] > 0
    [mount] = [m for m in helper.raw["Mounts"] if m.get("Name") == linked.volume("docs")]
    assert mount["Destination"] == "/w" and mount["RW"] is False
    writer = engine.instance(linked.name, linked.spawn("writer"))
    assert helper.raw["Config"]["User"] == writer.raw["Config"]["User"], "the agents' user, never root"


def test_a_base_clone_does_not_open_and_a_removed_agents_worktree_does(linked, fake_hub):
    refused = files(fake_hub, linked, "/", workspace="repo")
    assert isinstance(refused, str) and "no volume" in refused
    status, _, body = files(fake_hub, linked, "/wip.txt", workspace="repo", instance="gone")
    assert (status, body) == (200, "not committed\n")
    assert isinstance(files(fake_hub, linked, "/", workspace="../x"), str)


def test_deleting_a_worktree_removes_its_helper_first(linked, fake_hub, engine):
    assert files(fake_hub, linked, "/", workspace="repo", instance="gone")[0] == 200
    assert [h for h in helpers(engine, linked) if h.labels.get(f"{LABEL_PREFIX}instance") == "gone"], "a helper serves it"
    linked.ok("workspace rm repo --worktree gone")
    assert not engine.volume_exists(linked.worktree_volume("repo", "gone"))
    assert not [h for h in helpers(engine, linked) if h.labels.get(f"{LABEL_PREFIX}instance") == "gone"]
