import { describe, expect, it } from "vitest";
import { apply, emptyChat, fromSnapshot, messages, newTurnId, running } from "./richchat";

const T = "turn-1";

describe("the Rich UI chat", () => {
  it("follows a turn from its prompt to its answer, through a tool call", () => {
    let chat = { ...emptyChat(), channel: "ahp-chat:/ses_1" };
    const actions = [
      { type: "chat/turnStarted", turnId: T, message: { text: "Read README.md", origin: { kind: "user" } } },
      { type: "chat/reasoning", turnId: T, partId: "r1", content: "Let me " },
      { type: "chat/reasoning", turnId: T, partId: "r1", content: "read it." },
      { type: "chat/toolCallStart", turnId: T, toolCallId: "c1", toolName: "read", displayName: "Read file" },
      { type: "chat/toolCallReady", turnId: T, toolCallId: "c1", invocationMessage: "README.md", confirmed: "not-needed" },
      { type: "chat/toolCallComplete", turnId: T, toolCallId: "c1", result: { success: true, pastTenseMessage: "Read README.md" } },
      { type: "chat/responsePart", turnId: T, part: { kind: "markdown", id: "m1", content: "It says " } },
      { type: "chat/delta", turnId: T, partId: "m1", content: "hello." },
    ];
    for (const a of actions) chat = apply(chat, a);
    expect(running(chat)?.id).toBe(T);
    chat = apply(chat, { type: "chat/turnComplete", turnId: T, duration: 5 });
    const [turn] = chat.turns;
    expect(turn.prompt).toBe("Read README.md");
    expect(turn.state).toBe("complete");
    expect(turn.parts).toEqual([
      { kind: "reasoning", id: "r1", content: "Let me read it." },
      { kind: "tool", tool: { id: "c1", name: "read", title: "Read file: README.md", status: "done", outcome: "Read README.md" } },
      { kind: "markdown", id: "m1", content: "It says hello." },
    ]);
    expect(running(chat)).toBeUndefined();
  });

  it("marks a cancelled turn and its running tools", () => {
    let chat = apply(emptyChat(), { type: "chat/turnStarted", turnId: T, message: { text: "sleep" } });
    chat = apply(chat, { type: "chat/toolCallStart", turnId: T, toolCallId: "c1", toolName: "shell", displayName: "Shell" });
    chat = apply(chat, { type: "chat/turnCancelled", turnId: T });
    expect(chat.turns[0].state).toBe("cancelled");
    expect(chat.turns[0].parts[0]).toMatchObject({ kind: "tool", tool: { status: "cancelled" } });
  });

  it("starts from a snapshot, with the turn that runs last", () => {
    const chat = fromSnapshot("ahp-chat:/ses_1", {
      turns: [{ id: "a", message: { text: "one" }, responseParts: [{ kind: "markdown", id: "m", content: "done" }], state: "complete" }],
      activeTurn: { id: "b", message: { text: "two" }, responseParts: [{ kind: "toolCall", toolCall: { toolCallId: "c", toolName: "read", displayName: "Read", status: "running", invocationMessage: "x" } }] },
    });
    expect(chat.turns.map((t) => [t.id, t.state])).toEqual([["a", "complete"], ["b", "running"]]);
    expect(chat.turns[1].parts[0]).toMatchObject({ kind: "tool", tool: { title: "Read: x", status: "running" } });
  });

  it("puts older turns loaded from the harness's store before the ones it has, once", () => {
    let chat = apply(emptyChat(), { type: "chat/turnStarted", turnId: "live", message: { text: "now" } });
    const older = { type: "chat/turnsLoaded", turns: [{ id: "hist_1", message: { text: "before" }, responseParts: [], state: "complete" }] };
    chat = apply(apply(chat, older), older);
    expect(chat.turns.map((t) => t.id)).toEqual(["hist_1", "live"]);
  });

  it("ignores a turn it does not know and an action it does not show", () => {
    const chat = emptyChat();
    expect(apply(chat, { type: "chat/delta", turnId: "nope", partId: "p", content: "x" })).toEqual(chat);
    expect(apply(chat, { type: "chat/usage", turnId: T })).toBe(chat);
  });

  it("speaks AHP: a prompt is a turn started in the chat's channel, a stop cancels the turn", () => {
    const p = messages.prompt("ahp-chat:/ses_1", "hi", T);
    expect(p.method).toBe("dispatchAction");
    expect(p.params.channel).toBe("ahp-chat:/ses_1");
    expect(p.params.action).toMatchObject({ type: "chat/turnStarted", turnId: T, message: { text: "hi", origin: { kind: "user" } } });
    expect(messages.cancel("c", T).params.action).toEqual({ type: "chat/turnCancelled", turnId: T });
    expect(messages.initialize().params.protocolVersions).toEqual(["1.0.0"]);
  });

  it("shows why a turn failed, as AHP says it: an error part with the harness's message", () => {
    // what opencode-ahp sends when OpenCode's execution fails (ChatErrorAction: the part to append, then the turn is final)
    let chat = apply(emptyChat(), { type: "chat/turnStarted", turnId: T, message: { text: "hi" } });
    chat = apply(chat, { type: "chat/error", turnId: T, duration: 3, part: { kind: "error", error: { errorType: "APIError", message: "the proxy refused api.anthropic.com" } } });
    expect(chat.turns[0].state).toBe("error");
    expect(chat.turns[0].parts).toEqual([{ kind: "error", id: "error", content: "the proxy refused api.anthropic.com" }]);
    // and a finished turn of a snapshot keeps its error part
    const later = fromSnapshot("c", { turns: [{ id: "a", message: { text: "hi" }, state: "error", responseParts: [{ kind: "error", error: { errorType: "APIError", message: "no key" } }] }] });
    expect(later.turns[0].parts).toEqual([{ kind: "error", id: "", content: "no key" }]);
  });

  it("makes a turn id without crypto.randomUUID, which a hub served over plain http does not have", () => {
    const original = crypto.randomUUID;
    try {
      // @ts-expect-error: what a page that is not a secure context sees
      delete Crypto.prototype.randomUUID;
      Object.defineProperty(crypto, "randomUUID", { value: undefined, configurable: true });
      const a = newTurnId(), b = newTurnId();
      expect(a).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
      expect(a).not.toBe(b);
    } finally {
      Object.defineProperty(crypto, "randomUUID", { value: original, configurable: true });
    }
  });
});
