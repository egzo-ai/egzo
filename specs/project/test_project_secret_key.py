# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""The project's secret key at rest: the CA's private key in the proxy's volume is sealed with it, a restarted proxy opens it again with
the key `up` delivers, another key stops `up` with the two ways on, `rotate-ca` moves the project to a new key, and a project made before
the key keeps its CA. The file's side (`secret_key` is required, at least 32 bytes) is in `test_secret_key.py`."""

import shutil
import subprocess

import pytest

from support import SECRET_KEY_VALUE, SECRET_KEY_VARIABLE, custom, spec

OTHER_KEY = "another-spec-secret-key-" + "fedcba9876543210" * 3


def document(image):
    # example.com is inspected, so the proxy terminates TLS for it: a certificate minted with the CA's key, which must be open.
    return spec(egress={"default": {"services": {"docs": {"hosts": ["example.com"], "inspect": True}}}}, agents={"coder": custom(image)})


def proxy_volume(engine, project):
    proxy = next(c for c in engine.containers(project.name) if c.name == f"{project.name}-proxy-1")
    return {m["Destination"]: m["Name"] for m in proxy.raw["Mounts"]}["/ca-private"]


def volume_files(engine, project, image):
    """What the proxy keeps in its private volume, file by file, read from a container of its own."""
    listed = engine.run("run", "--rm", "-v", f"{proxy_volume(engine, project)}:/v:ro", image, "sh", "-c", 'for f in /v/*; do echo "== $f"; cat "$f"; done')
    assert listed.returncode == 0, listed.stderr
    return listed.stdout


def ca_of(engine, project, name):
    return engine.exec(engine.instance(project.name, name).name, "cat", "/etc/egzo/ca/ca.crt").stdout


def reaches_example(engine, project, name):
    reached = engine.exec(engine.instance(project.name, name).name, "sh", "-c", 'curl -sS -m 20 -o /dev/null -w "%{http_code}" https://example.com/')
    return reached.stdout.strip() == "200"


def test_the_ca_key_is_sealed_another_key_stops_up_and_rotate_ca_moves_the_project_to_a_new_key(live_project, engine, agent_image, internet):
    p = live_project
    p.write(document(agent_image))
    short = p.sh("project up", env={SECRET_KEY_VARIABLE: "x" * 31}, timeout=300)
    assert short.returncode != 0 and "32 bytes" in short.stderr
    assert engine.containers(p.name) == [], "a short key is refused before anything is created"

    p.ok("project up", timeout=300)
    name = p.spawn("coder")
    first = ca_of(engine, p, name)
    assert first.startswith("-----BEGIN CERTIFICATE-----") and reaches_example(engine, p, name)
    at_rest = volume_files(engine, p, agent_image)
    assert "ca.crt" in at_rest
    assert "PRIVATE KEY" not in at_rest and SECRET_KEY_VALUE not in at_rest, "the CA key is not in clear, and the secret key is not kept"

    p.ok("restart proxy", timeout=300)
    assert reaches_example(engine, p, name), "the restarted proxy opened the CA again with the key `restart` delivered"

    other = p.sh("project up", env={SECRET_KEY_VARIABLE: OTHER_KEY}, timeout=300)
    assert other.returncode != 0
    assert "secret key" in other.stderr and "rotate-ca" in other.stderr, other.stderr
    assert OTHER_KEY not in other.stdout + other.stderr and SECRET_KEY_VALUE not in other.stdout + other.stderr

    p.ok("project rotate-ca", env={SECRET_KEY_VARIABLE: OTHER_KEY}, timeout=300)
    p.ok("project up", env={SECRET_KEY_VARIABLE: OTHER_KEY}, timeout=300)
    rotated = ca_of(engine, p, name)
    assert rotated.startswith("-----BEGIN CERTIFICATE-----") and rotated != first
    assert reaches_example(engine, p, name)
    assert p.sh("project up", timeout=300).returncode != 0, "the old key no longer opens the CA"


def test_a_project_made_before_the_secret_key_keeps_its_ca_and_the_key_is_sealed_at_the_next_up(live_project, engine, agent_image, internet, tmp_path):
    """Before the secret key, the proxy kept `ca.key` and `ca.crt` in clear in its volume."""
    if shutil.which("openssl") is None:
        pytest.fail("this spec makes an old-style CA with openssl, which is not on PATH", pytrace=False)
    p = live_project
    p.write(document(agent_image))
    p.ok("project up", timeout=300)
    p.ok("stop proxy", timeout=300)

    old = tmp_path / "old-ca"
    old.mkdir()
    subprocess.run(["openssl", "ecparam", "-name", "prime256v1", "-genkey", "-noout", "-out", old / "ca.key"], check=True, capture_output=True)
    subprocess.run(
        ["openssl", "req", "-x509", "-new", "-key", old / "ca.key", "-days", "30", "-subj", "/CN=egzo project CA/O=egzo", "-out", old / "ca.crt",
         "-addext", "basicConstraints=critical,CA:TRUE,pathlen:0", "-addext", "keyUsage=critical,keyCertSign,cRLSign"],
        check=True, capture_output=True,
    )
    (old / "ca.key").chmod(0o644)
    replaced = engine.run("run", "--rm", "-v", f"{proxy_volume(engine, p)}:/v", "-v", f"{old}:/old:ro", agent_image,
                          "sh", "-c", 'owner=$(stat -c %u:%g /v/ca.crt) && rm -f /v/* && cp /old/ca.key /old/ca.crt /v/ && chown "$owner" /v/ca.key /v/ca.crt && chmod 600 /v/ca.key')
    assert replaced.returncode == 0, replaced.stderr

    p.ok("project up", timeout=300)
    name = p.spawn("coder")
    assert ca_of(engine, p, name).strip() == (old / "ca.crt").read_text().strip(), "same CA: no agent has to learn a new one"
    assert reaches_example(engine, p, name)
    at_rest = volume_files(engine, p, agent_image)
    assert "PRIVATE KEY" not in at_rest, "the first up with a key sealed the CA key and removed the clear one"
