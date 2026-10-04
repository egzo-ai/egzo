"""Fixtures for the egzo spec suite.

The suite drives the real `egzo` binary as a black box (set EGZO_BIN, or put it on PATH). Specs that
need a container engine use the `engine` fixture and run once per supported engine (ENGINES below).
Every engine is required: a host that lacks one FAILS those specs, with what to set up. Nothing is
skipped for a missing engine, because a green suite must mean every supported engine works.
"""

import copy
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
            errors="replace",
            timeout=timeout,
        )
        return Result(completed.returncode, completed.stdout, completed.stderr)


class Project:
    """A temporary project directory holding an egzo.yaml."""

    def __init__(self, root, egzo, env=None):
        self.root = root
        self.egzo = egzo
        self.env = dict(env or {})
        # When set, every agent a written document leaves without a runtime gets this one.
        self.default_runtime = None
        self.root.mkdir(parents=True)

    @property
    def name(self):
        return self.root.name

    def mkdir(self, relative):
        path = self.root / relative
        path.mkdir(parents=True, exist_ok=True)
        return path

    def write(self, document, filename="egzo.yaml"):
        if self.default_runtime and isinstance(document, dict):
            document = copy.deepcopy(document)
            for agent in (document.get("agents") or {}).values():
                if isinstance(agent, dict):
                    agent.setdefault("runtime", self.default_runtime)
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
    """A supported container engine, reached through its Docker-compatible socket.

    The CLI (docker, or podman in remote mode) talks to the same socket egzo does, so the specs
    observe exactly what egzo sees, and the socket is all a host has to provide."""

    def __init__(self, name, cli, host, rootless=False, runtime=None, setup=""):
        self.name = name
        self.cli = cli
        self.host = host
        self.rootless = rootless
        # An OCI runtime every agent of a spec project gets (the gVisor matrix entry).
        self.runtime = runtime
        self.setup = setup
        self.env = {"DOCKER_HOST": host}

    def __repr__(self):
        return self.name

    def run(self, *args):
        command = [self.cli, *args] if self.cli == "docker" else [self.cli, "--remote", "--url", self.host, *args]
        # Build output from remote Podman is not always valid UTF-8.
        completed = subprocess.run(
            command, env={**os.environ, **self.env}, capture_output=True, text=True, errors="replace"
        )
        return Result(completed.returncode, completed.stdout, completed.stderr)

    def problem(self):
        """Why this host cannot run the specs against this engine, or None when it can."""
        if not shutil.which(self.cli):
            return f"the {self.cli} CLI is not installed"
        if self.cli == "docker":
            # `docker version` fails when the engine is unreachable. A podman-docker symlink would
            # make a Docker entry silently test Podman, so check what answers.
            probe = self.run("version", "--format", "{{json .Server}}")
        else:
            # `podman version` exits 0 without a connection; `info` needs one.
            probe = self.run("info", "--format", "{{.Host.Security.Rootless}}")
        if probe.returncode != 0:
            return f"no usable engine at {self.host}: {probe.stderr.strip()}"
        if self.cli == "docker" and "podman" in probe.stdout.lower():
            return f"the engine at {self.host} is Podman, not Docker"
        if self.cli == "podman" and (probe.stdout.strip() == "true") != self.rootless:
            return f"the Podman at {self.host} is {'rootless' if self.rootless else 'rootful'} in name only: it reports otherwise"
        if self.runtime and not self.has_runtime(self.runtime):
            return f"the engine at {self.host} has no {self.runtime} runtime registered"
        return None

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


def _rootless_podman_socket():
    return f"unix://{os.environ.get('XDG_RUNTIME_DIR', f'/run/user/{os.getuid()}')}/podman/podman.sock"


DOCKER_SETUP = "install Docker Engine and make /var/run/docker.sock usable by this user (docker group)"
GVISOR_SETUP = 'install gVisor and register it: {"runtimes": {"runsc": {"path": "/usr/bin/runsc"}}} in /etc/docker/daemon.json'

# Every supported engine. All of them are required: the engine fixture fails when one is missing.
ENGINES = [
    Engine("docker", "docker", "unix:///var/run/docker.sock", setup=DOCKER_SETUP),
    Engine("docker-gvisor", "docker", "unix:///var/run/docker.sock", runtime="runsc", setup=f"{DOCKER_SETUP}; {GVISOR_SETUP}"),
    Engine(
        "podman",
        "podman",
        "unix:///run/podman/podman.sock",
        setup="install Podman, run `systemctl enable --now podman.socket` as root, and make "
        "/run/podman/podman.sock usable by this user (SocketGroup= and SocketMode=0660 in a podman.socket drop-in, "
        "and this user in that group)",
    ),
    Engine(
        "podman-rootless",
        "podman",
        _rootless_podman_socket(),
        rootless=True,
        setup="install Podman and run `systemctl --user enable --now podman.socket`",
    ),
]

_problems = {}


def pytest_generate_tests(metafunc):
    if "engine" in metafunc.fixturenames:
        metafunc.parametrize("engine", ENGINES, ids=[e.name for e in ENGINES], indirect=True)


@pytest.fixture
def engine(request):
    candidate = request.param
    if candidate.name not in _problems:
        _problems[candidate.name] = candidate.problem()
    if _problems[candidate.name]:
        pytest.fail(
            f"required engine '{candidate.name}' is not available: {_problems[candidate.name]}\n"
            f"to set it up: {candidate.setup}",
            pytrace=False,
        )
    return candidate


@pytest.fixture
def agent_image(engine):
    return engine.agent_image()


@pytest.fixture(scope="session")
def egzo():
    configured = os.environ.get("EGZO_BIN")
    # Specs run egzo from temporary project directories, so a relative EGZO_BIN would stop pointing at it.
    binary = os.path.abspath(configured) if configured else shutil.which("egzo")
    if not binary or not os.access(binary, os.X_OK):
        pytest.fail(
            f"egzo binary not found or not executable: {binary or configured or 'egzo'}; "
            "build it (make build) and set EGZO_BIN, or put it on PATH",
            pytrace=False,
        )
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
    created.default_runtime = engine.runtime
    yield created
    created.run("down", "--volumes")
    engine.cleanup(created.name)
