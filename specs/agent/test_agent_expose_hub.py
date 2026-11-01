# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""`expose` in a project with a hub: the answer is the preview's address, made from the template the hub gives the project's link.

The hub here is fixtures/fakehub, which only hands out the template: what opens an address is the hub's own business, tested in hub/."""

import re

import pytest

from agentapi import Agent
from hubkit import eventually
from support import agent, spec

TEMPLATE = "https://%s-p1a2b3c4d5e6.egzo-preview.example.net/"
ADDRESS = re.compile(r"^https://([a-z2-7]{16})-p1a2b3c4d5e6\.egzo-preview\.example\.net/$")


def expose(worker, port, **arguments):
    result = worker.tool("expose", port=port, **arguments)
    assert "structuredContent" in result, result
    return result["structuredContent"]


@pytest.fixture(scope="module")
def linked(shared_project, engine, egzo, fake_hub):
    fake_hub.preview(TEMPLATE)
    image = engine.session_image(egzo.binary)
    shared_project.up(spec(hub={"url": fake_hub.url, "ca": str(fake_hub.ca)}, agents={"web": agent(harness="custom", image=image, env={"FAKE_TUI": "cat"})}))
    shared_project.ok("project register", input=fake_hub.code("alice") + "\n")
    name = shared_project.spawn("web", "host")
    worker = Agent(engine, shared_project, name)
    assert eventually(lambda: expose(worker, 3100).get("address"), seconds=30), "the template never reached the control sidecar"
    return worker

def test_the_answer_is_the_address(linked):
    got = expose(linked, 3101)
    assert ADDRESS.fullmatch(got["address"]).group(1) == got["id"]

def test_the_same_address_again(linked):
    assert expose(linked, 3102)["address"] == expose(linked, 3102)["address"]

def test_tcp_is_a_command_even_with_a_hub(linked):
    got = expose(linked, 3103, protocol="tcp")
    assert not got.get("address")
    assert got["command"] == f"egzo agent port-forward {linked.name} 3103"

def test_the_status_line_carries_the_exposures_through_the_link(linked, fake_hub, shared_project):
    got = expose(linked, 3104, name="shop")

    def carried():
        lines = fake_hub.feed(shared_project.name, "status")
        agents = lines[-1]["Agents"] if lines else []
        exposures = next((a["Exposed"] for a in agents if a["Agent"] == linked.name), None) or []
        return [e for e in exposures if e["Port"] == 3104]

    assert eventually(carried, seconds=10)
    assert carried()[0]["ID"] == got["id"] and carried()[0]["Name"] == "shop" and carried()[0]["Protocol"] == "http"

def test_a_hub_that_changes_its_domain_moves_the_address(linked, fake_hub, shared_project):
    first = expose(linked, 3105)
    fake_hub.preview("https://%s-p1a2b3c4d5e6.other.example.net/")
    fake_hub.drop(shared_project.name)
    assert eventually(lambda: expose(linked, 3105).get("address") == f"https://{first['id']}-p1a2b3c4d5e6.other.example.net/", seconds=30)
    assert expose(linked, 3105)["id"] == first["id"]
    fake_hub.preview(TEMPLATE)
    fake_hub.drop(shared_project.name)
    assert eventually(lambda: expose(linked, 3105).get("address") == first["address"], seconds=30)

def test_a_hub_with_no_template_clears_it(linked, fake_hub, shared_project):
    fake_hub.preview("")
    fake_hub.drop(shared_project.name)
    assert eventually(lambda: "port-forward" in expose(linked, 3106).get("command", ""), seconds=30)
    fake_hub.preview(TEMPLATE)
    fake_hub.drop(shared_project.name)
    assert eventually(lambda: expose(linked, 3106).get("address"), seconds=30)

def test_a_project_that_leaves_its_hub_has_no_address(linked, fake_hub, shared_project, engine, egzo):
    image = engine.session_image(egzo.binary)
    shared_project.up(spec(agents={"web": agent(harness="custom", image=image, env={"FAKE_TUI": "cat"})}))
    assert eventually(lambda: not expose(linked, 3107).get("address"), seconds=30)
