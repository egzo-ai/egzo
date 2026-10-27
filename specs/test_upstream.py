# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""`proxy.upstream`: what the proxy trusts and may dial on the way out, for services inside a company network. A CA added to the system
roots for the upstream TLS the proxy terminates, and the exact hosts it may reach at a private address. Reaching such a host still takes
a profile that allows it. The proxy's side is exercised by `proxy/test_proxy_oauth.py`, whose stub servers sit behind both."""

import subprocess

import pytest

from support import spec


@pytest.fixture
def ca_file(project, tmp_path):
    key, cert = tmp_path / "ca.key", project.root / "certs" / "internal-root.pem"
    cert.parent.mkdir()
    subprocess.run(["openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1", "-nodes", "-keyout", key,
                    "-days", "30", "-subj", "/CN=spec internal root", "-out", cert], check=True, capture_output=True)
    return cert


def test_an_upstream_ca_and_private_hosts_are_resolved_project_wide(project, ca_file):
    resolved = project.resolved(spec(proxy={"upstream": {"ca": "./certs/internal-root.pem", "private_hosts": ["IdP.Internal.Example."]}}))
    upstream = resolved["proxy"]["upstream"]
    assert upstream["ca"] == str(ca_file)
    assert upstream["private_hosts"] == ["idp.internal.example"]


@pytest.mark.parametrize("ca, word", [("./missing.pem", "missing.pem"), ("./egzo.yaml", "certificate")])
def test_the_upstream_ca_is_an_existing_file_of_pem_certificates(project, ca, word):
    result = project.config(spec(proxy={"upstream": {"ca": ca}}))
    assert result.returncode != 0
    assert "upstream" in result.stderr and word in result.stderr


def test_the_upstream_ca_holds_no_private_key(project, tmp_path, ca_file):
    key = (tmp_path / "ca.key").read_text()
    ca_file.write_text(ca_file.read_text() + key)
    result = project.config(spec(proxy={"upstream": {"ca": "./certs/internal-root.pem"}}))
    assert result.returncode != 0
    assert "private key" in result.stderr


@pytest.mark.parametrize("host", ["*.internal.example", "*", "10.0.0.5", "idp.internal.example:443", "https://idp.internal.example"])
def test_a_private_host_is_one_exact_host_name(project, host):
    result = project.config(spec(proxy={"upstream": {"private_hosts": [host]}}))
    assert result.returncode != 0
    assert "private_hosts" in result.stderr
