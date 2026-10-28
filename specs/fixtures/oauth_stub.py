# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""A stub OAuth provider and API for the proxy specs, one HTTPS server for every name it is given.

- POST /oauth/token: `authorization_code` with the code `spec-code` starts a chain; `refresh_token` with the chain's current refresh
  token rotates it, any other refresh token is `invalid_grant` (OpenAI's `refresh_token_reused`). Tokens are JWTs whose payload has the
  claims a client reads (Codex decodes them without checking the signature); the refresh token is opaque.
- POST /oauth/revoke: a `token` that is the chain's refresh token ends the chain.
- /backend-api/... (any method), and a websocket upgrade there: answered with the Authorization header it got.
- As Anthropic (under the names platform.claude.com and api.anthropic.com): POST /v1/oauth/token, JSON, the same chain with opaque
  tokens (`sk-ant-oat01-...`, `sk-ant-ort01-...`) and what Claude Code reads besides them (`scope`, the account, the organization);
  GET /api/oauth/profile and /api/oauth/claude_cli/roles answer the chain's access token, and 401 any other.
- Anything else: 404.

Every request is a JSON line in the log (method, path, the Authorization header, the form or JSON body). The file /tmp/end-chain, when it
exists, ends the chain at the next request, as a password change would. Usage: oauth_stub.py CERT KEY LOG EXPIRES_IN"""

import base64
import json
import os
import secrets
import ssl
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs

CERT, KEY, LOG, EXPIRES_IN = sys.argv[1], sys.argv[2], sys.argv[3], int(sys.argv[4])
chain = {"refresh": None, "access": None}


def b64(data):
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()


def jwt(claims):
    header = b64(json.dumps({"alg": "RS256", "typ": "JWT"}).encode())
    return f"{header}.{b64(json.dumps(claims).encode())}.{b64(secrets.token_bytes(64))}"


SCOPE = "user:inference user:profile user:sessions:claude_code user:mcp_servers"


def issue_anthropic():
    chain["access"] = "sk-ant-oat01-stub-" + secrets.token_urlsafe(32)
    chain["refresh"] = "sk-ant-ort01-stub-" + secrets.token_urlsafe(32)
    return {
        "access_token": chain["access"],
        "refresh_token": chain["refresh"],
        "expires_in": EXPIRES_IN,
        "token_type": "Bearer",
        "scope": SCOPE,
        "account": {"uuid": "acct-spec", "email_address": "spec@example.com"},
        "organization": {"uuid": "org-spec", "name": "spec"},
    }


PROFILE = {
    "account": {"uuid": "acct-spec", "email": "spec@example.com", "display_name": "spec"},
    "organization": {"uuid": "org-spec", "name": "spec", "organization_type": "claude_pro", "rate_limit_tier": "default_claude_ai"},
}


def issue():
    now = int(time.time())
    auth = {"chatgpt_plan_type": "pro", "chatgpt_account_id": "acct-spec"}
    chain["access"] = jwt({"sub": "user-spec", "exp": now + EXPIRES_IN, "iat": now, "https://api.openai.com/auth": auth, "n": secrets.token_hex(4)})
    chain["refresh"] = "rt_" + secrets.token_urlsafe(32)
    return {
        "access_token": chain["access"],
        "refresh_token": chain["refresh"],
        "id_token": jwt({"sub": "user-spec", "email": "spec@example.com", "exp": now + EXPIRES_IN, "https://api.openai.com/auth": auth}),
        "expires_in": EXPIRES_IN,
        "token_type": "Bearer",
    }


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def body(self):
        data = self.rfile.read(int(self.headers.get("Content-Length") or 0)).decode()
        if "json" in (self.headers.get("Content-Type") or ""):
            return json.loads(data or "{}")
        return {k: v[0] for k, v in parse_qs(data).items()}

    def record(self, body=None):
        with open(LOG, "a") as log:
            log.write(json.dumps({"method": self.command, "host": self.headers.get("Host"), "path": self.path,
                                  "authorization": self.headers.get("Authorization"), "body": body}) + "\n")

    def answer(self, status, payload):
        data = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def handle_any(self):
        if os.path.exists("/tmp/end-chain"):
            os.remove("/tmp/end-chain")
            chain["refresh"] = chain["access"] = None
        if self.path.startswith("/backend-api/"):
            body = self.rfile.read(int(self.headers.get("Content-Length") or 0)).decode() if self.command != "GET" else None
            self.record(body)
            if self.headers.get("Upgrade", "").lower() == "websocket":
                self.send_response(101)
                self.send_header("Upgrade", "websocket")
                self.send_header("Connection", "Upgrade")
                self.end_headers()
                self.close_connection = True
                return
            return self.answer(200, {"authorization": self.headers.get("Authorization"), "path": self.path})
        if self.path in ("/api/oauth/profile", "/api/oauth/claude_cli/roles"):
            self.record()
            if not chain["access"] or self.headers.get("Authorization") != "Bearer " + chain["access"]:
                return self.answer(401, {"type": "error", "error": {"type": "authentication_error", "message": "invalid token"}})
            if self.path == "/api/oauth/profile":
                return self.answer(200, PROFILE)
            return self.answer(200, {"organization_role": "admin", "workspace_role": None, "organization_name": "spec"})
        if self.command != "POST" or self.path not in ("/oauth/token", "/oauth/revoke", "/v1/oauth/token"):
            self.record()
            return self.answer(404, {"error": "not_found"})
        body = self.body()
        self.record(body)
        if self.path == "/oauth/revoke":
            if body.get("token") and body.get("token") == chain["refresh"]:
                chain["refresh"] = chain["access"] = None
            return self.answer(200, {})
        grant = body.get("grant_type")
        make = issue_anthropic if self.path == "/v1/oauth/token" else issue
        if grant == "authorization_code" and body.get("code") == "spec-code":
            return self.answer(200, make())
        if grant == "refresh_token" and chain["refresh"] and body.get("refresh_token") == chain["refresh"]:
            return self.answer(200, make())
        return self.answer(400, {"error": "invalid_grant", "error_description": "refresh_token_reused"})

    do_GET = do_POST = do_PUT = do_DELETE = handle_any


server = ThreadingHTTPServer(("0.0.0.0", 443), Handler)
context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
context.minimum_version = ssl.TLSVersion.TLSv1_2
context.load_cert_chain(CERT, KEY)
server.socket = context.wrap_socket(server.socket, server_side=True)
open(LOG, "a").close()
server.serve_forever()
