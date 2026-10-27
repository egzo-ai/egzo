# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""What the proxy specs share: a project whose templates are named after the egress profile they use, and a probe that makes
requests from inside an instance and reads the proxy's audit trail.

A spec spawns the instance of the profile it is about and does not change the policy, so the project is up once for the whole module."""

import json

from conftest import LABEL_PREFIX
from support import custom, spec

SECRET = "sk-spec-injected-0123456789abcdef"
SITE_SECRET = 's3cr"t p&ss=w0rd+/%'  # characters that break a form or a JSON body unless encoded for the place they land in
ECH_HOST = "ech.egzo-spec.invalid"

PROFILES = {
    "allow": {"allow": ["example.com"]},
    "open": {"allow": ["*"]},
    "none": {},
    "echo": {"services": {"echo": {"hosts": ["httpbin.org"], "inject": {"header": "X-Egzo-Secret"}, "secret": "main/DEPLOY_TOKEN"}}},
    "paths": {"services": {"echo": {"hosts": ["httpbin.org"], "paths": ["/anything/api/"], "inject": {"header": "X-Egzo-Secret"}, "secret": "main/DEPLOY_TOKEN"}}},
    "basic": {"services": {"echo": {"hosts": ["httpbin.org"], "inject": {"header": "Authorization", "basic_user": "x-access-token"}, "secret": "main/DEPLOY_TOKEN"}}},
    "login": {"allow": ["httpbingo.org"], "services": {"login": {"hosts": ["httpbin.org"], "secret": "main/SITE_SECRET", "placeholder": "SITE_PASSWORD"}}},
    "plain": {"allow": ["httpbin.org"]},
    "inspect": {"services": {"docs": {"hosts": ["example.com"], "inspect": True}}},
    "wild": {"services": {"browse": {"hosts": ["*"], "inspect": True}, "echo": {"hosts": ["httpbin.org"], "inject": {"header": "X-Egzo-Secret"}, "secret": "main/DEPLOY_TOKEN"}}},
    "wildsub": {"services": {"docs": {"hosts": ["*.example.com"], "inspect": True}}},
    "ech": {"allow": [ECH_HOST]},
}


def document(image, *profiles):
    """A project with one template per named profile (`echo` uses the profile `echo`), and the two secrets from the environment."""
    chosen = profiles or tuple(PROFILES)
    return spec(
        vaults={"main": {"backend": "env", "secrets": ["DEPLOY_TOKEN", "SITE_SECRET"]}},
        egress={name: PROFILES[name] for name in chosen},
        agents={name: custom(image, egress=name) for name in chosen},
    )


class Probe:
    """The requests an instance makes, and what the proxy recorded about them."""

    def __init__(self, engine, project):
        self.engine = engine
        self.project = project

    def container(self, name):
        found = [
            r for r in self.engine.containers(self.project.name)
            if r.labels.get(f"{LABEL_PREFIX}instance") == name
            or (r.labels.get(f"{LABEL_PREFIX}kind") != "agent" and r.labels.get(f"{LABEL_PREFIX}service") == name)
        ]
        assert len(found) == 1, f"expected one {name} container, found {len(found)}"
        return found[0]

    def env(self, name):
        return dict(item.split("=", 1) for item in self.container(name).raw["Config"]["Env"])

    def curl(self, name, *args, timeout=25):
        """curl inside an instance; curl exits non-zero when the proxy refuses."""
        return self.engine.exec(self.container(name).name, "curl", "-sS", "-m", str(timeout), *args)

    def status(self, name, host, *extra):
        return self.curl(name, "-o", "/dev/null", "-w", "%{http_code}", f"https://{host}/", *extra)

    def shell(self, name, script):
        """A shell command inside an instance, so that $SITE_PASSWORD is expanded there, the way an agent would use it."""
        return self.engine.exec(self.container(name).name, "sh", "-c", f"curl -sS -m 25 {script}")

    def audit(self, *args, agent=None, host=None):
        """The events of the audit trail; with agent, those of the instance of that name that exists now (a name can come back,
        an instance's id does not)."""
        events = [json.loads(line) for line in self.project.ok("proxy log", *args).stdout.splitlines() if line.startswith("{")]
        instance = self.container(agent).labels[f"{LABEL_PREFIX}instance-id"] if agent is not None else None
        return [e for e in events if (agent is None or e.get("instance") == instance) and (host is None or e.get("host") == host)]

    def requests_to(self, host, agent):
        return [e for e in self.audit(agent=agent, host=host) if e["event"] == "request"]

    def proxy_log(self):
        logs = self.engine.run("logs", self.container("proxy").name)
        return logs.stdout + logs.stderr
