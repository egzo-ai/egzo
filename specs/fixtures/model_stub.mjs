// A stand-in for the Anthropic Messages API that does not think: it scripts the turn of a harness's agent loop.
//   node model_stub.mjs <port> <log> <command>
// A request that offers a shell tool (Claude Code's `Bash`, OpenCode's `shell`) is the agent's turn. The stub answers with one
// tool call per step, chosen by how many of its own tool calls the conversation already holds a result for: the shell tool
// running <command>, then the egzo MCP tool list_messages, then a closing text. Any other request (a title, a quota check) gets
// a short text. Every request is logged as one JSON line: the path, the key it carried, the tools it offered (with what each
// says it is and takes, for a failure to show), the step and the results of the stub's own calls.
import fs from "node:fs"
import http from "node:http"

const [port, log, command] = process.argv.slice(2)
const PREFIX = "toolu_egzo_spec_"
const SHELLS = ["bash", "shell"]

function results(messages) {
  const found = []
  for (const message of messages ?? []) {
    if (!Array.isArray(message.content)) continue
    for (const block of message.content) {
      if (block.type !== "tool_result" || !String(block.tool_use_id).startsWith(PREFIX)) continue
      const content = Array.isArray(block.content) ? block.content.map((c) => c.text ?? "").join("") : String(block.content ?? "")
      found.push({ id: block.tool_use_id, content, is_error: Boolean(block.is_error) })
    }
  }
  return found
}

// What the shell tool takes, from its schema: the command under the name it uses, and a description if it has one.
function shellInput(tool) {
  const fields = Object.keys(tool.input_schema?.properties ?? {})
  const input = { [["command", "cmd", "script"].find((f) => fields.includes(f)) ?? "command"]: command }
  if (fields.includes("description")) input.description = "Run the command the spec asked for"
  return input
}

function reply(request) {
  const tools = (request.tools ?? []).map((t) => t.name).filter(Boolean)
  const shell = (request.tools ?? []).find((t) => SHELLS.includes(String(t.name).toLowerCase()))
  if (!shell) return { tools, step: null, block: { type: "text", text: "ok" }, stop: "end_turn" }
  const done = results(request.messages)
  const step = done.length
  if (step === 0) {
    return { tools, step, done, block: { type: "tool_use", id: PREFIX + "0", name: shell.name, input: shellInput(shell) }, stop: "tool_use" }
  }
  if (step === 1) {
    const egzo = tools.find((name) => name.endsWith("list_messages"))
    if (egzo) return { tools, step, done, block: { type: "tool_use", id: PREFIX + "1", name: egzo, input: {} }, stop: "tool_use" }
    return { tools, step, done, block: { type: "text", text: "no egzo list_messages tool was offered" }, stop: "end_turn" }
  }
  return { tools, step, done, block: { type: "text", text: "Done." }, stop: "end_turn" }
}

function message(model, block, stop) {
  return {
    id: "msg_egzo_spec", type: "message", role: "assistant", model, content: [block],
    stop_reason: stop, stop_sequence: null, usage: { input_tokens: 1, output_tokens: 1 },
  }
}

function stream(res, model, block, stop) {
  const send = (event, data) => res.write(`event: ${event}\ndata: ${JSON.stringify({ type: event, ...data })}\n\n`)
  res.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-cache" })
  send("message_start", { message: { ...message(model, block, null), content: [] } })
  if (block.type === "tool_use") {
    send("content_block_start", { index: 0, content_block: { type: "tool_use", id: block.id, name: block.name, input: {} } })
    send("content_block_delta", { index: 0, delta: { type: "input_json_delta", partial_json: JSON.stringify(block.input) } })
  } else {
    send("content_block_start", { index: 0, content_block: { type: "text", text: "" } })
    send("content_block_delta", { index: 0, delta: { type: "text_delta", text: block.text } })
  }
  send("content_block_stop", { index: 0 })
  send("message_delta", { delta: { stop_reason: stop, stop_sequence: null }, usage: { output_tokens: 1 } })
  send("message_stop", {})
  res.end()
}

http
  .createServer((req, res) => {
    let body = ""
    req.on("data", (chunk) => (body += chunk))
    req.on("end", () => {
      const path = req.url.split("?")[0]
      const key = req.headers["x-api-key"] ?? req.headers.authorization ?? null
      let request = {}
      try {
        request = body ? JSON.parse(body) : {}
      } catch {}
      if (req.method !== "POST" || !path.endsWith("/messages") && !path.endsWith("/messages/count_tokens")) {
        fs.appendFileSync(log, JSON.stringify({ path, method: req.method, key }) + "\n")
        res.writeHead(404, { "content-type": "application/json" })
        res.end(JSON.stringify({ type: "error", error: { type: "not_found_error", message: "not part of the stub" } }))
        return
      }
      if (path.endsWith("/count_tokens")) {
        res.writeHead(200, { "content-type": "application/json" })
        res.end(JSON.stringify({ input_tokens: 1 }))
        return
      }
      const { tools, step, done, block, stop } = reply(request)
      const described = step === null ? undefined : (request.tools ?? []).map((t) => ({
        name: t.name, description: String(t.description ?? "").slice(0, 300), takes: Object.keys(t.input_schema?.properties ?? {}),
      }))
      fs.appendFileSync(log, JSON.stringify({ path, key, stream: Boolean(request.stream), tools, step, results: done ?? [], described }) + "\n")
      const model = request.model ?? "claude-egzo-spec"
      if (request.stream) stream(res, model, block, stop)
      else {
        res.writeHead(200, { "content-type": "application/json" })
        res.end(JSON.stringify(message(model, block, stop)))
      }
    })
  })
  .listen(Number(port), "127.0.0.1", () => fs.appendFileSync(log, JSON.stringify({ listening: Number(port) }) + "\n"))
