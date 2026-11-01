# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""An agent as the control sidecar sees it, and a team of agents sharing one project.

A spec that talks to the control sidecar does it from inside an agent's container, with that agent's own credentials, the way
a harness's tool calls do. `Agent.call` is one such request, `Agent.calls` several in one `exec` (a flood of fifty requests
costs one docker call, not fifty). `Team` is a project brought up once for a whole module, with agents spawned in it."""

import json
import re
import shlex

from conftest import LABEL_PREFIX
from support import spec, custom

ID = re.compile(r"^m[0-9a-f]{32}$")
END = "@@END@@"


class Agent:
    def __init__(self, engine, project, name):
        self.engine = engine
        self.project = project
        self.name = name
        self._container_name = None

    def __repr__(self):
        return self.name

    @property
    def address(self):
        return f"agent:{self.name}"

    @property
    def container(self):
        return self.engine.instance(self.project.name, self.name)

    def exec(self, script):
        # The container of an instance keeps its name for as long as the instance lives, so it is looked up once: finding it
        # lists and inspects every container of the project.
        if self._container_name is None:
            self._container_name = self.container.name
        return self.engine.exec(self._container_name, "sh", "-c", script)

    def token(self):
        return dict(i.split("=", 1) for i in self.container.raw["Config"]["Env"])["EGZO_TOKEN"]

    def calls(self, *requests):
        """Each request is (verb, path) or (verb, path, body); returns [(status, json or text)], all from one exec."""
        script = []
        for verb, path, *body in requests:
            data = f" -H 'content-type: application/json' -d {shlex.quote(json.dumps(body[0]))}" if body and body[0] is not None else ""
            script.append(
                f'curl -sS -m 10 -w "\\n%{{http_code}}\\n{END}\\n" -u "$EGZO_AGENT:$EGZO_TOKEN" -X {verb} "$EGZO_CONTROL_URL{path}"{data}'
            )
        out = self.exec("\n".join(script)).stdout
        results = []
        for chunk in out.split(f"{END}\n")[:-1]:
            text, _, code = chunk.rstrip("\n").rpartition("\n")
            try:
                results.append((int(code), json.loads(text)))
            except ValueError:
                results.append((int(code or 0), text))
        assert len(results) == len(requests), out
        return results

    def call(self, verb, path, body=None):
        return self.calls((verb, path, body))[0]

    def get(self, message_id):
        """Fetch a message, which must succeed (it marks it fetched)."""
        code, body = self.call("GET", f"/v1/messages/{message_id}")
        assert code == 200, body
        return body

    def send(self, to, text, **fields):
        """`POST /v1/messages`: a new message from this agent. Returns (status, body)."""
        return self.call("POST", "/v1/messages", {"to": to, "text": text, **fields})

    def resolve(self, message_id, text="done", outcome="done"):
        return self.call("POST", f"/v1/messages/{message_id}/resolve", {"text": text, "outcome": outcome})

    def mcp(self, method, params=None):
        """A JSON-RPC call to the control sidecar's MCP endpoint."""
        body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params or {}})
        command = (
            'curl -sS -m 10 -u "$EGZO_AGENT:$EGZO_TOKEN" -X POST "$EGZO_CONTROL_URL/mcp" '
            f"-H 'content-type: application/json' -H 'accept: application/json, text/event-stream' -d {shlex.quote(body)}"
        )
        reply = self.exec(command)
        assert reply.returncode == 0, reply.stderr
        return json.loads(reply.stdout)

    def tool(self, name, /, **arguments):
        return self.mcp("tools/call", {"name": name, "arguments": arguments})["result"]


class Team:
    """One project, up once, with a template `worker` (the custom harness on the spec agent image). Specs that can share
    agents use `coder` and `reviewer`; a spec that counts things (open requests, a rate limit, rows of `ps`) takes fresh
    ones with `pair()` or `spawn()`, so what other specs did cannot show in it."""

    def __init__(self, engine, project, image):
        self.engine = engine
        self.project = project
        project.up(spec(agents={"worker": custom(image)}))
        self.coder = self.spawn("coder")
        self.reviewer = self.spawn("reviewer")

    def spawn(self, name=None, template="worker"):
        return Agent(self.engine, self.project, self.project.spawn(template, name))

    def pair(self):
        return self.spawn(), self.spawn()

    def send(self, to, text, *flags):
        """`egzo message send TO TEXT`, which must succeed; returns the message id it prints."""
        return self.project.ok("message send", *flags, to, text).stdout.split()[1]

    def messages(self, *args):
        """`egzo message ls`, as {id: row}."""
        from support import table

        return {row["ID"]: row for row in table(self.project.ok("message ls", *args).stdout)}

    def events(self, *args):
        output = self.project.run("events", *args).stdout
        return [json.loads(line) for line in output.splitlines() if line.startswith("{")]

    def events_of(self, message_id, *types):
        """The events about one message (its own, and those that refer to it), oldest first."""
        return [e for e in self.events() if (e.get("id") == message_id or e.get("data", {}).get("re") == message_id) and (not types or e["type"] in types)]

    def container(self, service):
        """A sidecar's container (`control`, `proxy`)."""
        return next(c for c in self.engine.containers(self.project.name) if c.labels.get(f"{LABEL_PREFIX}service") == service)
