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


def up(project, document, **env):
    project.write(document)
    result = project.run("up", env=env, timeout=300)
    assert result.returncode == 0, result.stderr
    return result


def container(engine, project, service):
    found = [r for r in engine.containers(project.name) if r.labels.get(f"{LABEL_PREFIX}service") == service]
    assert len(found) == 1, f"expected one {service} container, found {len(found)}"
    return found[0]


def curl(engine, project, agent_name, *args, timeout=25):
    """curl inside an agent; returns the result (curl exits non-zero when the proxy refuses)."""
    name = container(engine, project, agent_name).name
    return engine.exec(name, "curl", "-sS", "-m", str(timeout), *args)


def status(engine, project, host, agent_name="coder", *extra):
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


def test_a_project_without_agents_has_no_proxy(live_project, engine):
    up(live_project, spec())
    assert not [r for r in engine.containers(live_project.name) if r.labels.get(f"{LABEL_PREFIX}kind") == "proxy"]


def test_agents_are_pointed_at_the_proxy_and_trust_the_project_ca(live_project, engine, agent_image):
    up(live_project, with_allow(coder=custom(agent_image)))
    env = dict(item.split("=", 1) for item in container(engine, live_project, "coder").raw["Config"]["Env"])
    assert env["HTTPS_PROXY"].startswith("http://coder:") and env["HTTPS_PROXY"].endswith("@proxy:3128")
    assert env["SSL_CERT_FILE"] == "/etc/egzo/ca/ca-bundle.crt"
    assert env["NODE_EXTRA_CA_CERTS"] == "/etc/egzo/ca/ca.crt"
    assert "control" in env["NO_PROXY"]


def test_agents_see_the_ca_certificate_but_never_its_key(live_project, engine, agent_image):
    up(live_project, with_allow(coder=custom(agent_image)))
    name = container(engine, live_project, "coder").name
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
    assert status(engine, live_project, "example.com", "coder", "--noproxy", "*", "-m", "6").returncode != 0


def test_the_proxy_refuses_connections_without_credentials(live_project, engine, agent_image):
    up(live_project, with_allow("*", coder=custom(agent_image)))
    refused = curl(engine, live_project, "coder", "--proxy", "http://proxy:3128", "-o", "/dev/null", "https://example.com/")
    assert refused.returncode != 0
    assert "407" in refused.stderr


@needs_internet
def test_each_agent_gets_its_own_profile(live_project, engine, agent_image):
    document = spec(
        egress={"default": {}, "open": {"allow": ["example.com"]}},
        agents={"coder": custom(agent_image, egress="open"), "reviewer": custom(agent_image)},
    )
    up(live_project, document)
    assert status(engine, live_project, "example.com", "coder").stdout == "200"
    reviewer = status(engine, live_project, "example.com", "reviewer")
    assert reviewer.returncode != 0
    assert "403" in reviewer.stderr


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
    echoed = curl(engine, live_project, "coder", "https://httpbin.org/headers")
    assert echoed.returncode == 0, echoed.stderr
    assert json.loads(echoed.stdout)["headers"]["X-Egzo-Secret"] == SECRET

    agent_resource = container(engine, live_project, "coder")
    assert SECRET not in engine.exec(agent_resource.name, "env").stdout
    assert SECRET not in json.dumps(agent_resource.raw)


@needs_httpbin
def test_what_the_agent_sends_never_overrides_the_injected_credential(live_project, engine, agent_image):
    up(live_project, injecting(agent_image), DEPLOY_TOKEN=SECRET)
    echoed = curl(engine, live_project, "coder", "-H", "X-Egzo-Secret: forged", "https://httpbin.org/headers")
    assert json.loads(echoed.stdout)["headers"]["X-Egzo-Secret"] == SECRET


@needs_httpbin
def test_no_secret_appears_in_any_engine_resource_or_the_audit_log(live_project, engine, agent_image):
    up(live_project, injecting(agent_image), DEPLOY_TOKEN=SECRET)
    curl(engine, live_project, "coder", "https://httpbin.org/headers")
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
    assert {"agent": "coder", "host": "example.com", "action": "allow"}.items() <= next(e for e in events if e["host"] == "example.com").items()
    denied = next(e for e in events if e["host"] == "example.org")
    assert denied["action"] == "deny" and denied["agent"] == "coder"


def test_a_missing_secret_stops_up_before_anything_is_created(live_project, engine, agent_image):
    live_project.write(injecting(agent_image))
    result = live_project.run("up", env={"DEPLOY_TOKEN": ""})
    assert result.returncode != 0
    assert "main/DEPLOY_TOKEN" in result.stderr
    assert engine.resources(live_project.name) == []


@needs_httpbin
def test_rotating_a_secret_updates_the_proxy_without_recreating_anything(live_project, engine, agent_image):
    up(live_project, injecting(agent_image), DEPLOY_TOKEN="first-value-0123456789")
    before = {s: container(engine, live_project, s).raw["Id"] for s in ("control", "proxy", "coder")}

    up(live_project, injecting(agent_image), DEPLOY_TOKEN="second-value-0123456789")
    assert {s: container(engine, live_project, s).raw["Id"] for s in ("control", "proxy", "coder")} == before
    echoed = curl(engine, live_project, "coder", "https://httpbin.org/headers")
    assert json.loads(echoed.stdout)["headers"]["X-Egzo-Secret"] == "second-value-0123456789"


def test_a_second_up_with_agents_changes_nothing(live_project, engine, agent_image):
    up(live_project, with_allow(coder=custom(agent_image)))
    again = up(live_project, with_allow(coder=custom(agent_image)))
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
    for service in ("control", "coder"):
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
