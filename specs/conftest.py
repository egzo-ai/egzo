"""Fixtures for the egzo spec suite.

The suite drives the real `egzo` binary as a black box (set EGZO_BIN, or put it on PATH). Specs that
need a container engine use the `engine` fixture and run once per available engine; set
EGZO_SPEC_ENGINES (default "docker,podman") to restrict the matrix.
"""

import hashlib
import json
import os
import secrets
import shutil
import subprocess
import tempfile
from dataclasses import dataclass, field
from pathlib import Path

import pytest
import yaml

pytest_plugins = ["pytester", "spec_status"]

LABEL_PREFIX = "ai.egzo."


@dataclass
class Result:
    returncode: int
    stdout: str
    stderr: str

    def yaml(self):
        return yaml.safe_load(self.stdout)


class Egzo:
    """Runs the egzo binary."""

    def __init__(self, binary):
        self.binary = binary

    def run(self, *args, cwd=None, env=None, input=None, timeout=120):
        environment = dict(os.environ)
        environment.pop("EGZO_PROJECT_NAME", None)
        environment["NO_COLOR"] = "1"
        environment.update(env or {})
        completed = subprocess.run(
            [self.binary, *map(str, args)],
            cwd=cwd,
            env=environment,
            input=input,
            capture_output=True,
            text=True,
            timeout=timeout,
        )
        return Result(completed.returncode, completed.stdout, completed.stderr)


class Project:
    """A temporary project directory holding an egzo.yaml."""

    def __init__(self, root, egzo, env=None):
        self.root = root
        self.egzo = egzo
        self.env = dict(env or {})
        self.root.mkdir(parents=True)

    @property
    def name(self):
        return self.root.name

    def mkdir(self, relative):
        path = self.root / relative
        path.mkdir(parents=True, exist_ok=True)
        return path

    def write(self, document, filename="egzo.yaml"):
        text = document if isinstance(document, str) else yaml.safe_dump(document, sort_keys=False)
        path = self.root / filename
        path.write_text(text)
        return path

    def run(self, *args, env=None, **kwargs):
        return self.egzo.run(*args, cwd=self.root, env={**self.env, **(env or {})}, **kwargs)

    def config(self, document, *args, **kwargs):
        """Write the file and run `egzo config`."""
        self.write(document)
        return self.run("config", *args, **kwargs)

    def resolved(self, document, **kwargs):
        """The resolved configuration; the spec fails if the file is rejected."""
        result = self.config(document, **kwargs)
        assert result.returncode == 0, f"egzo config rejected the file:\n{result.stderr}"
        return result.yaml()


@dataclass
class Resource:
    kind: str
    name: str
    labels: dict
    raw: dict = field(repr=False)


class Engine:
    """A container engine CLI (docker or podman) used to observe what egzo created."""

    def __init__(self, cli):
        self.cli = cli
        self.env = {}
        if cli == "podman":
            runtime_dir = os.environ.get("XDG_RUNTIME_DIR", f"/run/user/{os.getuid()}")
            self.env["DOCKER_HOST"] = f"unix://{runtime_dir}/podman/podman.sock"

    def run(self, *args):
        completed = subprocess.run(
            [self.cli, *args],
            env={**os.environ, **self.env},
            capture_output=True,
            text=True,
        )
        return Result(completed.returncode, completed.stdout, completed.stderr)

    def usable(self):
        return self.run("version").returncode == 0

    def image_for(self, binary):
        """A local image holding the egzo binary, which stands in for the published sidecar image."""
        digest = hashlib.sha256(Path(binary).read_bytes() + Path("/etc/ssl/certs/ca-certificates.crt").read_bytes()).hexdigest()[:12]
        tag = f"egzo-spec:{digest}"
        if self.run("image", "inspect", tag).returncode == 0:
            return tag
        with tempfile.TemporaryDirectory() as context:
            shutil.copy(binary, Path(context) / "egzo")
            # a scratch image has no roots: the proxy needs them to verify upstream servers
            shutil.copy("/etc/ssl/certs/ca-certificates.crt", Path(context) / "ca-certificates.crt")
            (Path(context) / "Dockerfile").write_text(
                "FROM scratch\nCOPY ca-certificates.crt /etc/ssl/certs/ca-certificates.crt\nCOPY egzo /egzo\n"
            )
            built = self.run("build", "-t", tag, context)
        assert built.returncode == 0, f"could not build the sidecar image with {self.cli}:\n{built.stderr}"
        return tag

    def agent_image(self):
        """A small image that stays up, standing in for a harness (the custom harness takes any image)."""
        tag = "egzo-spec-agent:2"
        if self.run("image", "inspect", tag).returncode == 0:
            return tag
        with tempfile.TemporaryDirectory() as context:
            (Path(context) / "Dockerfile").write_text('FROM docker.io/library/alpine:3\nRUN apk add --no-cache curl\nCMD ["sleep", "infinity"]\n')
            built = self.run("build", "-t", tag, context)
        assert built.returncode == 0, f"could not build the agent image with {self.cli}:\n{built.stderr}"
        return tag

    def has_runtime(self, name):
        """Whether the engine has an OCI runtime registered (for example runsc for gVisor)."""
        if self.cli == "docker":
            info = self.run("info", "--format", "{{json .Runtimes}}")
            return info.returncode == 0 and f'"{name}"' in info.stdout
        info = self.run("info", "--format", "{{json .Host.OCIRuntime}}")
        return info.returncode == 0 and name in info.stdout

    def exec(self, container, *command, detach=False):
        flags = ["-d"] if detach else []
        return self.run("exec", *flags, container, *command)

    def _list(self, kind, project):
        listing = {
            "container": ["ps", "-aq"],
            "volume": ["volume", "ls", "-q"],
            "network": ["network", "ls", "-q"],
        }[kind]
        label = ["--filter", f"label={LABEL_PREFIX}project={project}"]
        names = self.run(*listing, *label).stdout.split()
        resources = []
        for name in names:
            inspect = {"container": ["inspect"], "volume": ["volume", "inspect"], "network": ["network", "inspect"]}[kind]
            raw = json.loads(self.run(*inspect, name).stdout)[0]
            if kind == "container":
                labels = raw.get("Config", {}).get("Labels")
            else:  # podman reports network labels in lowercase
                labels = raw.get("Labels") or raw.get("labels")
            labels = labels or {}
            real_name = (raw.get("Name") or raw.get("name") or name).lstrip("/")  # ps -q and ls -q print ids
            resources.append(Resource(kind, real_name, labels, raw))
        return resources

    def resources(self, project):
        """Every container, volume and network labelled as belonging to the project."""
        return [r for kind in ("container", "volume", "network") for r in self._list(kind, project)]

    def containers(self, project):
        return self._list("container", project)

    def cleanup(self, project):
        for resource in self.containers(project):
            self.run("rm", "-f", resource.name)
        for kind, command in (("network", "network"), ("volume", "volume")):
            for resource in self._list(kind, project):
                self.run(command, "rm", "-f", resource.name)


def _available_engines():
    wanted = os.environ.get("EGZO_SPEC_ENGINES", "docker,podman").split(",")
    return [name for name in wanted if shutil.which(name)]


def pytest_generate_tests(metafunc):
    if "engine" in metafunc.fixturenames:
        metafunc.parametrize("engine", _available_engines() or ["unavailable"], indirect=True)


@pytest.fixture
def engine(request):
    if request.param == "unavailable":
        pytest.skip("no container engine CLI found")
    candidate = Engine(request.param)
    if not candidate.usable():
        pytest.skip(f"{request.param} is installed but not usable by this user")
    return candidate


@pytest.fixture
def agent_image(engine):
    return engine.agent_image()


@pytest.fixture(scope="session")
def egzo():
    binary = os.environ.get("EGZO_BIN") or shutil.which("egzo")
    if not binary:
        pytest.fail("egzo binary not found: build it and set EGZO_BIN (or put it on PATH)", pytrace=False)
    return Egzo(binary)


@pytest.fixture
def make_project(tmp_path, egzo):
    """Factory for project directories with unique, valid project names."""

    def factory(env=None):
        return Project(tmp_path / f"spec-{secrets.token_hex(4)}", egzo, env)

    return factory


@pytest.fixture
def project(make_project):
    return make_project()


@pytest.fixture
def live_project(make_project, engine, egzo):
    """A project bound to an engine; everything it created is removed afterwards."""
    created = make_project(env={**engine.env, "EGZO_IMAGE": engine.image_for(egzo.binary)})
    yield created
    created.run("down", "--volumes")
    engine.cleanup(created.name)
