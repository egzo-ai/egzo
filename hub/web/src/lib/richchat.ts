// The Rich UI chat of an agent spawned in AHP mode: the state of its main session and that session's chat, kept from what the hub relays of
// the agent's AHP server (https://github.com/microsoft/agent-host-protocol). The state is AHP's own, kept by the protocol's reference
// reducers (chatReducer, sessionReducer): a snapshot when a channel is subscribed to, then the actions the server sends, each with its
// serverSeq, which a reconnect resumes from. This is the pure part, tested on its own: ChatView.svelte owns the socket and draws the view
// this file derives from the state (turnsOf, pendingOf, ...).

import { chatReducer, sessionReducer } from "@microsoft/agent-host-protocol";
import type {
  ActionEnvelope,
  ChatAction,
  ChatState,
  ResponsePart,
  SessionAction,
  SessionState,
  Snapshot,
  ToolCallState,
  UsageInfo,
} from "@microsoft/agent-host-protocol";

export const ROOT = "ahp-root://";

export interface Chat {
  /** The main session's channel and its chat's, once egzo said which session is the agent's main one. */
  session: string;
  channel: string;
  /** The chat as the server holds it, from its snapshot on; null until subscribed. */
  state: ChatState | null;
  sessionState: SessionState | null;
  /** The newest serverSeq applied, of any channel: what a reconnect says it saw. */
  seq: number;
  /** Per channel, the serverSeq its snapshot was taken at: an action at or before it is in the snapshot already. */
  from: Record<string, number>;
}

export const emptyChat = (session = "", channel = ""): Chat => ({ session, channel, state: null, sessionState: null, seq: 0, from: {} });

/** The chat with a snapshot of one of its channels, as a subscribe or a reconnect answers it. */
export function withSnapshot(chat: Chat, snapshot: Snapshot): Chat {
  const from = { ...chat.from, [snapshot.resource]: snapshot.fromSeq };
  const seq = Math.max(chat.seq, snapshot.fromSeq);
  if (snapshot.resource === chat.channel) return { ...chat, state: snapshot.state as ChatState, from, seq };
  if (snapshot.resource === chat.session) return { ...chat, sessionState: snapshot.state as SessionState, from, seq };
  return { ...chat, seq };
}

/** The chat after one action the server sent. An action the server rejected changed nothing there, and changes nothing here. */
export function apply(chat: Chat, envelope: ActionEnvelope): Chat {
  if (typeof envelope.serverSeq === "number" && envelope.serverSeq <= (chat.from[envelope.channel] ?? -1)) return chat;
  const seq = typeof envelope.serverSeq === "number" ? Math.max(chat.seq, envelope.serverSeq) : chat.seq;
  if (envelope.rejectionReason !== undefined) return { ...chat, seq };
  if (envelope.channel === chat.channel && chat.state) {
    return { ...chat, seq, state: chatReducer(chat.state, envelope.action as ChatAction) };
  }
  if (envelope.channel === chat.session && chat.sessionState) {
    return { ...chat, seq, sessionState: sessionReducer(chat.sessionState, envelope.action as SessionAction) };
  }
  return { ...chat, seq };
}

// ─── The view ───────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

export interface ToolView {
  id: string;
  name: string;
  /** The tool's name in words ("Shell", "Read file"). */
  display: string;
  /** What it does, in Markdown: the command, the file. */
  said: string;
  status: "preparing" | "waiting" | "running" | "done" | "failed" | "cancelled";
  /** It waits for the person to approve it (or deny it) before it runs. */
  confirm: boolean;
  /** What it says it did, once complete. */
  outcome: string;
  /** A shell command it runs, when it runs one. */
  command: string;
  /** What it was called with, readable: pretty JSON, or the text. */
  input: string;
  /** What it gave back, as text. */
  output: string;
  error: string;
  /** Why it was cancelled, or why it waits. */
  note: string;
}

export type PartView =
  | { kind: "markdown"; key: string; content: string }
  | { kind: "reasoning"; key: string; content: string }
  | { kind: "tool"; key: string; tool: ToolView }
  | { kind: "error"; key: string; message: string }
  | { kind: "notice"; key: string; content: string }
  | { kind: "question"; key: string; request: QuestionRequest; open: boolean; response?: string }
  | { kind: "resource"; key: string; title: string };

export interface TurnView {
  id: string;
  /** What started it, as the user, egzo or another agent typed it. */
  prompt: string;
  /** Who: "user", "agent", "tool", "automation", "systemNotification". */
  origin: string;
  parts: PartView[];
  state: "running" | "complete" | "cancelled" | "error";
  usage?: UsageInfo;
  /** How long it took, in milliseconds, once it ended. */
  duration?: number;
  startedAt?: string;
}

/** What the agent asks the person (an AHP chat input request: OpenCode's question tool, a consent it needs, …). */
export interface QuestionRequest {
  id: string;
  message: string;
  /** A page to open, for a question answered elsewhere (a sign-in). */
  url: string;
  questions: QuestionView[];
  /** The answers given, by question id (AHP ChatInputAnswer). */
  answers: Record<string, Json>;
}

export interface QuestionView {
  id: string;
  kind: "text" | "number" | "integer" | "boolean" | "single-select" | "multi-select";
  title: string;
  message: string;
  required: boolean;
  options: { id: string; label: string; description: string; recommended: boolean }[];
  /** Free text besides (or instead of) the options. */
  freeform: boolean;
  defaultValue?: string | number | boolean;
}

/** A steering message the agent took while a turn ran: shown in the turn after `at` of its parts. */
export interface SteerView {
  key: string;
  at: number;
  text: string;
}

export interface PendingView {
  id: string;
  kind: "steering" | "queued";
  text: string;
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type Json = any;

/** A StringOrMarkdown as text. */
function text(value: Json): string {
  if (typeof value === "string") return value;
  if (value && typeof value.markdown === "string") return value.markdown;
  if (value && typeof value.value === "string") return value.value;
  return "";
}

function pretty(input: Json): { command: string; input: string } {
  if (typeof input !== "string") return { command: "", input: input?.uri ? String(input.uri) : "" };
  try {
    const parsed = JSON.parse(input);
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      const command = typeof parsed.command === "string" ? parsed.command : typeof parsed.cmd === "string" ? parsed.cmd : "";
      return { command, input: JSON.stringify(parsed, null, 2) };
    }
  } catch {
    /* not JSON: as it is */
  }
  return { command: "", input };
}

function contentText(content: Json): string {
  if (!Array.isArray(content)) return "";
  return content
    .map((c: Json) => {
      switch (c?.type) {
        case "text":
          return String(c.text ?? "");
        case "embeddedResource":
          return `[${c.contentType ?? "data"}]`;
        case "resource":
          return `[${c.title ?? c.uri ?? "resource"}]`;
        case "fileEdit":
          return `[edited ${c.after?.uri ?? c.before?.uri ?? "a file"}]`;
        case "terminal":
          return `[terminal: ${c.title ?? ""}]`;
        case "subagent":
          return `[subagent: ${c.title ?? ""}]`;
      }
      return "";
    })
    .filter(Boolean)
    .join("\n");
}

const toolStatus: Record<string, ToolView["status"]> = {
  streaming: "preparing",
  "pending-confirmation": "waiting",
  "auth-required": "waiting",
  "pending-result-confirmation": "waiting",
  running: "running",
  completed: "done",
  cancelled: "cancelled",
};

export function toolOf(tc: ToolCallState): ToolView {
  const t = tc as Json;
  const status = String(t.status);
  const { command, input } = pretty(t.toolInput ?? t.partialInput);
  let view = toolStatus[status] ?? "running";
  if (view === "done" && t.success === false) view = "failed";
  let note = "";
  if (status === "pending-confirmation") note = text(t.confirmationTitle) || "It waits for your approval.";
  else if (status === "auth-required") note = "It waits for a sign-in.";
  else if (status === "pending-result-confirmation") note = "Its result waits for an approval in the agent's terminal.";
  else if (status === "cancelled") note = text(t.reasonMessage) || ({ denied: "Denied.", skipped: "Skipped: the turn ended.", "result-denied": "Its result was denied." } as Record<string, string>)[t.reason] || "";
  return {
    id: String(t.toolCallId ?? ""),
    name: String(t.toolName ?? ""),
    display: String(t.displayName || t.toolName || "Tool"),
    said: text(t.invocationMessage),
    status: view,
    confirm: status === "pending-confirmation",
    outcome: view === "done" || view === "failed" ? text(t.pastTenseMessage) : "",
    command,
    input,
    output: contentText(t.content),
    error: t.error?.message ? String(t.error.message) : "",
    note,
  };
}

function questionOf(request: Json): QuestionRequest {
  const kinds = ["text", "number", "integer", "boolean", "single-select", "multi-select"];
  return {
    id: String(request?.id ?? ""),
    message: text(request?.message),
    url: typeof request?.url === "string" ? request.url : "",
    questions: (Array.isArray(request?.questions) ? request.questions : [])
      .filter((q: Json) => kinds.includes(String(q?.kind)))
      .map((q: Json) => ({
        id: String(q.id ?? ""),
        kind: String(q.kind) as QuestionView["kind"],
        title: text(q.title),
        message: text(q.message),
        required: q.required === true,
        options: (Array.isArray(q.options) ? q.options : []).map((o: Json) => ({
          id: String(o?.id ?? ""),
          label: String(o?.label ?? o?.id ?? ""),
          description: text(o?.description),
          recommended: o?.recommended === true,
        })),
        freeform: q.allowFreeformInput === true,
        ...(["string", "number", "boolean"].includes(typeof q.defaultValue) ? { defaultValue: q.defaultValue } : {}),
      })),
    answers: request?.answers && typeof request.answers === "object" ? request.answers : {},
  };
}

function partsOf(parts: ResponsePart[]): PartView[] {
  const out: PartView[] = [];
  parts.forEach((p, i) => {
    const part = p as Json;
    switch (String(part.kind)) {
      case "markdown":
        if (part.content) out.push({ kind: "markdown", key: `m-${part.id ?? i}`, content: String(part.content) });
        break;
      case "reasoning":
        if (part.content) out.push({ kind: "reasoning", key: `r-${part.id ?? i}`, content: String(part.content) });
        break;
      case "toolCall":
        out.push({ kind: "tool", key: `t-${part.toolCall?.toolCallId ?? i}`, tool: toolOf(part.toolCall) });
        break;
      case "error":
        out.push({ kind: "error", key: `e-${i}`, message: text(part.error?.message) || "The turn failed." });
        break;
      case "systemNotification":
        out.push({ kind: "notice", key: `n-${i}`, content: text(part.content) });
        break;
      case "inputRequest":
        out.push({
          kind: "question",
          key: `q-${part.request?.id ?? i}`,
          request: questionOf(part.request),
          open: part.response === undefined,
          ...(part.response !== undefined ? { response: String(part.response) } : {}),
        });
        break;
      case "contentRef":
        out.push({ kind: "resource", key: `c-${i}`, title: String(part.title ?? part.uri ?? "content") });
        break;
    }
  });
  return out;
}

/** The chat's turns, oldest first, the one that runs last. */
export function turnsOf(state: ChatState | null): TurnView[] {
  if (!state) return [];
  const turns: TurnView[] = state.turns.map((t) => ({
    id: t.id,
    prompt: t.message?.text ?? "",
    origin: String(t.message?.origin?.kind ?? "user"),
    parts: partsOf(t.responseParts ?? []),
    state: String(t.state) as TurnView["state"],
    usage: t.usage,
    duration: t.duration,
    startedAt: t.startedAt,
  }));
  const a = state.activeTurn;
  if (a) {
    turns.push({ id: a.id, prompt: a.message?.text ?? "", origin: String(a.message?.origin?.kind ?? "user"), parts: partsOf(a.responseParts ?? []), state: "running", usage: a.usage, startedAt: a.startedAt });
  }
  return turns;
}

/** The messages sent while a turn ran: one that steers it, and those queued for after it. */
export function pendingOf(state: ChatState | null): PendingView[] {
  if (!state) return [];
  const out: PendingView[] = [];
  const seen = new Set<string>();
  if (state.steeringMessage) {
    out.push({ id: state.steeringMessage.id, kind: "steering", text: state.steeringMessage.message?.text ?? "" });
    seen.add(state.steeringMessage.id);
  }
  for (const m of state.queuedMessages ?? []) {
    if (seen.has(m.id)) continue;
    out.push({ id: m.id, kind: String((m as Json).kind ?? "queued") === "steering" ? "steering" : "queued", text: m.message?.text ?? "" });
  }
  return out;
}

/** The session's title, or the chat's. A harness titles a session from its first prompt, which is often egzo's line announcing a message:
 * a title that names one says nothing, and is none. */
export function titleOf(chat: Chat): string {
  const title = chat.sessionState?.title || chat.state?.title || "";
  return /\bm[0-9a-f]{12,}\b/i.test(title) ? "" : title;
}

/** The turn that runs, if any: the one a stop cancels. */
export function running(chat: Chat): TurnView | undefined {
  const a = chat.state?.activeTurn;
  return a ? turnsOf({ ...chat.state!, turns: [] }).at(-1) : undefined;
}

// ─── egzo's line ─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

/** The messages egzo's line announces, when a prompt is that line ("check egzo message m… and handle the request for me."): their ids.
 * Null for anything else. */
export function announced(prompt: string): string[] | null {
  const line = prompt.trim();
  if (!/^(check )?egzo messages? m[0-9a-f]+/.test(line)) return null;
  const ids: string[] = [];
  for (const sentence of line.matchAll(/(?:^|[.:] )(?:check )?egzo messages? ((?:m[0-9a-f]+)(?:, m[0-9a-f]+)*)/g)) {
    for (const id of sentence[1].split(", ")) if (!ids.includes(id)) ids.push(id);
  }
  return ids.length ? ids : null;
}

// ─── Words ───────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

/** A duration in milliseconds, short: "850ms", "12s", "3m 05s". */
export function formatDuration(ms: number | undefined): string {
  if (ms === undefined || !Number.isFinite(ms) || ms < 0) return "";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${String(s % 60).padStart(2, "0")}s`;
  return `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, "0")}m`;
}

/** A token count, short: "950", "12.3k", "1.2M". */
export function formatTokens(n: number | undefined): string {
  if (n === undefined || !Number.isFinite(n)) return "";
  if (n < 1000) return String(n);
  if (n < 1_000_000) return `${(n / 1000).toFixed(n < 10_000 ? 1 : 0)}k`;
  return `${(n / 1_000_000).toFixed(1)}M`;
}

/** A model reference, without its provider: "opencode-go/glm-5.3-flash" says "glm-5.3-flash". */
export function modelName(model: string | undefined): string {
  if (!model) return "";
  return model.split("/").at(-1) ?? model;
}

// ─── What the view sends ─────────────────────────────────────────────────────────────────────────────────────────────────────────────

let seq = 0;

const dispatch = (channel: string, action: object) => ({ jsonrpc: "2.0", method: "dispatchAction", params: { channel, clientSeq: ++seq, action } });

/** The requests and notifications the view sends, without their ids (ChatView numbers its requests). */
export const messages = {
  initialize: (clientId: string) => ({ method: "initialize", params: { channel: ROOT, clientId, protocolVersions: ["1.0.0"], initialSubscriptions: [ROOT] } }),
  reconnect: (clientId: string, lastSeenServerSeq: number, subscriptions: string[]) => ({
    method: "reconnect",
    params: { channel: ROOT, clientId, lastSeenServerSeq, subscriptions },
  }),
  // listing the sessions first makes the harness's AHP server know a session it did not create, after a restart: then it can be subscribed to
  listSessions: () => ({ method: "listSessions", params: {} }),
  subscribe: (channel: string) => ({ method: "subscribe", params: { channel } }),
  prompt: (channel: string, prompt: string, turnId: string) =>
    dispatch(channel, { type: "chat/turnStarted", turnId, startedAt: new Date().toISOString(), message: { text: prompt, origin: { kind: "user" } } }),
  cancel: (channel: string, turnId: string) => dispatch(channel, { type: "chat/turnCancelled", turnId }),
  /** A message for the turn that runs: steering goes into it at the next step, queued starts a turn after it. */
  pending: (channel: string, kind: "steering" | "queued", id: string, prompt: string) =>
    dispatch(channel, { type: "chat/pendingMessageSet", kind, id, message: { text: prompt, origin: { kind: "user" } } }),
  /** Answers what the agent asks (accept), or dismisses it (decline). */
  answer: (channel: string, requestId: string, response: "accept" | "decline", answers?: Record<string, Json>) =>
    dispatch(channel, { type: "chat/inputCompleted", requestId, response, ...(answers ? { answers } : {}) }),
  /** Lets a tool call that waits for an approval run, or denies it. */
  confirm: (channel: string, turnId: string, toolCallId: string, approved: boolean) =>
    dispatch(channel, { type: "chat/toolCallConfirmed", turnId, toolCallId, approved, ...(approved ? { confirmed: "user-action" } : { reason: "denied" }) }),
};

/** A new id: a random UUID. crypto.randomUUID exists in a secure context only (https, or localhost), and a hub on a private network is
 * often plain http, so it is made from getRandomValues, which every page has. */
export function newTurnId(): string {
  const b = crypto.getRandomValues(new Uint8Array(16));
  b[6] = (b[6] & 0x0f) | 0x40; // version 4
  b[8] = (b[8] & 0x3f) | 0x80; // the RFC 4122 variant
  const h = Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}
