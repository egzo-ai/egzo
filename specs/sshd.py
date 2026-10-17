# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""A real OpenSSH server for the specs that need something to ssh to, started in a container of the engine under test.

It is the first piece of a server of the specs' own, so that no spec has to reach a site on the internet: the container
answers on the engine's default network, at an address the proxy and the agents can reach. Its keys are made when it starts
and read back, so a spec can pin the host key, give the proxy the client key, or make a different pair to be refused."""

import hashlib
import os
import socket
import subprocess
import threading
from pathlib import Path

IMAGE_DIR = Path(__file__).parent / "fixtures" / "sshd"
READY = "Server listening on"
USER = "ops"


class SshServer:
    """An sshd that accepts the key `private_key` for `user` and nobody else."""

    def __init__(self, engine, name, process, host):
        self.engine = engine
        self.name = name
        self.image = engine.inspect(name)["Config"]["Image"]
        self.process = process
        self.user = USER
        self.host = host
        # Published on the host's own address, which every engine lets a container reach (a rootless Podman gives
        # containers no address of their own that another container can use).
        self.port = int(engine.run("port", name, "22/tcp").stdout.splitlines()[0].rsplit(":", 1)[1])
        self.private_key = self.sh("cat /home/ops/.ssh/id_ed25519").stdout
        self.public_key = self.sh("cat /home/ops/.ssh/id_ed25519.pub").stdout.strip()
        self.host_key = self.sh("cat /etc/ssh/ssh_host_ed25519_key.pub").stdout.strip()
        # `256 SHA256:... root@x (ED25519)`: the second field is what `ssh-keygen -lf` and ssh print.
        self.fingerprint = self.sh("ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub").stdout.split()[1]

    def sh(self, script, user="root"):
        """A shell command inside the server, as root unless told otherwise."""
        return self.engine.run("exec", "-u", user, self.name, "sh", "-c", script)

    def keygen(self):
        """A key pair the server does not know: (private, public, fingerprint)."""
        made = self.sh("d=$(mktemp -d) && ssh-keygen -q -t ed25519 -N '' -f $d/k && cat $d/k $d/k.pub && ssh-keygen -lf $d/k.pub && rm -rf $d")
        lines = made.stdout.splitlines()
        private = "\n".join(lines[:-2]) + "\n"
        return private, lines[-2], lines[-1].split()[1]

    def stop(self):
        self.engine.run("rm", "-f", self.name)
        self.process.terminate()
        self.process.wait(timeout=30)


def host_address():
    """This machine's address on the network it reaches the world through (no packet is sent)."""
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as probe:
        probe.connect(("192.0.2.1", 9))
        return probe.getsockname()[0]


def start(engine):
    """Build the image (once per content), start a server and return it when sshd says it listens."""
    digest = hashlib.sha256(b"".join(p.read_bytes() for p in sorted(IMAGE_DIR.iterdir()))).hexdigest()[:12]
    tag = f"egzo-spec-sshd:{digest}"
    if engine.run("image", "inspect", tag).returncode != 0:
        built = engine.run("build", "-t", tag, str(IMAGE_DIR))
        assert built.returncode == 0, f"could not build the sshd image with {engine.cli}:\n{built.stderr[-3000:]}"
    host = host_address()
    reach = host
    if engine.cli == "podman" and engine.rootless:
        # pasta gives a container the host's own address, so that address is the container itself: the host is
        # reached under the name Podman defines for it.
        reach = engine.run("run", "--rm", "docker.io/library/alpine:3", "sh", "-c", "getent hosts host.containers.internal").stdout.split()[0]
    name = f"egzo-spec-sshd-{os.getpid()}"
    engine.run("rm", "-f", name)
    # Attached: the log is read as it comes, so there is nothing to poll. sshd logs to stderr (-e).
    process = subprocess.Popen(
        engine.command("run", "--rm", "--name", name, "-p", f"{host}::22", tag),
        env={**os.environ, **engine.env}, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, errors="replace",
    )
    seen = []
    ready = threading.Event()

    def watch():
        for line in process.stdout:
            seen.append(line)
            if READY in line:
                ready.set()
        ready.set()

    threading.Thread(target=watch, daemon=True).start()
    if not ready.wait(120) or not any(READY in line for line in seen):
        process.kill()
        engine.run("rm", "-f", name)
        raise AssertionError("the sshd of the specs did not start:\n" + "".join(seen)[-3000:])
    return SshServer(engine, name, process, reach)
