# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""What the OAuth specs share: a stub provider and API (fixtures/oauth_stub.py) on a project's egress network under two names, with a
certificate of a CA the project trusts upstream (`proxy.upstream`), and a sign-in made the way a client makes it at the end of a device-code
flow. No account and no internet are needed."""

import base64
import json
import subprocess
from pathlib import Path

import pytest

IDP, API = "idp.oauth-spec.test", "api.oauth-spec.test"
EXPIRES_IN = 200  # inside the proxy's renewal window from the start: it renews the chain on its own
STUB_LOG = "/tmp/stub.log"
CREDENTIAL = {"method": "oauth", "token_url": f"https://{IDP}/oauth/token", "revoke_url": f"https://{IDP}/oauth/revoke"}


def payload(token):
    part = token.split(".")[1]
    return json.loads(base64.urlsafe_b64decode(part + "=" * (-len(part) % 4)))


def make_certificates(directory):
    """A CA the project trusts upstream (`proxy.upstream.ca`), and the stub's certificate for both names."""
    def openssl(*args):
        subprocess.run(["openssl", *args], check=True, capture_output=True, cwd=directory)
    openssl("req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1", "-nodes", "-keyout", "ca.key", "-days", "2",
            "-subj", "/CN=oauth spec root", "-out", "stub-ca.pem")
    openssl("req", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1", "-nodes", "-keyout", "stub.key", "-subj", f"/CN={IDP}", "-out", "stub.csr")
    (directory / "san.ext").write_text(f"subjectAltName=DNS:{IDP},DNS:{API}\n")
    openssl("x509", "-req", "-in", "stub.csr", "-CA", "stub-ca.pem", "-CAkey", "ca.key", "-CAcreateserial", "-days", "2", "-extfile", "san.ext", "-out", "stub.crt")


class Stub:
    def __init__(self, engine, container):
        self.engine, self.container = engine, container

    def requests(self):
        log = self.engine.exec(self.container, "cat", STUB_LOG).stdout
        return [json.loads(line) for line in log.splitlines() if line.strip()]

    def grants(self, kind):
        return [r for r in self.requests() if r["path"] == "/oauth/token" and (r["body"] or {}).get("grant_type") == kind]

    def end_chain(self):
        self.engine.exec(self.container, "touch", "/tmp/end-chain")


UPSTREAM = {"upstream": {"ca": "./stub-ca.pem", "private_hosts": [IDP, API]}}
SERVICE = {"hosts": [API], "paths": ["/backend-api/"], "credential": CREDENTIAL}


def start_stub(engine, p, image):
    """Make the certificates in the project's directory (call before `up`: the file names the CA) and return a function that starts the
    stub once the project is up, and returns it."""
    if subprocess.run(["which", "openssl"], capture_output=True).returncode != 0:
        pytest.fail("these specs make the stub's certificates with openssl, which is not on PATH", pytrace=False)
    make_certificates(p.root)

    def start():
        return run_stub(engine, p, image)
    return start


def run_stub(engine, p, image):
    name = f"{p.name}-oauth-stub"
    started = engine.run("run", "-d", "--name", name, "--network", f"{p.name}_egress", "--network-alias", IDP, "--network-alias", API,
                         image, "sleep", "infinity")
    assert started.returncode == 0, started.stderr
    for source in ("stub.crt", "stub.key"):
        engine.put_file(name, p.root / source, f"/tmp/{source}")
    engine.put_file(name, Path(__file__).parent / "fixtures" / "oauth_stub.py", "/tmp/oauth_stub.py")
    engine.exec(name, "python3", "/tmp/oauth_stub.py", "/tmp/stub.crt", "/tmp/stub.key", STUB_LOG, str(EXPIRES_IN), detach=True)
    return Stub(engine, name)


def stop_stub(engine, p):
    engine.run("rm", "-f", f"{p.name}-oauth-stub")




def sign_in(engine, project, name):
    """What a client does at the end of a device-code sign-in: exchange the code at the token URL. The client's answer: stand-ins."""
    container = engine.instance(project.name, name).name
    result = engine.exec(container, "curl", "-sS", "-m", "20", "-X", "POST", f"https://{IDP}/oauth/token",
                         "-d", "grant_type=authorization_code", "-d", "code=spec-code", "-d", "client_id=app_spec",
                         "-d", f"redirect_uri=https://{IDP}/deviceauth/callback")
    assert result.returncode == 0, result.stderr
    return json.loads(result.stdout)
