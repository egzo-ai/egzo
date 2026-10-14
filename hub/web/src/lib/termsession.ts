// The client side of the terminal websocket: binary frames are the terminal's bytes in both directions; text frames are control
// messages (the size going up; "busy", "taken" and "error" coming down). One session is one websocket; a terminal reconnects by
// making a new one.
import type { ControlFrame } from "./types";

export type TermState = "connecting" | "connected" | "busy" | "taken" | "disconnected" | "error";

export interface SessionEvents {
  onState(state: TermState, info: { holder?: string; message?: string }): void;
  onData(bytes: Uint8Array): void;
}

export type SocketFactory = (url: string) => WebSocket;

export class TermSession {
  private socket: WebSocket | null = null;
  private encoder = new TextEncoder();
  private state: TermState = "connecting";
  private info: { holder?: string; message?: string } = {};

  constructor(private url: string, private events: SessionEvents, private size: () => { cols: number; rows: number }, private open: SocketFactory = (u) => new WebSocket(u)) {}

  start(): void {
    this.set("connecting");
    const ws = this.open(this.url);
    this.socket = ws;
    ws.binaryType = "arraybuffer";
    ws.onopen = () => {
      if (ws !== this.socket) return;
      ws.send(JSON.stringify({ type: "resize", ...this.size() }));
      this.set("connected");
    };
    ws.onmessage = (event) => {
      if (ws !== this.socket) return;
      if (typeof event.data === "string") {
        this.control(event.data);
        return;
      }
      this.events.onData(new Uint8Array(event.data as ArrayBuffer));
    };
    ws.onerror = () => {
      /* a close follows */
    };
    ws.onclose = () => {
      if (ws !== this.socket) return;
      this.socket = null;
      // busy, taken and error already said why; anything else is a connection that ended
      if (this.state === "connected" || this.state === "connecting") this.set("disconnected");
    };
  }

  private control(raw: string): void {
    let frame: ControlFrame;
    try {
      frame = JSON.parse(raw) as ControlFrame;
    } catch {
      return;
    }
    if (frame.type === "busy") this.set("busy", { holder: frame.holder });
    else if (frame.type === "taken") this.set("taken", { holder: frame.holder });
    else if (frame.type === "error") this.set("error", { message: frame.message });
  }

  private set(state: TermState, info: { holder?: string; message?: string } = {}): void {
    this.state = state;
    this.info = info;
    this.events.onState(state, info);
  }

  /** Keystrokes and pasted text, as the terminal produced them. */
  send(data: string | Uint8Array): void {
    if (this.socket?.readyState !== WebSocket.OPEN || this.state !== "connected") return;
    this.socket.send(typeof data === "string" ? this.encoder.encode(data) : data);
  }

  resize(cols: number, rows: number): void {
    if (this.socket?.readyState === WebSocket.OPEN && this.state === "connected") {
      this.socket.send(JSON.stringify({ type: "resize", cols, rows }));
    }
  }

  close(): void {
    const ws = this.socket;
    this.socket = null;
    if (ws) {
      ws.onopen = ws.onmessage = ws.onclose = ws.onerror = null;
      ws.close();
    }
  }

  get current(): { state: TermState; info: { holder?: string; message?: string } } {
    return { state: this.state, info: this.info };
  }
}
