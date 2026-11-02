# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) Neopeak Internet Solutions inc.

"""A stand-in for the model APIs, as api.anthropic.com (POST /v1/messages) and api.openai.com (POST /v1/responses). It does not think. With the
file /tmp/browse it asks for the browser, once, to open URL, and after that, or without the file, it answers pong. Every model request is a
JSON line in the log: the path, the key it carried, the names of the tools it offered, and what a tool answered.
Usage: model_api_stub.py CERT KEY LOG URL"""

import json
import os
import ssl
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

CERT, KEY, LOG, URL = sys.argv[1:5]
CALL = "call_egzo_spec"


def tool_results(request):
    """What the tools answered in this conversation, in either API's form, as text."""
    found = []
    for item in request.get("input") or []:  # OpenAI
        if isinstance(item, dict) and item.get("type", "").endswith("_call_output"):
            found.append(json.dumps(item.get("output")))
    for message in request.get("messages") or []:  # Anthropic
        for block in message["content"] if isinstance(message.get("content"), list) else []:
            if block.get("type") == "tool_result":
                found.append(json.dumps(block.get("content")))
    return found


def tool_names(request):
    names = []
    # Codex sends its tools as an item of the input, in a namespace
    tools = (request.get("tools") or []) + [t for item in request.get("input") or [] if isinstance(item, dict) for t in item.get("tools") or []]
    for tool in tools:
        names += [inner["name"] for inner in tool["tools"]] if "tools" in tool else [tool.get("name") or tool.get("type")]
    return names


def next_call(request):
    """The tool to call next, or None to answer with text. Claude Code keeps the browser behind ToolSearch and OpenCode behind Code Mode."""
    if not os.path.exists("/tmp/browse"):
        return None
    names, done = tool_names(request), tool_results(request)
    if len(done) >= 2:
        return None
    navigate = next((n for n in names if n.endswith("browser_navigate")), None)
    if navigate:
        return navigate, {"url": URL}
    if "ToolSearch" in names and not done:
        return "ToolSearch", {"query": "select:mcp__playwright__browser_navigate", "max_results": 1}
    if "execute" in names and not done:
        return "execute", {"code": f"return await tools.playwright.browser_navigate({{ url: {json.dumps(URL)} }})"}
    if "exec" in names and not done:
        return "exec", f"text(JSON.stringify(await tools.mcp__playwright__browser_navigate({{ url: {json.dumps(URL)} }})))"
    return None


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def events(self, events):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for name, data in events:
            self.wfile.write(f"event: {name}\ndata: {json.dumps({'type': name, **data})}\n\n".encode())

    def json(self, status, payload):
        data = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def anthropic(self, request, call):
        block = {"type": "tool_use", "id": f"{CALL}_{len(tool_results(request))}", "name": call[0], "input": call[1]} if call else {"type": "text", "text": "pong"}
        stop = "tool_use" if call else "end_turn"
        message = {"id": "msg_egzo_spec", "type": "message", "role": "assistant", "model": request.get("model"), "stop_reason": None,
                   "stop_sequence": None, "usage": {"input_tokens": 1, "output_tokens": 1}}
        if not request.get("stream"):
            return self.json(200, {**message, "content": [block], "stop_reason": stop})
        start = {**block, "input": {}} if call else {"type": "text", "text": ""}
        delta = {"type": "input_json_delta", "partial_json": json.dumps(call[1])} if call else {"type": "text_delta", "text": "pong"}
        self.events([
            ("message_start", {"message": {**message, "content": []}}),
            ("content_block_start", {"index": 0, "content_block": start}),
            ("content_block_delta", {"index": 0, "delta": delta}),
            ("content_block_stop", {"index": 0}),
            ("message_delta", {"delta": {"stop_reason": stop, "stop_sequence": None}, "usage": {"output_tokens": 1}}),
            ("message_stop", {}),
        ])

    def openai(self, request, call):
        ids = {"id": "ctc_egzo_spec", "call_id": f"{CALL}_{len(tool_results(request))}"}
        item = ({"type": "custom_tool_call", **ids, "name": call[0], "input": call[1]} if call else
                {"type": "message", "id": "msg_egzo_spec", "role": "assistant", "content": [{"type": "output_text", "text": "pong"}]})
        response = {"id": "resp_egzo_spec", "object": "response", "status": "completed", "output": [item],
                    "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}
        self.events([
            ("response.created", {"response": {**response, "status": "in_progress", "output": []}}),
            ("response.output_item.done", {"output_index": 0, "item": item}),
            ("response.completed", {"response": response}),
        ])

    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers.get("Content-Length") or 0)) or b"{}")
        if self.path.split("?")[0].endswith("/count_tokens"):
            return self.json(200, {"input_tokens": 1})
        api = "anthropic" if self.path.split("?")[0].endswith("/v1/messages") else "openai" if self.path.split("?")[0].endswith("/responses") else None
        call = next_call(request) if api else None
        with open(LOG, "a") as log:
            log.write(json.dumps({"path": self.path, "key": self.headers.get("x-api-key") or self.headers.get("Authorization"),
                                  "tools": tool_names(request), "results": tool_results(request), "called": call and call[0]}) + "\n")
        if api == "anthropic":
            return self.anthropic(request, call)
        if api == "openai":
            return self.openai(request, call)
        return self.json(404, {"error": {"type": "not_found_error", "message": "not part of the stub"}})

    def do_GET(self):
        with open(LOG, "a") as log:
            log.write(json.dumps({"path": self.path, "method": "GET"}) + "\n")
        self.json(404, {"error": {"type": "not_found_error", "message": "not part of the stub"}})


server = ThreadingHTTPServer(("0.0.0.0", 443), Handler)
context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
context.load_cert_chain(CERT, KEY)
server.socket = context.wrap_socket(server.socket, server_side=True)
open(LOG, "a").close()
server.serve_forever()
