import { describe, expect, it } from "vitest";
import { bytes, connections, duration, explain, sharedParents } from "./network";
import type { NetworkEvent } from "./types";

describe("the network view", () => {
  it("says how to fix a host that is not in the profile, with the profile's own name", () => {
    const e = explain({ code: "not-in-profile", host: "registry.npmjs.org", profile: "builders", protocol: "https", text: "" });
    expect(e.yaml).toBe("egress:\n  builders:\n    allow: [registry.npmjs.org]");
    expect(e.command).toBe("egzo project up");
    expect(e.why).toContain("builders");
  });

  it("explains every reason code of the proxy, and falls back on the text for one it does not know", () => {
    const codes = ["no-policy", "credentials", "port", "not-in-profile", "private-address", "limit", "client-hello", "sni-mismatch", "host-header",
      "host-mismatch", "placeholder", "dial", "tls-handshake", "upstream", "identity", "channel", "forwarding", "request", "host-key", "key", "auth", "session"];
    for (const code of codes) {
      const e = explain({ code, host: "h.example", profile: "p", protocol: "https", text: "t" });
      expect(e.fix, code).not.toBe("");
      expect(e.title, code).not.toBe(code);
    }
    expect(explain({ code: "new-one", host: "h", profile: "", protocol: "https", text: "what happened" })).toEqual({ title: "new-one", why: "what happened", fix: "" });
  });

  it("finds the parent domains several denied hosts share", () => {
    const shared = sharedParents(["a.cdn.example.com", "b.cdn.example.com", "b.cdn.example.com", "example.org", "10.0.0.1", "x.other.net"]);
    expect([...shared]).toEqual([["cdn.example.com", ["a.cdn.example.com", "b.cdn.example.com"]]]);
  });

  it("makes one row of a connection's events, with its worst decision, its requests and what its close says", () => {
    const at = (s: number) => `2026-10-23T10:00:0${s}Z`;
    const newestFirst: NetworkEvent[] = [
      { time: at(5), conn: "b", protocol: "https", event: "connect", decision: "deny", agent: "coder-1", host: "evil.example", reason: { code: "not-in-profile", text: "no" } },
      { time: at(4), conn: "a", protocol: "https", event: "close", agent: "coder-1", host: "example.com", bytesUp: 10, bytesDown: 2048, durationMs: 1500 },
      { time: at(3), conn: "a", protocol: "https", event: "request", decision: "deny", agent: "coder-1", host: "example.com", method: "POST", path: "/x", reason: { code: "placeholder", text: "too large" } },
      { time: at(2), conn: "a", protocol: "https", event: "request", decision: "allow", agent: "coder-1", host: "example.com", method: "GET", path: "/", status: 200 },
      { time: at(1), conn: "a", protocol: "https", event: "connect", decision: "allow", mode: "inspect", agent: "coder-1", host: "example.com" },
      { time: at(0), conn: "c", protocol: "https", event: "connect", decision: "error", agent: "coder-1", host: "api.example.com", reason: { code: "aborted", text: "left" } },
    ];
    const rows = connections(newestFirst);
    expect(rows.map((r) => r.conn)).toEqual(["b", "a", "c"]);
    const a = rows[1]!;
    expect([a.decision, a.mode, a.requests, a.bytesDown, a.durationMs, a.reason?.code]).toEqual(["deny", "inspect", 2, 2048, 1500, "placeholder"]);
    expect(a.steps.map((s) => s.event)).toEqual(["connect", "request", "request", "close"]);
    expect(rows[2]!.decision).toBe("error");
  });

  it("writes sizes and durations for people", () => {
    expect([bytes(512), bytes(2048), bytes(5 * 1024 * 1024)]).toEqual(["512 B", "2.0 KB", "5.0 MB"]);
    expect([duration(250), duration(1500), duration(75_000), duration(7_260_000)]).toEqual(["250 ms", "1.5 s", "1 min 15 s", "2 h 1 min"]);
  });
});
