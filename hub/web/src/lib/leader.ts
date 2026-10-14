// The keyboard. Inside a terminal every key belongs to the agent; the hub binds exactly one chord there, Ctrl+] (the leader), then
// one key. The browser keeps a few chords for itself (Ctrl+W, Ctrl+T, Ctrl+N, ...); a page cannot take them, so they close the
// browser tab and never reach the agent.

export type LeaderAction =
  | { type: "go"; n: number }
  | { type: "step"; delta: 1 | -1 }
  | { type: "back" }
  | { type: "sidebar" }
  | { type: "rail" }
  | { type: "layout" }
  | { type: "pane" }
  | { type: "fullscreen" }
  | { type: "detach" }
  | { type: "palette" }
  | { type: "literal" }
  | { type: "help" }
  | { type: "cancel" };

export interface KeyLike {
  key: string;
  code: string;
  ctrlKey: boolean;
  metaKey: boolean;
  altKey: boolean;
  shiftKey: boolean;
}

export const isLeader = (e: KeyLike): boolean => e.ctrlKey && !e.metaKey && !e.altKey && (e.code === "BracketRight" || e.key === "]");

export function actionFor(e: KeyLike): LeaderAction | null {
  if (e.key === "Escape") return { type: "cancel" };
  if (e.ctrlKey || e.metaKey || e.altKey) return null;
  if (/^[1-9]$/.test(e.key)) return { type: "go", n: Number(e.key) };
  switch (e.key) {
    case "ArrowDown": case "ArrowRight": return { type: "step", delta: 1 };
    case "ArrowUp": case "ArrowLeft": return { type: "step", delta: -1 };
    case "h": return { type: "back" };
    case "n": return { type: "sidebar" };
    case "b": return { type: "rail" };
    case "s": return { type: "layout" };
    case "o": return { type: "pane" };
    case "f": return { type: "fullscreen" };
    case "d": return { type: "detach" };
    case ":": return { type: "palette" };
    case "]": return { type: "literal" };
    case "?": return { type: "help" };
  }
  return null;
}

/** The two-step chord: the leader arms it, the next key is consumed (an unknown one cancels). */
export class Leader {
  armed = false;

  /** Returns what the key meant, or null when the key is not the leader's (let it through). `consumed` says to stop the event. */
  handle(e: KeyLike): { consumed: boolean; action?: LeaderAction; armed: boolean } {
    if (!this.armed) {
      if (isLeader(e)) {
        this.armed = true;
        return { consumed: true, armed: true };
      }
      return { consumed: false, armed: false };
    }
    if (["Control", "Shift", "Alt", "Meta"].includes(e.key)) return { consumed: false, armed: true }; // a modifier alone keeps waiting
    this.armed = false;
    return { consumed: true, action: actionFor(e) ?? { type: "cancel" }, armed: false };
  }

  cancel(): void {
    this.armed = false;
  }
}

export const LEADER_KEYS: [string, string][] = [
  ["1–9", "go to terminal n"], ["↑ ↓", "previous / next terminal"], ["h", "back to the last page"], ["n", "focus the sidebar"],
  ["b", "collapse / expand the sidebar"], ["s", "layout: single → split → grid"], ["o", "focus the other pane"], ["f", "full screen on / off"],
  ["d", "detach and close the terminal"], [":", "command menu"], ["]", "send a literal Ctrl+] to the agent"], ["?", "all shortcuts"], ["Esc", "cancel"],
];
