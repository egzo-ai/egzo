# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""The egress proxy: the only way out for agents, with credentials injected so agents never hold them."""

import json
import socket
import urllib.request

import pytest

from conftest import LABEL_PREFIX
from support import agent, anthropic_profile, spec

SECRET = "sk-spec-injected-0123456789abcdef"


def custom(image, **fields):
    return agent(harness="custom", image=image, **fields)


def reup(project, document, **env):
    """`up` alone: infrastructure and policy, no instance."""
    project.write(document)
    result = project.run("up", env=env, timeout=300)
    assert result.returncode == 0, result.stderr
    return result


def up(project, document, **env):
    """`up`, then one instance of every template in the document (`coder-1`, `reviewer-1`, ...)."""
    result = reup(project, document, **env)
    for template in document.get("agents", {}):
        project.spawn(template)
    return result


def container(engine, project, name):
    """The container of an instance (`coder-1`) or of a sidecar (`control`, `proxy`)."""
    found = [
        r for r in engine.containers(project.name)
        if r.labels.get(f"{LABEL_PREFIX}instance") == name
        or (r.labels.get(f"{LABEL_PREFIX}kind") != "agent" and r.labels.get(f"{LABEL_PREFIX}service") == name)
    ]
    assert len(found) == 1, f"expected one {name} container, found {len(found)}"
    return found[0]


def curl(engine, project, agent_name, *args, timeout=25):
    """curl inside an agent; returns the result (curl exits non-zero when the proxy refuses)."""
    name = container(engine, project, agent_name).name
    return engine.exec(name, "curl", "-sS", "-m", str(timeout), *args)


def status(engine, project, host, agent_name="coder-1", *extra):
    return curl(engine, project, agent_name, "-o", "/dev/null", "-w", "%{http_code}", f"https://{host}/", *extra)


def reachable(host):
    try:
        socket.create_connection((host, 443), timeout=5).close()
        return True
    except OSError:
        return False


def require_reachable(host):
    """The specs that go through the proxy need the real internet: without it they fail, never skip."""
    if not reachable(host):
        pytest.fail(f"these specs need to reach {host}:443 from this host, and cannot", pytrace=False)


@pytest.fixture
def internet():
    require_reachable("example.com")


@pytest.fixture
def httpbin():
    require_reachable("httpbin.org")


needs_internet = pytest.mark.usefixtures("internet")


def with_allow(*hosts, **agents):
    return spec(egress={"default": {"allow": list(hosts)}}, agents=agents)


def test_agents_come_with_a_proxy_sidecar_and_an_egress_network(live_project, engine, agent_image):
    up(live_project, with_allow(coder=custom(agent_image)))
    proxy = container(engine, live_project, "proxy")
    assert proxy.labels[f"{LABEL_PREFIX}kind"] == "proxy"
    assert {r.name for r in engine.resources(live_project.name) if r.kind == "network"} >= {f"{live_project.name}_egress"}


def test_a_project_without_agent_templates_has_no_proxy(live_project, engine):
    up(live_project, spec())
    assert not [r for r in engine.containers(live_project.name) if r.labels.get(f"{LABEL_PREFIX}kind") == "proxy"]


def test_agents_are_pointed_at_the_proxy_and_trust_the_project_ca(live_project, engine, agent_image):
    up(live_project, with_allow(coder=custom(agent_image)))
    env = dict(item.split("=", 1) for item in container(engine, live_project, "coder-1").raw["Config"]["Env"])
    assert env["HTTPS_PROXY"].startswith("http://coder-1:") and env["HTTPS_PROXY"].endswith("@proxy:3128")
    assert env["SSL_CERT_FILE"] == "/etc/egzo/ca/ca-bundle.crt"
    assert env["NODE_EXTRA_CA_CERTS"] == "/etc/egzo/ca/ca.crt"
    assert "control" in env["NO_PROXY"]


def test_agents_see_the_ca_certificate_but_never_its_key(live_project, engine, agent_image):
    up(live_project, with_allow(coder=custom(agent_image)))
    name = container(engine, live_project, "coder-1").name
    listing = engine.exec(name, "ls", "/etc/egzo/ca").stdout.split()
    assert sorted(listing) == ["ca-bundle.crt", "ca.crt"]
    assert "PRIVATE KEY" not in engine.exec(name, "cat", "/etc/egzo/ca/ca.crt", "/etc/egzo/ca/ca-bundle.crt").stdout
    assert engine.exec(name, "touch", "/etc/egzo/ca/planted").returncode != 0


@needs_internet
def test_an_allowed_host_is_reachable_through_the_proxy(live_project, engine, agent_image):
    up(live_project, with_allow("example.com", coder=custom(agent_image)))
    assert status(engine, live_project, "example.com").stdout == "200"


@needs_internet
def test_a_host_outside_the_profile_is_refused(live_project, engine, agent_image):
    up(live_project, with_allow("example.com", coder=custom(agent_image)))
    refused = status(engine, live_project, "example.org")
    assert refused.returncode != 0
    assert "403" in refused.stderr


@needs_internet
def test_allow_star_grants_the_whole_internet_through_the_proxy(live_project, engine, agent_image):
    up(live_project, with_allow("*", coder=custom(agent_image)))
    assert status(engine, live_project, "example.org").stdout == "200"


@needs_internet
def test_an_agent_cannot_go_around_the_proxy(live_project, engine, agent_image):
    up(live_project, with_allow("*", coder=custom(agent_image)))
    assert status(engine, live_project, "example.com", "coder-1", "--noproxy", "*", "-m", "6").returncode != 0


def test_the_proxy_refuses_connections_without_credentials(live_project, engine, agent_image):
    up(live_project, with_allow("*", coder=custom(agent_image)))
    refused = curl(engine, live_project, "coder-1", "--proxy", "http://proxy:3128", "-o", "/dev/null", "https://example.com/")
    assert refused.returncode != 0
    assert "407" in refused.stderr


@needs_internet
def test_each_instance_gets_the_profile_of_its_template(live_project, engine, agent_image):
    document = spec(
        egress={"default": {}, "open": {"allow": ["example.com"]}},
        agents={"coder": custom(agent_image, egress="open"), "reviewer": custom(agent_image)},
    )
    reup(live_project, document)
    live_project.spawn("reviewer")  # the instances are spawned one after the other, long after the policy was loaded
    live_project.spawn("coder")
    assert status(engine, live_project, "example.com", "coder-1").stdout == "200"
    reviewer = status(engine, live_project, "example.com", "reviewer-1")
    assert reviewer.returncode != 0
    assert "403" in reviewer.stderr
    # the instance of the open profile is not the first one spawned: its name is bound to its own profile
    assert container(engine, live_project, "coder-1").labels[f"{LABEL_PREFIX}service"] == "coder"


def injecting(image):
    return spec(
        egress={
            "default": {
                "services": {
                    "echo": {"hosts": ["httpbin.org"], "inject": {"header": "X-Egzo-Secret"}, "secret": "main/DEPLOY_TOKEN"}
                }
            }
        },
        agents={"coder": custom(image)},
    )


needs_httpbin = pytest.mark.usefixtures("httpbin")


@needs_httpbin
def test_the_proxy_injects_the_credential_and_the_agent_never_sees_it(live_project, engine, agent_image):
    up(live_project, injecting(agent_image), DEPLOY_TOKEN=SECRET)
    echoed = curl(engine, live_project, "coder-1", "https://httpbin.org/headers")
    assert echoed.returncode == 0, echoed.stderr
    assert json.loads(echoed.stdout)["headers"]["X-Egzo-Secret"] == SECRET

    agent_resource = container(engine, live_project, "coder-1")
    assert SECRET not in engine.exec(agent_resource.name, "env").stdout
    assert SECRET not in json.dumps(agent_resource.raw)


@needs_httpbin
def test_what_the_agent_sends_never_overrides_the_injected_credential(live_project, engine, agent_image):
    up(live_project, injecting(agent_image), DEPLOY_TOKEN=SECRET)
    echoed = curl(engine, live_project, "coder-1", "-H", "X-Egzo-Secret: forged", "https://httpbin.org/headers")
    assert json.loads(echoed.stdout)["headers"]["X-Egzo-Secret"] == SECRET


@needs_httpbin
def test_no_secret_appears_in_any_engine_resource_or_the_audit_log(live_project, engine, agent_image):
    up(live_project, injecting(agent_image), DEPLOY_TOKEN=SECRET)
    curl(engine, live_project, "coder-1", "https://httpbin.org/headers")
    for resource in engine.resources(live_project.name):
        assert SECRET not in json.dumps(resource.raw), f"{resource.kind} {resource.name}"
    proxy_logs = engine.run("logs", container(engine, live_project, "proxy").name)
    assert SECRET not in proxy_logs.stdout + proxy_logs.stderr


@needs_internet
def test_the_audit_log_records_allowed_and_denied_connections(live_project, engine, agent_image):
    up(live_project, with_allow("example.com", coder=custom(agent_image)))
    status(engine, live_project, "example.com")
    status(engine, live_project, "example.org")
    logs = engine.run("logs", container(engine, live_project, "proxy").name)
    events = [json.loads(line) for line in (logs.stdout + logs.stderr).splitlines() if line.startswith("{")]
    assert {"agent": "coder-1", "host": "example.com", "action": "allow"}.items() <= next(e for e in events if e["host"] == "example.com").items()
    denied = next(e for e in events if e["host"] == "example.org")
    assert denied["action"] == "deny" and denied["agent"] == "coder-1"


def test_a_missing_secret_stops_up_before_anything_is_created(live_project, engine, agent_image):
    live_project.write(injecting(agent_image))
    result = live_project.run("up", env={"DEPLOY_TOKEN": ""})
    assert result.returncode != 0
    assert "main/DEPLOY_TOKEN" in result.stderr
    assert engine.resources(live_project.name) == []


@needs_httpbin
def test_rotating_a_secret_updates_the_proxy_without_recreating_anything(live_project, engine, agent_image):
    up(live_project, injecting(agent_image), DEPLOY_TOKEN="first-value-0123456789")
    before = {s: container(engine, live_project, s).raw["Id"] for s in ("control", "proxy", "coder-1")}

    reup(live_project, injecting(agent_image), DEPLOY_TOKEN="second-value-0123456789")
    assert {s: container(engine, live_project, s).raw["Id"] for s in ("control", "proxy", "coder-1")} == before
    echoed = curl(engine, live_project, "coder-1", "https://httpbin.org/headers")
    assert json.loads(echoed.stdout)["headers"]["X-Egzo-Secret"] == "second-value-0123456789"


def test_a_second_up_with_a_running_instance_changes_nothing(live_project, engine, agent_image):
    up(live_project, with_allow(coder=custom(agent_image)))
    again = reup(live_project, with_allow(coder=custom(agent_image)))
    assert "nothing to do" in again.stdout


@needs_internet
def test_the_ca_survives_recreating_the_proxy_and_the_policy_comes_back(live_project, engine, agent_image):
    up(live_project, with_allow("example.com", coder=custom(agent_image)))
    name = container(engine, live_project, "proxy").name

    def ca_fingerprint():
        reply = engine.exec(container(engine, live_project, "proxy").name, "/egzo", "proxy", "request", "GET", "/policy")
        return json.loads(reply.stdout)["ca"]

    before = ca_fingerprint()
    live_project.run("up", "--recreate")
    assert container(engine, live_project, "proxy").name == name
    assert ca_fingerprint() == before
    assert status(engine, live_project, "example.com").stdout == "200"


def test_the_ca_private_key_is_only_in_the_proxy_volume(live_project, engine, agent_image):
    up(live_project, with_allow(coder=custom(agent_image)))
    proxy = container(engine, live_project, "proxy")
    mounted = {m["Destination"]: m["Name"] for m in proxy.raw["Mounts"]}
    private = mounted["/ca-private"]
    for service in ("control", "coder-1"):
        others = {m.get("Name") for m in container(engine, live_project, service).raw["Mounts"]}
        assert private not in others, service


def test_up_refuses_an_oauth_token_bound_to_the_api_key_service(live_project, engine, agent_image):
    """api.anthropic.com answers 401 to a subscription token sent as x-api-key: say so before starting anything."""
    live_project.env["ANTHROPIC_API_KEY"] = "sk-ant-oat01-" + "x" * 60
    live_project.write(spec(egress={"default": anthropic_profile()}, agents={"coder": custom(agent_image)}))
    result = live_project.run("up", timeout=300)
    assert result.returncode != 0
    assert "OAuth" in result.stderr and "anthropic-oauth" in result.stderr
    assert "oat01" not in result.stderr and not engine.containers(live_project.name)


def test_up_refuses_an_api_key_bound_to_the_oauth_service(live_project, engine, agent_image):
    live_project.env["ANTHROPIC_API_KEY"] = "sk-ant-api03-" + "x" * 60
    profile_ = {"allow": ["platform.claude.com"], "services": {"anthropic-oauth": "main/ANTHROPIC_API_KEY"}}
    live_project.write(spec(egress={"default": profile_}, agents={"coder": custom(agent_image)}))
    result = live_project.run("up", timeout=300)
    assert result.returncode != 0
    assert "API key" in result.stderr and "anthropic" in result.stderr
    assert "api03" not in result.stderr and not engine.containers(live_project.name)


# --- the proxy reaches the public internet only ----------------------------------------------------------


@pytest.mark.parametrize("target", ["10.0.0.1", "169.254.169.254", "192.168.1.1", "172.17.0.1"])
def test_the_proxy_never_connects_to_a_private_address_even_when_everything_is_allowed(live_project, engine, agent_image, target):
    up(live_project, with_allow("*", coder=custom(agent_image)))
    result = status(engine, live_project, target)
    assert result.returncode != 0
    assert "403" in result.stderr
    logs = engine.run("logs", container(engine, live_project, "proxy").name)
    assert "not a public address" in logs.stdout + logs.stderr


def test_an_agent_cannot_reach_another_agent_through_the_proxy(live_project, engine, agent_image):
    up(live_project, with_allow("*", coder=custom(agent_image), reviewer=custom(agent_image)))
    reviewer = container(engine, live_project, "reviewer-1")
    networks = reviewer.raw["NetworkSettings"]["Networks"]
    address = networks[f"{live_project.name}_reviewer-1"]["IPAddress"]
    result = status(engine, live_project, address, "coder-1")
    assert result.returncode != 0
    assert "403" in result.stderr


@needs_internet
def test_a_restarted_proxy_gets_its_egress_policy_back(live_project, engine, agent_image):
    """The policy lives in the proxy's memory: restarting it must not cut every agent off until the next `up`."""
    up(live_project, with_allow("example.com", coder=custom(agent_image)))
    assert status(engine, live_project, "example.com").stdout == "200"
    restarted = live_project.run("restart", "proxy")
    assert restarted.returncode == 0, restarted.stderr
    assert status(engine, live_project, "example.com").stdout == "200"


@needs_internet
def test_a_proxy_that_was_stopped_and_started_again_gets_its_bindings_back_from_up(live_project, engine, agent_image):
    """The bindings live in the proxy's memory: `docker stop` and `start` lose them, and `up` must give them back."""
    up(live_project, with_allow("example.com", coder=custom(agent_image)))
    live_project.spawn("coder")
    assert status(engine, live_project, "example.com").stdout == "200"
    proxy = container(engine, live_project, "proxy").name
    engine.run("stop", proxy)
    engine.run("start", proxy)
    again = live_project.run("up")
    assert again.returncode == 0, again.stderr
    assert status(engine, live_project, "example.com").stdout == "200"


@needs_internet
def test_restarting_the_proxy_uses_the_published_profiles_not_a_file_edited_since(live_project, engine, agent_image):
    up(live_project, with_allow("example.com", coder=custom(agent_image)))
    live_project.spawn("coder")
    live_project.write(with_allow("example.org", coder=custom(agent_image)))  # edited, not applied by `up`
    assert live_project.run("restart", "proxy").returncode == 0
    assert status(engine, live_project, "example.com").stdout == "200"  # the instance still has what it was spawned with


def test_a_proxy_that_has_no_policy_tells_the_agent_what_to_do(live_project, engine, agent_image):
    up(live_project, with_allow("example.com", coder=custom(agent_image)))
    proxy = container(engine, live_project, "proxy").name
    engine.run("restart", proxy)  # not through egzo: nobody reloads the policy
    import time

    time.sleep(3)
    refused = curl(engine, live_project, "coder-1", "-o", "/dev/null", "https://example.com/")
    assert refused.returncode != 0
    assert "503" in refused.stderr or "egzo up" in refused.stderr
    assert "407" not in refused.stderr, "a missing policy is not an authentication problem"


@needs_internet
def test_the_audit_log_records_how_much_went_through_a_tunnel(live_project, engine, agent_image):
    up(live_project, with_allow("example.com", coder=custom(agent_image)))
    status(engine, live_project, "example.com")
    logs = engine.run("logs", container(engine, live_project, "proxy").name)
    events = [json.loads(line) for line in (logs.stdout + logs.stderr).splitlines() if line.startswith("{")]
    closed = [e for e in events if e["action"] == "close" and e["host"] == "example.com"]
    assert closed, events
    assert closed[0]["bytes_up"] > 0 and closed[0]["bytes_down"] > 0 and "duration_ms" in closed[0]


def test_the_audit_log_cannot_be_filled_by_one_long_value(live_project, engine, agent_image):
    up(live_project, with_allow("*", coder=custom(agent_image)))
    huge = "h" * 4000
    curl(engine, live_project, "coder-1", "-o", "/dev/null", "--proxy", f"http://{huge}:x@proxy:3128", "https://example.com/")
    logs = engine.run("logs", container(engine, live_project, "proxy").name)
    for line in (logs.stdout + logs.stderr).splitlines():
        assert len(line) < 2000, line[:200]


# --- the proxy knows profiles from `up` and instances from spawn ---------------------------------------------------


def credentials_of(engine, project, instance):
    env = dict(item.split("=", 1) for item in container(engine, project, instance).raw["Config"]["Env"])
    return env["HTTPS_PROXY"]


@needs_internet
def test_removing_an_instance_makes_its_proxy_credentials_stop_working(live_project, engine, agent_image):
    reup(live_project, with_allow("example.com", coder=custom(agent_image)))
    live_project.spawn("coder")
    live_project.spawn("coder")
    borrowed = credentials_of(engine, live_project, "coder-1")
    through = ["--proxy", borrowed, "-o", "/dev/null", "-w", "%{http_code}", "https://example.com/"]
    assert curl(engine, live_project, "coder-2", *through).stdout == "200"  # the proxy authenticates the credential
    assert live_project.run("rm", "--force", "coder-1").returncode == 0
    refused = curl(engine, live_project, "coder-2", *through)
    assert refused.returncode != 0
    assert "407" in refused.stderr


@needs_internet
def test_an_instance_spawned_after_a_restart_of_the_proxy_gets_its_template_profile(live_project, engine, agent_image):
    reup(live_project, with_allow("example.com", coder=custom(agent_image)))
    live_project.spawn("coder")
    assert live_project.run("restart", "proxy").returncode == 0
    assert live_project.spawn("coder") == "coder-2"
    assert status(engine, live_project, "example.com", "coder-1").stdout == "200"  # re-bound by the restart
    assert status(engine, live_project, "example.com", "coder-2").stdout == "200"


@needs_httpbin
def test_spawn_needs_no_secret_because_the_profiles_were_loaded_by_up(live_project, engine, agent_image):
    """Spawn only binds the instance's token to its profile's name: the hub, which never reads secrets, can do it."""
    reup(live_project, injecting(agent_image), DEPLOY_TOKEN=SECRET)
    spawned = live_project.run("spawn", "coder", env={"DEPLOY_TOKEN": ""}, timeout=300)  # the secret is unreadable now
    assert spawned.returncode == 0, spawned.stderr
    echoed = curl(engine, live_project, "coder-1", "https://httpbin.org/headers")
    assert json.loads(echoed.stdout)["headers"]["X-Egzo-Secret"] == SECRET
    assert SECRET not in spawned.stdout + spawned.stderr
