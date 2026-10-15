import { describe, expect, it } from "vitest";
import { activityKind, activityLabel, ago, initials } from "./format";

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
});
