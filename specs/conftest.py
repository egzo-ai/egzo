# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""Fixtures for the egzo spec suite.

The suite drives the real `egzo` binary as a black box (set EGZO_BIN, or put it on PATH).

A run tests egzo on ONE reference platform (ENGINES below), chosen with `--engine NAME` or EGZO_ENGINE,
or, when neither is given, by select_platform.py from what this machine has (the report is printed in
the header of the run). A platform says what the host is like: a rootful Docker host, a Docker host that
has gVisor as its default runtime, rootful or rootless Podman. It does not ask egzo for anything: egzo never
chooses a runtime, so the platform is what makes agents run under gVisor. A spec that needs a feature a platform
lacks is expected to fail there, and is marked so. Nothing is probed or skipped for it: egzo is asked, and fails or not.
"""

import copy
import hashlib
import shlex
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

import clitree

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

    def sh(self, command, *arguments, **kwargs):
        """Run an egzo command line written as the shell would take it: `p.sh("agent spawn coder issue-1 -m hi")`.
        `arguments` are appended as they are (a text with spaces, a path). The line is checked against the CLI's own help
        first, so a spec that names a command or a flag egzo does not have fails as itself. Use `run` to try a bad line."""
        argv = [*shlex.split(command), *map(str, arguments)]
        wrong = clitree.problem(self.egzo.binary, argv)
        if wrong:
            pytest.fail(f"this spec runs a command egzo does not have: egzo {command}\n{wrong}", pytrace=False)
        return self.run(*argv, **kwargs)

    def pty(self, command, *arguments, dimensions=(24, 80), timeout=30, env=None):
        """Run an egzo command line on a terminal, for what needs one (`agent attach`, `exec` without `-T`): returns the pexpect child.
        The line is checked against the CLI's help like `sh`."""
        import pexpect

        argv = [*shlex.split(command), *map(str, arguments)]
        wrong = clitree.problem(self.egzo.binary, argv)
        if wrong:
            pytest.fail(f"this spec runs a command egzo does not have: egzo {command}\n{wrong}", pytrace=False)
        environment = {**os.environ, **self.env, **(env or {})}
        environment.pop("EGZO_PROJECT_NAME", None)
        return pexpect.spawn(self.egzo.binary, argv, cwd=self.root, env=environment, dimensions=dimensions, encoding=None, timeout=timeout)

    def ok(self, command, *arguments, **kwargs):
        """`sh`, which must exit 0."""
        result = self.sh(command, *arguments, **kwargs)
        assert result.returncode == 0, f"egzo {command} {' '.join(map(str, arguments))}\n{result.stderr}"
        return result

    def ps(self, *args):
        """`egzo ps` as {name: row}: the sidecars and the instances, by the columns a person reads."""
        from support import table

        return {row["NAME"]: row for row in table(self.ok("ps", *args).stdout)}

    def ps_json(self):
        """`egzo ps --json` as {name: object}."""
        return {item["name"]: item for item in json.loads(self.ok("ps --json").stdout)}

    def instance_names(self):
        return sorted(item["name"] for item in json.loads(self.ok("agent ls --json").stdout))

    def retire(self):
        """Remove every instance, in one command, so the next spec of a shared project finds it as it was left by `up`."""
        names = self.instance_names()
        if names:
            self.ok("agent rm --force", *names)

    def config(self, document, *args, **kwargs):
        """Write the file and run `egzo config`."""
        self.write(document)
        return self.run("project", "check", "--resolved", *args, **kwargs)

    def resolved(self, document, **kwargs):
        """The resolved configuration; the spec fails if the file is rejected."""
        result = self.config(document, **kwargs)
        assert result.returncode == 0, f"egzo project check rejected the file:\n{result.stderr}"
        return result.yaml()

    def container(self, instance):
        """The engine name of an instance's container: `<project>-<instance>`."""
        return f"{self.name}-{instance}"

    def volume(self, workspace):
        """The volume of a directory workspace."""
        return f"{self.name}_ws.{workspace}"

    def base_volume(self, workspace):
        """The volume of a git workspace's base clone."""
        return f"{self.name}_ws.{workspace}.base"

    def worktree_volume(self, workspace, instance):
        """The volume of an instance's worktree of a git workspace."""
        return f"{self.name}_ws.{workspace}.wt.{instance}"

    def left_in_directory(self):
        """What is in the project directory besides what the user wrote: egzo makes nothing there."""
        return sorted(str(p.relative_to(self.root)) for p in self.root.rglob("*") if p.name != "egzo.yaml")

    def up(self, document=None, **kwargs):
        """`egzo up`, which must succeed. With a document, writes it first. A template starts nothing:
        use spawn (or start) for an agent."""
        if document is not None:
            self.write(document)
        result = self.run("project", "up", **{"timeout": 300, **kwargs})
        assert result.returncode == 0, f"egzo up failed:\n{result.stderr}"
        return result

    def spawn(self, template, name=None, *args, **kwargs):
        """`egzo spawn TEMPLATE [NAME] ARGS...`, which must succeed; returns the instance name it printed.
        Without a name the instance is `<template>-<n>`, so `spawn("claude")` is `claude-1`, whose container
        is `<project>-claude-1`."""
        command = ["agent", "spawn", template, *([name] if name else []), *args]
        result = self.run(*command, **{"timeout": 300, **kwargs})
        assert result.returncode == 0, f"egzo spawn {template} failed:\n{result.stderr}"
        return result.stdout.strip().splitlines()[-1]

    def start(self, *templates, document=None, **kwargs):
        """`up`, then one instance of each template (auto names); returns their names in order. This is what
        a spec that needs running agents does: `up` itself starts none."""
        self.up(document, **kwargs)
        return [self.spawn(template) for template in templates]


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
        # Whether gVisor (runsc) is this reference platform's default runtime. It describes the host: egzo does
        # not choose a runtime, so every container the engine starts runs under it.
        self.gvisor = gvisor
        self.setup = setup
        self.env = {"DOCKER_HOST": host}
        # The ids the invoking user has inside a container. A rootless engine maps that user to root
        # of the container's user namespace; every other engine keeps the host ids.
        self.uid = 0 if rootless else os.getuid()
        self.gid = 0 if rootless else os.getgid()

    def __repr__(self):
        return self.name

    def command(self, *args):
        """The CLI's argv for talking to this engine's socket (Podman only in remote mode: its local storage is another one)."""
        return [self.cli, *args] if self.cli == "docker" else [self.cli, "--remote", "--url", self.host, *args]

    def run(self, *args, input=None):
        command = self.command(*args)
        # Build output from remote Podman is not always valid UTF-8.
        completed = subprocess.run(
            command, env={**os.environ, **self.env}, capture_output=True, text=True, errors="replace", input=input
        )
        return Result(completed.returncode, completed.stdout, completed.stderr)

    def put_file(self, container, source, destination):
        """Put a text file into a running container. Through the container's own shell, not `cp`: a sandboxed runtime such as
        gVisor keeps its own view of the root filesystem, and what `cp` writes behind its back is not there."""
        return self.run("exec", "-i", container, "sh", "-c", f"cat > {destination}", input=Path(source).read_text())

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
        if self.cli == "docker" and (self.default_runtime() == "runsc") != self.gvisor:
            return False, f"the engine at {self.host} has {self.default_runtime()!r} as its default runtime" + (
                ": it is a docker-gvisor host" if not self.gvisor else ", not runsc (gVisor)"
            )
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
        tag = "egzo-spec-agent:4"
        if self.run("image", "inspect", tag).returncode == 0:
            return tag
        with tempfile.TemporaryDirectory() as context:
            (Path(context) / "Dockerfile").write_text(
                'FROM docker.io/library/alpine:3\nRUN apk add --no-cache curl git python3\nCMD ["sleep", "infinity"]\n'
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

    def default_runtime(self):
        """The engine's default OCI runtime (Docker only): runsc on a gVisor platform, usually runc."""
        info = self.run("info", "--format", "{{.DefaultRuntime}}")
        return info.stdout.strip() if info.returncode == 0 else ""

    def exec(self, container, *command, detach=False):
        flags = ["-d"] if detach else []
        return self.run("exec", *flags, container, *command)

    def volume_exists(self, name):
        return self.run("volume", "inspect", name).returncode == 0

    def in_volumes(self, volumes, command, *, read_only=False, workdir=None):
        """Run `sh -c command` as root in a throwaway container (the spec agent image: alpine and git) that mounts
        volumes, a dict of volume name to mount point. This is how the specs look inside a workspace volume, which
        the host cannot browse."""
        flags = ["--rm", "--network", "none", "-u", "0"]
        for name, target in volumes.items():
            flags += ["-v", f"{name}:{target}{':ro' if read_only else ''}"]
        if workdir:
            flags += ["-w", workdir]
        return self.run("run", *flags, "--entrypoint", "sh", self.agent_image(), "-c", command)

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

    def instances(self, project):
        """The containers of a project that are agent instances."""
        return [r for r in self.containers(project) if r.labels.get(f"{LABEL_PREFIX}kind") == "agent"]

    def instance(self, project, name):
        """The container of one instance, or None."""
        found = [r for r in self.instances(project) if r.labels.get(f"{LABEL_PREFIX}instance") == name]
        return found[0] if found else None

    def cleanup(self, project):
        for resource in self.containers(project):
            self.run("rm", "-f", resource.name)
        for kind, command in (("network", "network"), ("volume", "volume")):
            for resource in self._list(kind, project):
                self.run(command, "rm", "-f", resource.name)


def _rootless_podman_socket():
    return f"unix://{os.environ.get('XDG_RUNTIME_DIR', f'/run/user/{os.getuid()}')}/podman/podman.sock"


DOCKER_SETUP = "install Docker Engine and make /var/run/docker.sock usable by this user (docker group)"
GVISOR_SETUP = (
    'install gVisor, register it and make it the default: {"runtimes": {"runsc": {"path": "/usr/bin/runsc"}}, "default-runtime": "runsc"} '
    "in /etc/docker/daemon.json"
)

# The supported reference platforms. A run tests exactly one.
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


def require_reachable(host):
    """The specs that go through the proxy need the real internet: without it they fail, never skip."""
    if not _reachable(host):
        pytest.fail(f"these specs need to reach {host}:443 from this host, and cannot", pytrace=False)


@pytest.fixture
def internet():
    require_reachable("example.com")


@pytest.fixture
def httpbin():
    require_reachable("httpbin.org")


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
    created.run("project", "down", "--force")
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


@pytest.fixture(scope="module")
def shared_project(tmp_path_factory, engine, egzo):
    """A project bound to the engine that lasts for a whole module, for the specs that only need it running and take what
    they change from agents of their own. Removed, with everything it made, when the module is done."""
    root = tmp_path_factory.mktemp("shared") / f"spec-{secrets.token_hex(4)}"
    created = Project(root, egzo, {**engine.env, "EGZO_IMAGE": engine.image_for(egzo.binary)})
    yield created
    created.run("project", "down", "--force")
    engine.cleanup(created.name)


@pytest.fixture(scope="module")
def team(shared_project, engine):
    """The shared project, up, with the template `worker` and two instances of it, `coder` and `reviewer` (see agentapi.Team)."""
    from agentapi import Team

    return Team(engine, shared_project, engine.agent_image())


@pytest.fixture
def pair(team):
    """Two fresh instances of `worker`, for a spec whose counts must not include what other specs did."""
    return team.pair()
