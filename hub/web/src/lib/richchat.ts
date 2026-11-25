// The Rich UI chat of an agent spawned in AHP mode: the state of its main session's chat, kept from what the hub relays of the agent's
// AHP server (https://github.com/microsoft/agent-host-protocol): a snapshot when the chat is subscribed to, then actions. This is the pure
// part, tested on its own; ChatView.svelte draws it and owns the socket.

export interface ToolCall {
  id: string;
  name: string;
  /** What it does, in words: the tool's display name, then what it was called with. */
  title: string;
  status: "running" | "done" | "failed" | "cancelled";
  /** What it says it did, once complete. */
  outcome: string;
}

export type Part =
  | { kind: "markdown"; id: string; content: string }
  | { kind: "reasoning"; id: string; content: string }
  | { kind: "tool"; tool: ToolCall }
  | { kind: "error"; id: string; content: string };

export interface Turn {
  id: string;
  /** What started it: the prompt, as the user, or egzo's line, typed it. */
  prompt: string;
  parts: Part[];
  state: "running" | "complete" | "cancelled" | "error";
}

export interface Chat {
  turns: Turn[];
  /** The chat's channel, once egzo said which session is the agent's main one. */
  channel: string;
}

export const emptyChat = (): Chat => ({ turns: [], channel: "" });

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type Json = any;

function text(value: Json): string {
  if (typeof value === "string") return value;
  if (value && typeof value.markdown === "string") return value.markdown;
  if (value && typeof value.value === "string") return value.value;
  return "";
}

function toolOf(state: Json): ToolCall {
  const status = state?.status;
  const done = status === "completed";
  return {
    id: String(state?.toolCallId ?? ""),
    name: String(state?.toolName ?? ""),
    title: [state?.displayName || state?.toolName, text(state?.invocationMessage)].filter(Boolean).join(": "),
    status: status === "cancelled" ? "cancelled" : done ? (state?.success === false ? "failed" : "done") : "running",
    outcome: done ? text(state?.pastTenseMessage) : "",
  };
}

function partOf(part: Json): Part | null {
  switch (part?.kind) {
    case "markdown":
      return { kind: "markdown", id: String(part.id), content: String(part.content ?? "") };
    case "reasoning":
      return { kind: "reasoning", id: String(part.id), content: String(part.content ?? "") };
    case "toolCall":
      return { kind: "tool", tool: toolOf(part.toolCall) };
    case "error":
      // AHP's ErrorResponsePart: { kind: "error", error: { errorType, message } }
      return { kind: "error", id: String(part.id ?? ""), content: text(part.error?.message) || text(part.message) || String(part.content ?? "error") };
  }
  return null;
}

function turnOf(turn: Json, state: Turn["state"]): Turn {
  return {
    id: String(turn.id),
    prompt: String(turn.message?.text ?? ""),
    parts: (turn.responseParts ?? []).map(partOf).filter((p: Part | null): p is Part => p !== null),
    state,
  };
}

/** The chat as a subscribe's snapshot says it is. */
export function fromSnapshot(channel: string, state: Json): Chat {
  const turns: Turn[] = (state?.turns ?? []).map((t: Json) => turnOf(t, (t.state as Turn["state"]) ?? "complete"));
  if (state?.activeTurn) turns.push(turnOf(state.activeTurn, "running"));
  return { turns, channel };
}

function withTurn(chat: Chat, id: string, change: (t: Turn) => Turn): Chat {
  return { ...chat, turns: chat.turns.map((t) => (t.id === id ? change(t) : t)) };
}

function withPart(t: Turn, match: (p: Part) => boolean, make: () => Part, change: (p: Part) => Part): Turn {
  const i = t.parts.findIndex(match);
  if (i < 0) return { ...t, parts: [...t.parts, make()] };
  const parts = t.parts.slice();
  parts[i] = change(parts[i]);
  return { ...t, parts };
}

function withTool(t: Turn, id: string, change: (tool: ToolCall) => ToolCall): Turn {
  return withPart(
    t,
    (p) => p.kind === "tool" && p.tool.id === id,
    () => ({ kind: "tool", tool: change({ id, name: "", title: "", status: "running", outcome: "" }) }),
    (p) => (p.kind === "tool" ? { kind: "tool", tool: change(p.tool) } : p),
  );
}

/** The chat after one action of its channel. Actions of other channels, or that the view does not show, change nothing. */
export function apply(chat: Chat, action: Json): Chat {
  const id = String(action?.turnId ?? "");
  switch (action?.type) {
    case "chat/turnStarted":
      if (chat.turns.some((t) => t.id === id)) return chat;
      return { ...chat, turns: [...chat.turns, { id, prompt: String(action.message?.text ?? ""), parts: [], state: "running" }] };
    case "chat/responsePart": {
      const part = partOf(action.part);
      if (!part) return chat;
      return withTurn(chat, id, (t) =>
        part.kind === "tool"
          ? withTool(t, part.tool.id, () => part.tool)
          : withPart(t, (p) => p.kind === part.kind && "id" in p && p.id === part.id, () => part, () => part),
      );
    }
    case "chat/delta":
    case "chat/reasoning": {
      const kind = action.type === "chat/delta" ? "markdown" : "reasoning";
      const partId = String(action.partId ?? "");
      const content = String(action.content ?? "");
      return withTurn(chat, id, (t) =>
        withPart(
          t,
          (p) => p.kind === kind && p.id === partId,
          () => ({ kind, id: partId, content }) as Part,
          (p) => (p.kind === kind ? { ...p, content: p.content + content } : p),
        ),
      );
    }
    case "chat/toolCallStart":
      return withTurn(chat, id, (t) =>
        withTool(t, String(action.toolCallId), (tool) => ({ ...tool, name: String(action.toolName ?? ""), title: String(action.displayName || action.toolName || "") })),
      );
    case "chat/toolCallReady":
      return withTurn(chat, id, (t) =>
        withTool(t, String(action.toolCallId), (tool) => {
          const said = text(action.invocationMessage);
          const base = tool.title.split(": ")[0] || tool.name;
          return { ...tool, title: said ? `${base}: ${said}` : base };
        }),
      );
    case "chat/toolCallComplete":
      return withTurn(chat, id, (t) =>
        withTool(t, String(action.toolCallId), (tool) => ({
          ...tool,
          status: action.result?.success === false ? "failed" : "done",
          outcome: text(action.result?.pastTenseMessage),
        })),
      );
    case "chat/turnsLoaded": {
      // older turns, oldest first, from the harness's own store (after a restart): before the ones the chat has
      const known = new Set(chat.turns.map((t) => t.id));
      const older = (action.turns ?? []).filter((t: Json) => !known.has(String(t.id))).map((t: Json) => turnOf(t, (t.state as Turn["state"]) ?? "complete"));
      return older.length ? { ...chat, turns: [...older, ...chat.turns] } : chat;
    }
    case "chat/turnComplete":
      return withTurn(chat, id, (t) => ({ ...t, state: "complete" }));
    case "chat/turnCancelled":
      return withTurn(chat, id, (t) => ({ ...t, state: "cancelled", parts: t.parts.map((p) => (p.kind === "tool" && p.tool.status === "running" ? { kind: "tool", tool: { ...p.tool, status: "cancelled" } } : p)) }));
    case "chat/error":
      // AHP's ChatErrorAction carries the error part to append (part.error.message), then the turn is final
      return withTurn(chat, id, (t) => ({ ...t, state: "error", parts: [...t.parts, { kind: "error", id: "error", content: text(action.part?.error?.message) || text(action.error?.message) || text(action.message) || "The turn failed." }] }));
  }
  return chat;
}

/** The turn that runs, if any: the one a stop cancels. */
export function running(chat: Chat): Turn | undefined {
  return [...chat.turns].reverse().find((t) => t.state === "running");
}

let seq = 0;

/** The messages the view sends, as JSON-RPC lines. */
export const messages = {
  initialize: () => ({ jsonrpc: "2.0", id: 1, method: "initialize", params: { channel: "ahp-root://", clientId: `egzo-hub-${Math.random().toString(36).slice(2)}`, protocolVersions: ["1.0.0"], initialSubscriptions: ["ahp-root://"] } }),
  // listing the sessions first makes the harness's AHP server know a session it did not create, after a restart: then it can be subscribed to
  listSessions: () => ({ jsonrpc: "2.0", id: 3, method: "listSessions", params: {} }),
  subscribe: (channel: string) => ({ jsonrpc: "2.0", id: 2, method: "subscribe", params: { channel } }),
  prompt: (channel: string, prompt: string, turnId: string) => ({
    jsonrpc: "2.0",
    method: "dispatchAction",
    params: { channel, clientSeq: ++seq, action: { type: "chat/turnStarted", turnId, startedAt: new Date().toISOString(), message: { text: prompt, origin: { kind: "user" } } } },
  }),
  cancel: (channel: string, turnId: string) => ({ jsonrpc: "2.0", method: "dispatchAction", params: { channel, clientSeq: ++seq, action: { type: "chat/turnCancelled", turnId } } }),
};
