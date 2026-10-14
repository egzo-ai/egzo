import { describe, expect, it } from "vitest";
import { Leader, actionFor, isLeader, type KeyLike } from "./leader";

const key = (k: string, extra: Partial<KeyLike> = {}): KeyLike => ({ key: k, code: "", ctrlKey: false, metaKey: false, altKey: false, shiftKey: false, ...extra });
const ctrlBracket = key("]", { ctrlKey: true, code: "BracketRight" });

describe("the leader chord", () => {
  it("is Ctrl+] on any layout, and nothing else", () => {
    expect(isLeader(ctrlBracket)).toBe(true);
    expect(isLeader(key("5", { ctrlKey: true, code: "BracketRight" }))).toBe(true); // a layout where ] needs a modifier
    expect(isLeader(key("]"))).toBe(false);
    expect(isLeader(key("w", { ctrlKey: true, code: "KeyW" }))).toBe(false);
    expect(isLeader(key("]", { ctrlKey: true, altKey: true, code: "BracketRight" }))).toBe(false);
  });

  it("lets every other key through to the agent while idle", () => {
    const leader = new Leader();
    for (const k of [key("c", { ctrlKey: true }), key("r", { ctrlKey: true }), key("k", { ctrlKey: true }), key("Escape"), key("Tab", { shiftKey: true })]) {
      expect(leader.handle(k).consumed).toBe(false);
    }
  });

  it("arms on the leader and runs the next key", () => {
    const leader = new Leader();
    expect(leader.handle(ctrlBracket)).toEqual({ consumed: true, armed: true });
    expect(leader.handle(key("3"))).toEqual({ consumed: true, action: { type: "go", n: 3 }, armed: false });
    expect(leader.armed).toBe(false);
  });

  it("cancels on Escape or on a key it does not know, and ignores a lone modifier", () => {
    const leader = new Leader();
    leader.handle(ctrlBracket);
    expect(leader.handle(key("Control")).armed).toBe(true);
    expect(leader.handle(key("Escape")).action).toEqual({ type: "cancel" });
    leader.handle(ctrlBracket);
    expect(leader.handle(key("z"))).toEqual({ consumed: true, action: { type: "cancel" }, armed: false });
  });

  it("maps the documented keys", () => {
    const cases: [string, unknown][] = [
      ["ArrowDown", { type: "step", delta: 1 }], ["ArrowUp", { type: "step", delta: -1 }], ["h", { type: "back" }], ["n", { type: "sidebar" }],
      ["b", { type: "rail" }], ["s", { type: "layout" }], ["o", { type: "pane" }], ["f", { type: "fullscreen" }], ["d", { type: "detach" }],
      [":", { type: "palette" }], ["]", { type: "literal" }], ["?", { type: "help" }],
    ];
    for (const [k, want] of cases) expect(actionFor(key(k))).toEqual(want);
    expect(actionFor(key("p"))).toBeNull(); // there is no pop-out
  });
});
