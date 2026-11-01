# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""`expose` in a project with a hub: `hub` answers the preview's address, made from the template the hub gives the project's link, and the
project's local ports still work beside it.

The hub here is fixtures/fakehub, which only hands out the template: what opens an address is the hub's own business, tested in hub/."""

import re

import pytest

from agentapi import Agent
from hubkit import eventually
from support import agent, spec

TEMPLATE = "https://%s-p1a2b3c4d5e6.egzo-preview.example.net/"
ADDRESS = re.compile(r"^https://([a-z2-7]{16})-p1a2b3c4d5e6\.egzo-preview\.example\.net/$")
PORTS = {"app": "3100/http", "shop": "3101/http", "dev": "3102/http", "db": "5432/tcp"}


def document(fake_hub, image):
    return spec(
        hub={"url": fake_hub.url, "ca": str(fake_hub.ca)},
        local_ports={"bind": "127.0.0.1", "max_exposed": 1},
        agents={"web": agent(harness="custom", image=image, env={"FAKE_TUI": "cat"}, ports=PORTS)},
    )


def expose(worker, name, where="hub"):
    result = worker.tool("expose", name=name, where=where)
    assert "structuredContent" in result, result
    return result["structuredContent"]


def refused(worker, name, where="hub"):
    result = worker.tool("expose", name=name, where=where)
    assert result.get("isError"), result
    return result["content"][0]["text"]


def unexpose(worker, *names):
    for name in names:
        worker.tool("unexpose", name=name)


@pytest.fixture(scope="module")
def linked(shared_project, engine, egzo, fake_hub):
    fake_hub.preview(TEMPLATE)
    shared_project.up(document(fake_hub, engine.session_image(egzo.binary)))
    shared_project.ok("project register", input=fake_hub.code("alice") + "\n")
    name = shared_project.spawn("web", "host")
    worker = Agent(engine, shared_project, name)
    assert eventually(lambda: expose(worker, "app").get("address"), seconds=30), "the template never reached the control sidecar"
    unexpose(worker, "app")
    return worker


def test_the_answer_is_the_address(linked):
    got = expose(linked, "shop")
    assert ADDRESS.fullmatch(got["address"]).group(1) == got["id"]
    assert (got["name"], got["port"], got["protocol"], got["where"]) == ("shop", 3101, "http", "hub")
    unexpose(linked, "shop")


def test_the_same_address_again_and_after_an_unexpose(linked):
    first = expose(linked, "shop")
    assert expose(linked, "shop") == first
    unexpose(linked, "shop")
    assert expose(linked, "shop") == first
    unexpose(linked, "shop")


def test_tcp_is_local_only_even_with_a_hub(linked):
    assert "only http" in refused(linked, "db", where="hub")


def test_a_port_is_open_for_one_place_at_a_time(linked):
    expose(linked, "shop", where="hub")
    assert "unexpose it first" in refused(linked, "shop", where="local")
    unexpose(linked, "shop")
    local = expose(linked, "shop", where="local")
    assert re.fullmatch(r"http://127\.0\.0\.1:\d+/", local["address"]) and "id" not in local
    assert "unexpose it first" in refused(linked, "shop", where="hub")
    unexpose(linked, "shop")


def test_the_status_line_carries_the_exposures_and_where_through_the_link(linked, fake_hub, shared_project):
    got = expose(linked, "shop")

    def carried():
        lines = fake_hub.feed(shared_project.name, "status")
        agents = lines[-1]["Agents"] if lines else []
        exposures = next((a["Exposed"] for a in agents if a["Agent"] == linked.name), None) or []
        return [e for e in exposures if e["Port"] == 3101]

    assert eventually(carried, seconds=10)
    line = carried()[0]
    assert (line["ID"], line["Name"], line["Protocol"], line["Where"]) == (got["id"], "shop", "http", "hub")
    unexpose(linked, "shop")


def test_hub_exposures_do_not_count_towards_max_exposed(linked):
    expose(linked, "app", where="hub")
    expose(linked, "shop", where="hub")
    expose(linked, "db", where="local")
    assert "unexpose one first" in refused(linked, "dev", where="local"), "max_exposed is 1, and one local port is open"
    unexpose(linked, "app", "shop", "db")


def test_a_local_exposure_is_not_served_as_a_preview(linked, fake_hub, shared_project):
    got = expose(linked, "dev", where="local")

    def carried():
        lines = fake_hub.feed(shared_project.name, "status")
        agents = lines[-1]["Agents"] if lines else []
        return [e for a in agents if a["Agent"] == linked.name for e in a.get("Exposed") or [] if e["Port"] == 3102]

    assert eventually(carried, seconds=10)
    assert carried()[0]["Where"] == "local" and not carried()[0].get("ID")
    answer = fake_hub.verb(shared_project.name, "forward", Agent=linked.name, Port=3102, Seconds=1)
    assert "does not expose port 3102" in answer["error"], "the link forwards a port open for the hub and no other"
    assert got["where"] == "local"
    unexpose(linked, "dev")


def address(worker, name="app"):
    """The address a preview has now, or "" when the project has none."""
    return worker.tool("expose", name=name, where="hub").get("structuredContent", {}).get("address", "")


def test_a_hub_that_changes_its_domain_moves_the_address(linked, fake_hub, shared_project):
    first = expose(linked, "app")
    fake_hub.preview("https://%s-p1a2b3c4d5e6.other.example.net/")
    fake_hub.drop(shared_project.name)
    assert eventually(lambda: address(linked) == f"https://{first['id']}-p1a2b3c4d5e6.other.example.net/", seconds=30)
    assert expose(linked, "app")["id"] == first["id"]
    fake_hub.preview(TEMPLATE)
    fake_hub.drop(shared_project.name)
    assert eventually(lambda: address(linked) == first["address"], seconds=30)
    unexpose(linked, "app")


def test_a_hub_with_no_template_clears_it(linked, fake_hub, shared_project):
    fake_hub.preview("")
    fake_hub.drop(shared_project.name)
    assert eventually(lambda: "no hub" in refused(linked, "app"), seconds=30)
    fake_hub.preview(TEMPLATE)
    fake_hub.drop(shared_project.name)
    assert eventually(lambda: address(linked), seconds=30)
    unexpose(linked, "app")


def test_a_hub_that_turns_the_project_away_clears_it(linked, fake_hub, shared_project):
    fake_hub.revoke(shared_project.name)
    assert eventually(lambda: "no hub" in refused(linked, "app"), seconds=30)
    fake_hub.forget(shared_project.name)
    shared_project.ok("project register", input=fake_hub.code("alice") + "\n")
    assert eventually(lambda: address(linked), seconds=30)
    unexpose(linked, "app")


def test_a_project_that_leaves_its_hub_has_no_address_and_its_local_ports_stay(linked, fake_hub, shared_project, engine, egzo):
    image = engine.session_image(egzo.binary)
    document_without_hub = document(fake_hub, image)
    del document_without_hub["hub"]
    shared_project.up(document_without_hub)
    assert eventually(lambda: "no hub" in refused(linked, "app"), seconds=30)
    assert expose(linked, "db", where="local")["address"].startswith("127.0.0.1:")
