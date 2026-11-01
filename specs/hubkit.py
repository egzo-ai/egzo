# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""A hub for the specs: fixtures/fakehub, core's real hub side of the link protocol. It runs on this machine and listens where
the engine's containers can reach it, and a spec plays the hub through its control API: making link codes, revoking, asking verbs."""

import json
import re
import subprocess
import time
import urllib.request
from pathlib import Path

ROOT = Path(__file__).parent.parent


def hub_host(engine):
    """The address the engine's containers reach this machine at: the default network's gateway, and for a rootless Podman the name it
    defines for the host, because there a container has the host's own address (see sshd.py)."""
    if engine.cli == "podman" and engine.rootless:
        return engine.run("run", "--rm", "docker.io/library/alpine:3", "sh", "-c", "getent hosts host.containers.internal").stdout.split()[0]
    network = "podman" if engine.cli == "podman" else "bridge"
    inspected = json.loads(engine.run("network", "inspect", network).stdout)[0]
    if "subnets" in inspected:  # Podman's own format: Docker's is IPAM.Config
        return inspected["subnets"][0]["gateway"]
    return inspected["IPAM"]["Config"][0]["Gateway"]


class FakeHub:
    def __init__(self, directory, gateway):
        directory.mkdir(parents=True, exist_ok=True)
        self.ca = directory / "hub-ca.pem"
        self.binary = directory / "fakehub"
        built = subprocess.run(["go", "build", "-o", str(self.binary), "./specs/fixtures/fakehub"], cwd=ROOT, capture_output=True, text=True)
        assert built.returncode == 0, f"could not build the fake hub:\n{built.stderr}"
        self.gateway = gateway
        self.process = None
        self.start()

    def start(self):
        self.process = subprocess.Popen(
            [str(self.binary), "-host", self.gateway, "-ca", str(self.ca)], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True
        )
        found = re.match(r"link=(\S+) control=(\S+)", self.process.stdout.readline())
        assert found, "the fake hub did not start"
        self.url, self.control = f"https://{found.group(1)}", f"http://{found.group(2)}"

    def stop(self):
        self.process.terminate()
        self.process.wait()

    def _call(self, method, path, body=None):
        data = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(self.control + path, data=data, method=method)
        with urllib.request.urlopen(request, timeout=150) as response:
            text = response.read()
        return json.loads(text) if text else None

    def registrations(self):
        return self._call("GET", "/registrations")["registrations"]

    def forgotten(self):
        return self._call("GET", "/registrations")["forgotten"] or []

    def registration(self, project):
        found = [r for r in self.registrations() if r["Project"] == project]
        return found[0] if found else None

    def dials(self):
        """How many links have proved a key here."""
        return self._call("GET", "/registrations")["dials"]

    def codes(self):
        """How many link codes are still good."""
        return self._call("GET", "/registrations")["codes"]

    def code(self, owner="alice"):
        """A link code, as an operator makes one in Settings, Links."""
        return self._call("POST", "/code", {"Owner": owner})

    def revoke(self, project, message=""):
        self._call("POST", "/revoke", {"Project": project, "Message": message})

    def forget(self, project):
        self._call("POST", f"/forget?project={project}")

    def verb(self, project, verb, **fields):
        """Ask a verb; returns {"result", "steps", "error"}."""
        return self._call("POST", "/verb", {"Project": project, "Verb": verb, **fields})

    def must(self, project, verb, **fields):
        answer = self.verb(project, verb, **fields)
        assert not answer.get("error"), f"{verb}: {answer['error']}"
        return answer["result"]

    def feed(self, project, feed):
        return self._call("GET", f"/feed?project={project}&feed={feed}")

    def preview(self, template):
        """The address template the hub gives a link from now on."""
        self._call("POST", "/preview", {"Template": template})

    def drop(self, project):
        self._call("POST", f"/drop?project={project}")


def eventually(check, seconds=30):
    deadline = time.time() + seconds
    while time.time() < deadline:
        found = check()
        if found:
            return found
        time.sleep(0.2)
    return check()
