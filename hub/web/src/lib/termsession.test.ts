import { describe, expect, it } from "vitest";
import { TermSession, type TermState } from "./termsession";

class FakeSocket {
  static OPEN = 1;
  readyState = 1;
  binaryType = "";
  sent: unknown[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((e: { data: unknown }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  send(d: unknown) { this.sent.push(d); }
  close() { this.readyState = 3; }
}

function setup() {
  (globalThis as unknown as { WebSocket: unknown }).WebSocket = FakeSocket;
  const socket = new FakeSocket();
  const states: [TermState, { holder?: string; message?: string }][] = [];
  const data: string[] = [];
  const session = new TermSession(
    "ws://hub/terminal",
    { onState: (s, i) => states.push([s, i]), onData: (b) => data.push(new TextDecoder().decode(b)) },
    () => ({ cols: 100, rows: 30 }),
    () => socket as unknown as WebSocket,
  );
  session.start();
  return { socket, states, data, session };
}

describe("TermSession", () => {
  it("says its size first, then relays bytes both ways", () => {
    const { socket, states, data, session } = setup();
    expect(states.at(-1)![0]).toBe("connecting");
    socket.onopen!();
    expect(states.at(-1)![0]).toBe("connected");
    expect(JSON.parse(socket.sent[0] as string)).toEqual({ type: "resize", cols: 100, rows: 30 });
    socket.onmessage!({ data: new TextEncoder().encode("hello").buffer });
    expect(data).toEqual(["hello"]);
    session.send("ls\r");
    expect(new TextDecoder().decode(socket.sent[1] as Uint8Array)).toBe("ls\r");
    session.resize(120, 40);
    expect(JSON.parse(socket.sent[2] as string)).toEqual({ type: "resize", cols: 120, rows: 40 });
  });

  it("keeps the reason of a busy terminal after the hub closes the socket", () => {
    const { socket, states } = setup();
    socket.onopen!();
    socket.onmessage!({ data: JSON.stringify({ type: "busy", holder: "root" }) });
    socket.onclose!();
    expect(states.at(-1)).toEqual(["busy", { holder: "root" }]);
  });

  it("reports a takeover and an error, and a plain close as a lost connection", () => {
    const a = setup();
    a.socket.onopen!();
    a.socket.onmessage!({ data: JSON.stringify({ type: "taken", holder: "alice" }) });
    a.socket.onclose!();
    expect(a.states.at(-1)).toEqual(["taken", { holder: "alice" }]);

    const b = setup();
    b.socket.onopen!();
    b.socket.onmessage!({ data: JSON.stringify({ type: "error", message: "engine unreachable" }) });
    b.socket.onclose!();
    expect(b.states.at(-1)).toEqual(["error", { message: "engine unreachable" }]);

    const c = setup();
    c.socket.onopen!();
    c.socket.onclose!();
    expect(c.states.at(-1)![0]).toBe("disconnected");
  });

  it("sends nothing unless connected, and nothing after close", () => {
    const { socket, session } = setup();
    session.send("early");
    expect(socket.sent).toHaveLength(0);
    socket.onopen!();
    session.close();
    session.send("late");
    expect(socket.sent).toHaveLength(1);
  });
});
