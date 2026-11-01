import { describe, expect, it } from "vitest";
import { canonical, href, parse } from "./router.svelte";

describe("the router", () => {
  it("knows the app's addresses", () => {
    expect(parse("/")).toEqual({ name: "home" });
    expect(parse("/login")).toEqual({ name: "login" });
    expect(parse("/settings/links")).toEqual({ name: "settings", tab: "links" });
    expect(parse("/settings/invites")).toEqual({ name: "settings", tab: "invites" });
    expect(parse("/settings/users")).toEqual({ name: "settings", tab: "users" });
    expect(parse("/settings/audit")).toEqual({ name: "settings", tab: "audit" });
    expect(parse("/invite")).toEqual({ name: "invite" });
    expect(parse("/settings/account")).toEqual({ name: "settings", tab: "account" });
    expect(parse("/p/p1")).toEqual({ name: "project", project: "p1", tab: "agents" });
    expect(parse("/p/p1/members")).toEqual({ name: "project", project: "p1", tab: "members" });
    expect(parse("/p/p1/a/coder-1")).toEqual({ name: "agent", project: "p1", agent: "coder-1", tab: "overview" });
    expect(parse("/p/p1/a/coder-1/logs")).toEqual({ name: "agent", project: "p1", agent: "coder-1", tab: "logs" });
    expect(parse("/p/p1/network")).toEqual({ name: "project", project: "p1", tab: "network" });
    expect(parse("/p/p1/a/coder-1/network")).toEqual({ name: "agent", project: "p1", agent: "coder-1", tab: "network" });
    expect(href.agent("p1", "coder-1", "network")).toBe("/p/p1/a/coder-1/network");
    expect(parse("/p/p1/activity")).toEqual({ name: "project", project: "p1", tab: "activity" });
    expect(parse("/p/p1/a/coder-1/activity")).toEqual({ name: "agent", project: "p1", agent: "coder-1", tab: "activity" });
  });

  it("answers not found for anything else", () => {
    for (const path of ["/nope", "/p", "/p/p1/bogus", "/p/p1/a", "/p/p1/a/x/terminal", "/link", "/link/QXRB-7KPM", "/link/a/b", "/engines", "/settings/nope", "/settings/users/x"]) {
      expect(parse(path).name).toBe("notfound");
    }
  });

  it("sends settings alone to the first tab", () => {
    const route = parse("/settings");
    expect(route).toEqual({ name: "settings", tab: "links" });
    expect(canonical(route)).toBe("/settings/links");
    expect(canonical(parse("/settings/users"))).toBe("/settings/users");
    expect(canonical(parse("/"))).toBeNull();
    expect(href.links()).toBe("/settings/links");
    expect(href.users()).toBe("/settings/users");
  });

  it("decodes names and builds addresses that parse back", () => {
    const path = href.agent("p1", "fix/bug", "logs");
    expect(path).toBe("/p/p1/a/fix%2Fbug/logs");
    expect(parse(path)).toEqual({ name: "agent", project: "p1", agent: "fix/bug", tab: "logs" });
    expect(parse(href.project("p1", "spec"))).toMatchObject({ tab: "spec" });
    expect(href.project("p1")).toBe("/p/p1");
  });
});
