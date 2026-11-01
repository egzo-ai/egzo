# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""`ports:` of an agent and `local_ports:` of the project: the ports an agent may serve are declared by name, and a file that declares one
that could never open is refused. Nothing here needs an engine."""

import pytest

from support import agent, spec

LOCAL = {"bind": "127.0.0.1"}
HUB = "https://hub.example.org"


def ports(**declared):
    return {"coder": agent(ports=declared)}


def refused(project, document, *words):
    result = project.config(document)
    assert result.returncode != 0, result.stdout
    for word in words:
        assert word in result.stderr, result.stderr
    return result


# --- a file that passes -----------------------------------------------------------------------------------------


def test_declared_ports_are_resolved_with_their_number_and_protocol(project):
    resolved = project.resolved(spec(local_ports=LOCAL, agents=ports(web="5173/http", db="5432/tcp", voice="5432/udp")))
    assert resolved["agents"]["coder"]["ports"] == {
        "web": {"port": 5173, "protocol": "http"},
        "db": {"port": 5432, "protocol": "tcp"},
        "voice": {"port": 5432, "protocol": "udp"},
    }


def test_local_ports_has_defaults(project):
    resolved = project.resolved(spec(local_ports=LOCAL, agents=ports(web="5173/http")))
    assert resolved["local_ports"] == {"bind": "127.0.0.1", "host": "127.0.0.1", "pool_low": 20000, "pool_high": 29999, "max_exposed": 10}


def test_local_ports_takes_every_setting(project):
    local = {"bind": "0.0.0.0", "host": "mybox.lan", "pool": "30000-30009", "max_exposed": 3}
    resolved = project.resolved(spec(local_ports=local))
    assert resolved["local_ports"] == {"bind": "0.0.0.0", "host": "mybox.lan", "pool_low": 30000, "pool_high": 30009, "max_exposed": 3}


def test_an_ipv6_address_can_be_the_bind(project):
    assert project.resolved(spec(local_ports={"bind": "::", "host": "mybox.lan"}))["local_ports"]["bind"] == "::"


def test_a_project_without_ports_has_none_resolved(project):
    resolved = project.resolved(spec(agents={"reviewer": agent()}))
    assert "ports" not in resolved["agents"]["reviewer"] and "local_ports" not in resolved


def test_an_http_port_needs_only_a_hub(project):
    assert project.resolved(spec(hub=HUB, agents=ports(web="5173/http")))["agents"]["coder"]["ports"]["web"]["port"] == 5173


def test_a_tcp_port_and_a_udp_port_may_share_a_number(project):
    project.resolved(spec(local_ports=LOCAL, agents=ports(db="5432/tcp", voice="5432/udp")))


def test_an_http_and_a_tcp_port_may_not_share_a_number(project):
    refused(project, spec(local_ports=LOCAL, agents=ports(web="8080/http", raw="8080/tcp")), "8080", "twice")


# --- the ports of a template ---------------------------------------------------------------------------------------


@pytest.mark.parametrize("wrong", ["5173", "http", "5173/", "/http", "5173/ftp", "5173/HTTP", "0/http", "65536/http", "-1/tcp", "5e3/tcp", "05173/http", "5173/http/x"])
def test_a_port_that_is_not_a_number_and_a_protocol_is_refused(project, wrong):
    refused(project, spec(local_ports=LOCAL, agents=ports(web=wrong)), "web", "PORT/PROTOCOL")


@pytest.mark.parametrize("name", ["Web", "1web", "web_app", "-web", "web.app", "a" * 33])
def test_a_port_name_outside_the_pattern_is_refused(project, name):
    refused(project, spec(local_ports=LOCAL, agents=ports(**{name: "5173/http"})), "port name")


def test_a_port_name_of_32_characters_is_accepted(project):
    project.resolved(spec(local_ports=LOCAL, agents=ports(**{"a" * 32: "5173/http"})))


def test_more_than_32_ports_in_a_template_are_refused(project):
    many = {f"p{i}": f"{3000 + i}/http" for i in range(33)}
    refused(project, spec(local_ports={**LOCAL, "pool": "20000-29999"}, agents=ports(**many)), "33 ports", "at most 32")


def test_32_ports_in_a_template_are_accepted(project):
    project.resolved(spec(local_ports=LOCAL, agents=ports(**{f"p{i}": f"{3000 + i}/http" for i in range(32)})))


def test_the_same_port_number_twice_is_refused(project):
    refused(project, spec(local_ports=LOCAL, agents=ports(a="3000/http", b="3000/http")), "3000", "twice")
    refused(project, spec(local_ports=LOCAL, agents=ports(a="3000/tcp", b="3000/tcp")), "3000", "twice")
    refused(project, spec(local_ports=LOCAL, agents=ports(a="3000/udp", b="3000/udp")), "3000", "twice")


# --- a port that could never open ----------------------------------------------------------------------------------


@pytest.mark.parametrize("protocol", ["tcp", "udp"])
def test_a_raw_port_without_local_ports_is_refused_even_with_a_hub(project, protocol):
    refused(project, spec(hub=HUB, agents=ports(db=f"5432/{protocol}")), "db", "local_ports")


def test_an_http_port_with_neither_a_hub_nor_local_ports_is_refused(project):
    refused(project, spec(agents=ports(web="5173/http")), "web", "hub", "local_ports")


def test_more_raw_ports_than_max_exposed_are_refused(project):
    document = spec(local_ports={**LOCAL, "max_exposed": 2}, agents=ports(a="1001/tcp", b="1002/tcp", c="1003/udp"))
    refused(project, document, "max_exposed", "3")


def test_raw_ports_up_to_max_exposed_are_accepted_and_http_ports_do_not_count(project):
    project.resolved(spec(local_ports={**LOCAL, "max_exposed": 2}, agents=ports(a="1001/tcp", b="1002/udp", c="1003/http", d="1004/http")))


def test_more_than_ten_raw_ports_are_refused_with_the_default_max_exposed(project):
    refused(project, spec(local_ports=LOCAL, agents=ports(**{f"p{i}": f"{4000 + i}/tcp" for i in range(11)})), "max_exposed", "10")


# --- local_ports ---------------------------------------------------------------------------------------------------


def test_local_ports_needs_a_bind(project):
    refused(project, spec(local_ports={"host": "mybox.lan"}), "local_ports", "bind")


@pytest.mark.parametrize("wrong", ["localhost", "mybox.lan", "300.1.1.1", "127.0.0.1:80"])
def test_a_bind_that_is_not_an_ip_is_refused(project, wrong):
    refused(project, spec(local_ports={"bind": wrong}), "bind", "IP")


@pytest.mark.parametrize("wildcard", ["0.0.0.0", "::"])
def test_a_wildcard_bind_needs_a_host(project, wildcard):
    refused(project, spec(local_ports={"bind": wildcard}), "host", wildcard)


@pytest.mark.parametrize("pool", ["20000", "a-b", "0-100", "100-65536", "300-200", "1-2-3", "-5"])
def test_a_pool_that_is_not_a_range_within_the_ports_is_refused(project, pool):
    refused(project, spec(local_ports={**LOCAL, "pool": pool}), "pool")


def test_a_pool_within_1_to_65535_is_accepted(project):
    assert project.resolved(spec(local_ports={**LOCAL, "pool": "1-65535"}))["local_ports"]["pool_high"] == 65535


@pytest.mark.parametrize("value", [0, -1])
def test_max_exposed_below_one_is_refused(project, value):
    refused(project, spec(local_ports={**LOCAL, "max_exposed": value}), "max_exposed")


def test_an_unknown_key_of_local_ports_is_refused_with_its_place(project):
    project.write("name: demo\nlocal_ports:\n  bind: 127.0.0.1\n  poool: 1-2\n")
    result = project.sh("project check --resolved")
    assert result.returncode != 0
    assert 'egzo.yaml:4:3: local_ports: unknown key "poool", did you mean "pool"?' in result.stderr


def test_every_problem_is_reported_not_only_the_first(project):
    refused(project, spec(local_ports={"bind": "nope"}, agents=ports(web="5173", Bad="1/http")), "bind", "web", "port name")
