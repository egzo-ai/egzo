import { describe, expect, it } from "vitest";
import type { ActionEnvelope, Snapshot } from "@microsoft/agent-host-protocol";
import { announced, announcement, apply, emptyChat, formatDuration, formatTokens, messages, modelName, newTurnId, pendingOf, running, titleOf, turnsOf, withSnapshot, type Chat } from "./richchat";

const S = "ahp-session:/ses_1";
const C = "ahp-chat:/ses_1";
const T = "turn-1";
let n = 0;

// eslint-disable-next-line @typescript-eslint/no-explicit-any
const env = (action: any, extra: Partial<ActionEnvelope> = {}): ActionEnvelope => ({ channel: C, action, serverSeq: ++n, origin: undefined, ...extra });

const chatState = (over: object = {}) => ({ resource: C, title: "the chat", status: 1, modifiedAt: "2026-11-25T00:00:00Z", turns: [], ...over });
const snap = (resource: string, state: object, fromSeq = 0) => ({ resource, state, fromSeq }) as unknown as Snapshot;

function subscribed(over: object = {}): Chat {
  return withSnapshot(emptyChat(S, C), snap(C, chatState(over), n));
}

function play(chat: Chat, actions: object[]): Chat {
  for (const a of actions) chat = apply(chat, env(a));
  return chat;
}

const started = (id = T, text = "Read README.md") => ({ type: "chat/turnStarted", turnId: id, startedAt: "2026-11-25T00:00:00Z", message: { text, origin: { kind: "user" } } });

describe("the Rich UI chat, kept by AHP's reducers", () => {
  it("follows a turn from its prompt to its answer, through a tool call with its input and output", () => {
    let chat = play(subscribed(), [
      started(),
      { type: "chat/responsePart", turnId: T, part: { kind: "reasoning", id: "r1", content: "Let me " } },
      { type: "chat/reasoning", turnId: T, partId: "r1", content: "read it." },
      { type: "chat/toolCallStart", turnId: T, toolCallId: "c1", toolName: "read", displayName: "Read file" },
      { type: "chat/toolCallReady", turnId: T, toolCallId: "c1", invocationMessage: "README.md", toolInput: '{"filePath":"README.md"}', confirmed: "not-needed" },
      { type: "chat/toolCallComplete", turnId: T, toolCallId: "c1", result: { success: true, pastTenseMessage: "Read README.md", content: [{ type: "text", text: "# egzo" }] } },
      { type: "chat/responsePart", turnId: T, part: { kind: "markdown", id: "m1", content: "It says " } },
      { type: "chat/delta", turnId: T, partId: "m1", content: "**hello**." },
      { type: "chat/usage", turnId: T, usage: { inputTokens: 1200, outputTokens: 80, model: "opencode-go/glm-5.3-flash" } },
    ]);
    expect(running(chat)?.id).toBe(T);
    chat = play(chat, [{ type: "chat/turnComplete", turnId: T, duration: 4200 }]);
    const [turn] = turnsOf(chat.state);
    expect(turn).toMatchObject({ id: T, prompt: "Read README.md", origin: "user", state: "complete", duration: 4200, usage: { inputTokens: 1200, model: "opencode-go/glm-5.3-flash" } });
    expect(turn.parts.map((p) => p.kind)).toEqual(["reasoning", "tool", "markdown"]);
    expect(turn.parts[0]).toMatchObject({ content: "Let me read it." });
    expect(turn.parts[1]).toMatchObject({
      tool: { id: "c1", display: "Read file", said: "README.md", status: "done", outcome: "Read README.md", input: '{\n  "filePath": "README.md"\n}', output: "# egzo" },
    });
    expect(turn.parts[2]).toMatchObject({ content: "It says **hello**." });
    expect(running(chat)).toBeUndefined();
  });

  it("shows a shell command, its output as it runs, and a failure", () => {
    const chat = play(subscribed(), [
      started(T, "run the tests"),
      { type: "chat/toolCallStart", turnId: T, toolCallId: "c1", toolName: "shell", displayName: "Shell" },
      { type: "chat/toolCallReady", turnId: T, toolCallId: "c1", invocationMessage: "Run `go test`", toolInput: '{"command":"go test ./..."}', confirmed: "not-needed" },
      { type: "chat/toolCallContentChanged", turnId: T, toolCallId: "c1", content: [{ type: "text", text: "ok  pkg 0.1s" }] },
    ]);
    expect(turnsOf(chat.state)[0].parts[0]).toMatchObject({ tool: { command: "go test ./...", status: "running", output: "ok  pkg 0.1s" } });
    const failed = play(chat, [{ type: "chat/toolCallComplete", turnId: T, toolCallId: "c1", result: { success: false, pastTenseMessage: "Ran `go test`", error: { message: "exit 1" } } }]);
    expect(turnsOf(failed.state)[0].parts[0]).toMatchObject({ tool: { status: "failed", error: "exit 1" } });
  });

  it("ends a cancelled turn and skips its tools that ran", () => {
    const chat = play(subscribed(), [
      started(T, "sleep"),
      { type: "chat/toolCallStart", turnId: T, toolCallId: "c1", toolName: "shell", displayName: "Shell" },
      { type: "chat/turnCancelled", turnId: T, duration: 10 },
    ]);
    const [turn] = turnsOf(chat.state);
    expect(turn.state).toBe("cancelled");
    expect(turn.parts[0]).toMatchObject({ tool: { status: "cancelled", note: "Skipped: the turn ended." } });
  });

  it("ends a turn in error with what went wrong", () => {
    const chat = play(subscribed(), [started(), { type: "chat/error", turnId: T, duration: 1, part: { kind: "error", error: { errorType: "APIError", message: "the model refused" } } }]);
    const [turn] = turnsOf(chat.state);
    expect(turn.state).toBe("error");
    expect(turn.parts.at(-1)).toMatchObject({ kind: "error", message: "the model refused" });
  });

  it("changes nothing for an action the server rejected", () => {
    const chat = play(subscribed(), [started()]);
    const rejected = apply(chat, env(started("t2", "another"), { rejectionReason: "a turn is already active" }));
    expect(turnsOf(rejected.state).map((t) => t.id)).toEqual([T]);
    expect(rejected.seq).toBe(n);
  });

  it("starts from a snapshot, with the turn that runs last, and skips what the snapshot has already", () => {
    let chat = withSnapshot(emptyChat(S, C), snap(C, chatState({
      turns: [{ id: "a", message: { text: "one", origin: { kind: "user" } }, responseParts: [{ kind: "markdown", id: "m", content: "done" }], state: "complete", usage: undefined }],
      activeTurn: { id: "b", startedAt: "2026-11-25T00:00:00Z", message: { text: "two", origin: { kind: "user" } }, responseParts: [{ kind: "markdown", id: "m2", content: "half" }], usage: undefined },
    }), 40));
    expect(turnsOf(chat.state).map((t) => [t.id, t.state])).toEqual([["a", "complete"], ["b", "running"]]);
    expect(chat.seq).toBe(40);
    chat = apply(chat, env({ type: "chat/delta", turnId: "b", partId: "m2", content: " again" }, { serverSeq: 39 }));
    expect(turnsOf(chat.state)[1].parts[0]).toMatchObject({ content: "half" });
    chat = apply(chat, env({ type: "chat/delta", turnId: "b", partId: "m2", content: " more" }, { serverSeq: 41 }));
    expect(turnsOf(chat.state)[1].parts[0]).toMatchObject({ content: "half more" });
    expect(chat.seq).toBe(41);
  });

  it("knows the session's title from its own channel", () => {
    let chat = withSnapshot(subscribed(), snap(S, { resource: S, provider: "opencode", title: "New session", status: 1, createdAt: "", modifiedAt: "", lifecycle: "ready", activeClients: [], chats: [] }, n));
    expect(titleOf(chat)).toBe("New session");
    chat = apply(chat, env({ type: "session/titleChanged", title: "Fix the login page" }, { channel: S }));
    expect(titleOf(chat)).toBe("Fix the login page");
    // titled from egzo's line: no title
    chat = apply(chat, env({ type: "session/titleChanged", title: "Egzo message m9d56df56661977b63d5d26dc4e3a7a19" }, { channel: S }));
    expect(titleOf(chat)).toBe("");
  });

  it("lists the messages that steer the running turn or wait for the next one", () => {
    const chat = play(subscribed(), [
      started(),
      { type: "chat/pendingMessageSet", kind: "steering", id: "p1", message: { text: "use the v2 API", origin: { kind: "user" } } },
      { type: "chat/pendingMessageSet", kind: "queued", id: "p2", message: { text: "then write a test", origin: { kind: "user" } } },
    ]);
    expect(pendingOf(chat.state)).toEqual([
      { id: "p1", kind: "steering", text: "use the v2 API" },
      { id: "p2", kind: "queued", text: "then write a test" },
    ]);
    const next = play(chat, [{ type: "chat/turnComplete", turnId: T, duration: 1 }, { type: "chat/pendingMessageRemoved", kind: "steering", id: "p1" }, { ...started("t2", "then write a test"), queuedMessageId: "p2" }]);
    expect(pendingOf(next.state)).toEqual([]);
    expect(running(next)?.prompt).toBe("then write a test");
  });

  it("shows a tool that waits for an approval, and what the agent asks with its questions, until it is answered", () => {
    const chat = play(subscribed(), [
      started(),
      { type: "chat/toolCallStart", turnId: T, toolCallId: "c1", toolName: "shell", displayName: "Shell" },
      { type: "chat/toolCallReady", turnId: T, toolCallId: "c1", invocationMessage: "Run `rm -rf build`", toolInput: '{"command":"rm -rf build"}' },
      {
        type: "chat/inputRequested",
        turnId: T,
        request: {
          id: "q1",
          message: "Web Search",
          questions: [
            {
              id: "choice",
              kind: "single-select",
              message: "Allow OpenCode to search the web?",
              required: true,
              options: [{ id: "allow", label: "Allow" }, { id: "disable", label: "Disable", recommended: true }],
            },
            { id: "why", kind: "text", message: "Why?", defaultValue: "because" },
            { id: "odd", kind: "slider", message: "unknown kinds are left out" },
          ],
        },
      },
    ]);
    const [turn] = turnsOf(chat.state);
    expect(turn.parts[0]).toMatchObject({ tool: { status: "waiting", confirm: true, note: "It waits for your approval." } });
    expect(turn.parts[1]).toMatchObject({
      kind: "question",
      open: true,
      request: {
        id: "q1",
        message: "Web Search",
        url: "",
        questions: [
          {
            id: "choice",
            kind: "single-select",
            required: true,
            freeform: false,
            options: [
              { id: "allow", label: "Allow", description: "", recommended: false },
              { id: "disable", label: "Disable", description: "", recommended: true },
            ],
          },
          { id: "why", kind: "text", required: false, defaultValue: "because" },
        ],
      },
    });
    const answered = play(chat, [
      { type: "chat/toolCallConfirmed", turnId: T, toolCallId: "c1", approved: true, confirmed: "user-action" },
      { type: "chat/inputCompleted", requestId: "q1", response: "accept", answers: { choice: { state: "submitted", value: { kind: "selected", value: "allow" } } } },
    ]);
    const [after] = turnsOf(answered.state);
    expect(after.parts[0]).toMatchObject({ tool: { status: "running", confirm: false } });
    expect(after.parts[1]).toMatchObject({ kind: "question", open: false, response: "accept", request: { answers: { choice: { value: { value: "allow" } } } } });
  });

  it("ignores the actions of another channel", () => {
    const chat = subscribed();
    expect(turnsOf(apply(chat, env(started(), { channel: "ahp-chat:/ses_other" })).state)).toEqual([]);
  });
});

describe("egzo's line", () => {
  it("names the messages it announces", () => {
    expect(announced("check egzo message m1a2b3c and handle the request for me.")).toEqual(["m1a2b3c"]);
    expect(announced("check egzo messages m01, m02 and handle each one.")).toEqual(["m01", "m02"]);
    expect(announced("egzo message m0f: your network access changed; fetch it. check egzo message m1a and handle the request for me.")).toEqual(["m0f", "m1a"]);
  });

  it("says in a person's words what it announces", () => {
    const id = "m384b9c214b8eda15ae885bd930cce178";
    expect(announcement(`egzo message ${id} from another agent is waiting: fetch it and decide whether it fits your work.`)).toEqual([
      { text: "A message from another agent.", ids: [id] },
    ]);
    expect(
      announcement(
        "egzo message m0f: your network access changed; fetch it. check egzo messages m01, m02 and handle each one. egzo message m3 answers your question on request m9: fetch it.",
      ),
    ).toEqual([
      { text: "Its network access changed.", ids: ["m0f"] },
      { text: "2 messages from people.", ids: ["m01", "m02"] },
      { text: "An answer to its question.", ids: ["m3"] },
    ]);
    expect(announcement("egzo message m1 is the reply to your earlier request: fetch it.")).toEqual([{ text: "A reply to what it asked.", ids: ["m1"] }]);
    expect(announcement("Read README.md")).toBeNull();
  });

  it("keeps the words of a sentence it does not know, with its ids short", () => {
    expect(announcement("egzo message m0123456789abcdef: something new.")).toEqual([{ text: "egzo message m01234567…: something new.", ids: ["m0123456789abcdef"] }]);
  });

  it("is not a prompt someone typed", () => {
    expect(announced("Read README.md")).toBeNull();
    expect(announced("please check egzo message m1 later")).toBeNull();
  });
});

describe("words", () => {
  it("are short", () => {
    expect([formatDuration(850), formatDuration(12_000), formatDuration(185_000), formatDuration(undefined)]).toEqual(["850ms", "12s", "3m 05s", ""]);
    expect([formatTokens(950), formatTokens(12_345), formatTokens(1_234), formatTokens(2_500_000)]).toEqual(["950", "12k", "1.2k", "2.5M"]);
    expect(modelName("opencode-go/glm-5.3-flash")).toBe("glm-5.3-flash");
  });
});

describe("what the view sends", () => {
  it("is AHP's commands and the actions the link lets through", () => {
    expect(messages.reconnect("hub-1", 7, ["ahp-root://", S, C])).toEqual({ method: "reconnect", params: { channel: "ahp-root://", clientId: "hub-1", lastSeenServerSeq: 7, subscriptions: ["ahp-root://", S, C] } });
    expect(messages.prompt(C, "hi", T).params.action).toMatchObject({ type: "chat/turnStarted", turnId: T, message: { text: "hi", origin: { kind: "user" } } });
    expect(messages.pending(C, "steering", "p1", "hi").params.action).toMatchObject({ type: "chat/pendingMessageSet", kind: "steering", id: "p1", message: { text: "hi" } });
    expect(messages.answer(C, "q1", "accept", { choice: { state: "submitted", value: { kind: "selected", value: "allow" } } }).params.action).toEqual({
      type: "chat/inputCompleted",
      requestId: "q1",
      response: "accept",
      answers: { choice: { state: "submitted", value: { kind: "selected", value: "allow" } } },
    });
    expect(messages.answer(C, "q1", "decline").params.action).toEqual({ type: "chat/inputCompleted", requestId: "q1", response: "decline" });
    expect(messages.confirm(C, T, "c1", true).params.action).toEqual({ type: "chat/toolCallConfirmed", turnId: T, toolCallId: "c1", approved: true, confirmed: "user-action" });
    expect(messages.confirm(C, T, "c1", false).params.action).toEqual({ type: "chat/toolCallConfirmed", turnId: T, toolCallId: "c1", approved: false, reason: "denied" });
    const a = messages.cancel(C, T).params.clientSeq;
    expect(messages.cancel(C, T).params.clientSeq).toBe(a + 1);
  });

  it("makes turn ids that are UUIDs, without a secure context", () => {
    expect(newTurnId()).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  });
});
