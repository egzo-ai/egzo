"""Fixtures for the egzo spec suite.

The suite drives the real `egzo` binary as a black box (set EGZO_BIN, or put it on PATH).

A run tests egzo on ONE reference platform (ENGINES below), chosen with `--engine NAME` or EGZO_ENGINE,
or, when neither is given, by select_platform.py from what this machine has (the report is printed in
the header of the run). A platform says what the host is like: a rootful Docker host, a Docker host that
has gVisor registered, rootful or rootless Podman. It does not ask egzo for anything: specs that use a
feature the platform lacks (an agent with `runtime: runsc` on a host without gVisor) are expected to
fail there, and are marked so. Nothing is probed or skipped for them: egzo is asked, and fails or not.
"""

import copy
import hashlib
import json
import os
import secrets
import shutil
import socket
import subprocess
import sys
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
    """A supported container engine, reached through its Docker-compatible socket.

    The CLI (docker, or podman in remote mode) talks to the same socket egzo does, so the specs
    observe exactly what egzo sees, and the socket is all a host has to provide."""

    def __init__(self, name, cli, host, rootless=False, gvisor=False, setup=""):
        self.name = name
        self.cli = cli
        self.host = host
        self.rootless = rootless
        # Whether this reference platform has gVisor (runsc) registered. It only describes the host:
        # no agent runs under it unless a spec asks for `runtime: runsc`.
        self.gvisor = gvisor
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
        return None

    def available(self):
        """Whether this host is this reference platform: (True, "") or (False, why not). Used to choose
        a platform automatically; a platform chosen by name is not second-guessed."""
        problem = self.problem()
        if problem:
            return False, problem
        if self.gvisor and not self.has_runtime("runsc"):
            return False, f"the engine at {self.host} has no runsc (gVisor) runtime registered"
        if not self.gvisor and self.has_runtime("runsc"):
            return True, "has gVisor registered too (docker-gvisor is the better match)"
        return True, ""

    def image_for(self, binary):
        """A local image holding the egzo binary and git, which stands in for the published sidecar image."""
        digest = hashlib.sha256(b"alpine-git:" + Path(binary).read_bytes()).hexdigest()[:12]
        tag = f"egzo-spec:{digest}"
        if self.run("image", "inspect", tag).returncode == 0:
            return tag
        with tempfile.TemporaryDirectory() as context:
            shutil.copy(binary, Path(context) / "egzo")
            # like the published image: CA roots for the proxy, and git (>= 2.47) for the prep role
            (Path(context) / "Dockerfile").write_text(
                "FROM docker.io/library/alpine:3\n"
                "RUN apk add --no-cache ca-certificates git\n"
                "COPY egzo /egzo\n"
            )
            built = self.run("build", "-t", tag, context)
        assert built.returncode == 0, f"could not build the sidecar image with {self.cli}:\n{built.stderr}"
        return tag

    def agent_image(self):
        """A small image that stays up, standing in for a harness (the custom harness takes any image)."""
        tag = "egzo-spec-agent:3"
        if self.run("image", "inspect", tag).returncode == 0:
            return tag
        with tempfile.TemporaryDirectory() as context:
            (Path(context) / "Dockerfile").write_text(
                'FROM docker.io/library/alpine:3\nRUN apk add --no-cache curl git\nCMD ["sleep", "infinity"]\n'
            )
            built = self.run("build", "-t", tag, context)
        assert built.returncode == 0, f"could not build the agent image with {self.cli}:\n{built.stderr}"
        return tag

    def session_image(self, binary):
        """An agent image whose entrypoint is the egzo session holder, running the fake TUI of the specs."""
        script = Path(__file__).parent / "fixtures" / "fake-tui.sh"
        digest = hashlib.sha256(Path(binary).read_bytes() + script.read_bytes()).hexdigest()[:12]
        tag = f"egzo-spec-session:{digest}"
        if self.run("image", "inspect", tag).returncode == 0:
            return tag
        with tempfile.TemporaryDirectory() as context:
            shutil.copy(script, Path(context) / "fake-tui")
            shutil.copy(binary, Path(context) / "egzo")
            (Path(context) / "Dockerfile").write_text(
                "FROM docker.io/library/alpine:3\n"
                "RUN apk add --no-cache curl git\n"
                "COPY fake-tui /usr/local/bin/fake-tui\n"
                "COPY egzo /usr/local/bin/egzo\n"
                'ENTRYPOINT ["/usr/local/bin/egzo", "agent", "run", "--"]\n'
                'CMD ["/usr/local/bin/fake-tui"]\n'
            )
            built = self.run("build", "-t", tag, context)
        assert built.returncode == 0, f"could not build the session image with {self.cli}:\n{built.stderr}"
        return tag

    def harness_image(self, name, binary):
        """The real harness image of the repository (harness/<name>), with the egzo binary of this run in it."""
        root = Path(__file__).parent.parent / "harness" / name
        assert (root / "Dockerfile").exists(), f"there is no harness image for {name}: harness/{name}/Dockerfile"
        sidecar = self.image_for(binary)
        digest = hashlib.sha256(
            Path(binary).read_bytes() + b"".join(p.read_bytes() for p in sorted(root.rglob("*")) if p.is_file())
        ).hexdigest()[:12]
        tag = f"egzo-harness-{name}:spec-{digest}"
        if self.run("image", "inspect", tag).returncode == 0:
            return tag
        built = self.run("build", "--build-arg", f"EGZO_IMAGE={sidecar}", "-t", tag, str(root))
        assert built.returncode == 0, f"could not build the {name} harness image:\n{built.stderr[-3000:]}"
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

# The supported reference platforms. A run tests exactly one. There is no podman-gvisor: Podman's
# Docker-compatible API cannot select a runtime (known-issues/podman-gvisor-unsupported.md).
ENGINES = [
    Engine("docker", "docker", "unix:///var/run/docker.sock", setup=DOCKER_SETUP),
    Engine("docker-gvisor", "docker", "unix:///var/run/docker.sock", gvisor=True, setup=f"{DOCKER_SETUP}; {GVISOR_SETUP}"),
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

def pytest_addoption(parser):
    parser.addoption(
        "--engine",
        default=os.environ.get("EGZO_ENGINE"),
        choices=[e.name for e in ENGINES],
        help="the reference platform this run tests (or set EGZO_ENGINE); default: chosen from this machine",
    )


def pytest_configure(config):
    import select_platform

    name = config.getoption("--engine")
    config._egzo_platform = select_platform.select(ENGINES, name)


def pytest_report_header(config):
    return config._egzo_platform.report.splitlines()


@pytest.fixture(scope="session")
def engine(request):
    """The one reference platform this run tests."""
    chosen = request.config._egzo_platform
    if chosen.engine is None:
        pytest.fail(chosen.report, pytrace=False)
    problem = chosen.engine.problem()
    if problem:
        pytest.fail(
            f"required platform '{chosen.engine.name}' is not available: {problem}\nto set it up: {chosen.engine.setup}",
            pytrace=False,
        )
    return chosen.engine


@pytest.fixture
def gvisor_agents(request, engine):
    """For specs that use the gVisor feature of egzo: they need a platform with gVisor, and are expected
    to fail without it. Strict, so a pass on a platform said to lack it is reported as well."""
    if not engine.gvisor:
        request.applymarker(pytest.mark.xfail(strict=True, reason=f"{engine.name} is a platform without gVisor"))


@pytest.fixture
def agent_image(engine):
    return engine.agent_image()


@pytest.fixture
def session_image(engine, egzo):
    return engine.session_image(egzo.binary)


@pytest.fixture
def harness_image(engine, egzo):
    """Factory: the image of a real harness, built from the repository."""
    return lambda name: engine.harness_image(name, egzo.binary)


def _reachable(host):
    try:
        socket.create_connection((host, 443), timeout=5).close()
        return True
    except OSError:
        return False


@pytest.fixture
def github():
    """The git specs clone a public repository: without the internet they fail, never skip."""
    if not _reachable("github.com"):
        pytest.fail("these specs need to reach github.com:443 from this host, and cannot", pytrace=False)


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
    yield created
    created.run("down", "--volumes")
    engine.cleanup(created.name)


class Registry:
    """A registry the specs can push to, to prove egzo pulls harness images by their default name."""

    def __init__(self, engine, host):
        self.engine = engine
        self.host = host

    def push(self, local, repository, tag):
        remote = f"{self.host}/{repository}:{tag}"
        assert self.engine.run("tag", local, remote).returncode == 0
        pushed = self.engine.run("push", remote)
        assert pushed.returncode == 0, f"could not push {remote}:\n{pushed.stderr}"
        return remote

    def forget(self, remote):
        self.engine.run("image", "rm", "-f", remote)


@pytest.fixture
def registry(engine):
    """The test registry (EGZO_SPEC_REGISTRY, default localhost:5000). Pushing needs EGZO_SPEC_REGISTRY_USER
    and a password in the file named by EGZO_SPEC_REGISTRY_PASSWORD_FILE (defaults: cedric, ~/.registry-password)."""
    host = os.environ.get("EGZO_SPEC_REGISTRY", "localhost:5000")
    try:
        socket.create_connection((host.split(":")[0], int(host.split(":")[1])), timeout=3).close()
    except OSError:
        pytest.fail(f"these specs need the test registry at {host}: set EGZO_SPEC_REGISTRY", pytrace=False)
    user = os.environ.get("EGZO_SPEC_REGISTRY_USER", "cedric")
    password_file = Path(os.environ.get("EGZO_SPEC_REGISTRY_PASSWORD_FILE", "~/.registry-password")).expanduser()
    if password_file.exists():
        login = subprocess.run(
            [engine.cli, "login", "-u", user, "--password-stdin", host],
            input=password_file.read_text().strip(), text=True, capture_output=True, env={**os.environ, **engine.env},
        )
        assert login.returncode == 0, f"could not log in to {host}:\n{login.stderr}"
    return Registry(engine, host)
