// A minimal MCP client over stdio, for the browser specs. Run inside an agent container:
//
//   node mcp_client.mjs '<server command as a JSON array>' '<calls as a JSON array>'
//
// A call is {"name": "browser_navigate", "arguments": {...}}. It starts the server the way a harness would, runs
// the calls in order, and prints one JSON array: each item is {"text": ..., "isError": ...} (or {"error": ...}),
// and with no calls, the names of the tools the server offers. It exits non-zero if the server never answers.
import { spawn } from 'node:child_process';

const [command, calls] = process.argv.slice(2).map((text) => JSON.parse(text));
const server = spawn(command[0], command.slice(1), { stdio: ['pipe', 'pipe', 'inherit'] });
const pending = new Map();
let buffer = '';
let next = 0;

server.stdout.on('data', (chunk) => {
  buffer += chunk;
  for (let end; (end = buffer.indexOf('\n')) >= 0; ) {
    const line = buffer.slice(0, end);
    buffer = buffer.slice(end + 1);
    try {
      const message = JSON.parse(line);
      pending.get(message.id)?.(message);
    } catch {}
  }
});

function request(method, params) {
  const id = ++next;
  server.stdin.write(JSON.stringify({ jsonrpc: '2.0', id, method, params }) + '\n');
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`no answer to ${method}`)), 120000);
    pending.set(id, (message) => {
      clearTimeout(timer);
      resolve(message);
    });
  });
}

try {
  await request('initialize', { protocolVersion: '2024-11-05', capabilities: {}, clientInfo: { name: 'egzo-spec', version: '1' } });
  server.stdin.write(JSON.stringify({ jsonrpc: '2.0', method: 'notifications/initialized' }) + '\n');
  const results = [];
  if (calls.length === 0) {
    const listed = await request('tools/list', {});
    results.push(...listed.result.tools.map((tool) => tool.name));
  }
  for (const call of calls) {
    const answer = await request('tools/call', call);
    if (answer.error) {
      results.push({ error: answer.error.message });
    } else {
      const text = (answer.result.content || []).map((part) => part.text || '').join('\n');
      results.push({ text, isError: Boolean(answer.result.isError) });
    }
  }
  console.log(JSON.stringify(results));
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
} finally {
  server.kill();
}
