import { describe, expect, it } from "vitest";
import { activityKind, activityLabel, ago, duration, initials, taskEnded, taskFailedLabel } from "./format";

describe("format", () => {
  it("says how long ago", () => {
    const now = Date.parse("2026-10-14T12:00:00Z");
    expect(ago("2026-10-14T11:59:40Z", now)).toBe("just now");
    expect(ago("2026-10-14T11:15:00Z", now)).toBe("45 min ago");
    expect(ago("2026-10-14T07:00:00Z", now)).toBe("5 h ago");
    expect(ago("2026-10-10T12:00:00Z", now)).toBe("4 days ago");
    expect(ago("", now)).toBe("");
  });

  it("makes initials from names and addresses", () => {
    expect(initials("Maria Chen")).toBe("MC");
    expect(initials("root")).toBe("RO");
    expect(initials("alice@example.com")).toBe("AL");
    expect(initials("")).toBe("?");
  });

  it("shows a waiting agent as waiting, whatever its activity", () => {
    expect(activityKind("idle", true)).toBe("waiting");
    expect(activityLabel("idle", true)).toBe("waiting for you");
    expect(activityKind("working", false)).toBe("working");
    expect(activityKind("something-new", false)).toBe("neutral");
    // a running agent whose status cannot be read is just running: never "starting", which it is not known to be
    expect(activityLabel("", false)).toBe("running");
    expect(activityLabel("unknown", false)).toBe("running");
    expect(activityKind("unknown", false)).toBe("running");
    expect(activityLabel("blocked", false)).toBe("blocked");
  });

  it("says how a task ended, for whoever asked", () => {
    expect(taskEnded("rm", "coder-1", "done")).toBe("Removed coder-1");
    expect(taskEnded("spawn", "coder-1", "failed", "no room")).toBe("Could not spawn coder-1: no room");
    expect(taskEnded("stop", "coder-1", "interrupted")).toBe("The hub stopped while stopping coder-1: look at the agent");
    expect(taskFailedLabel("start", "failed")).toBe("start failed");
    expect(taskFailedLabel("start", "interrupted")).toBe("interrupted");
  });

  it("says how long a task took", () => {
    expect(duration("2026-10-14T12:00:00Z", "2026-10-14T12:00:04Z")).toBe("4 s");
    expect(duration("2026-10-14T12:00:00Z", "2026-10-14T12:02:05Z")).toBe("2 min 5 s");
    expect(duration("2026-10-14T12:00:00Z", "2026-10-14T12:03:00Z")).toBe("3 min");
    expect(duration("2026-10-14T12:00:00Z", undefined, Date.parse("2026-10-14T12:00:30Z"))).toBe("30 s");
  });
});
