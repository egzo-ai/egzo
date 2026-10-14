import { describe, expect, it } from "vitest";
import { href, parse } from "./router.svelte";

describe("the router", () => {
  it("knows the app's addresses", () => {
    expect(parse("/")).toEqual({ name: "home" });
    expect(parse("/login")).toEqual({ name: "login" });
    expect(parse("/engines")).toEqual({ name: "engines" });
    expect(parse("/e/lab/p/web")).toEqual({ name: "project", engine: "lab", project: "web", tab: "agents" });
    expect(parse("/e/lab/p/web/members")).toEqual({ name: "project", engine: "lab", project: "web", tab: "members" });
    expect(parse("/e/lab/p/web/a/coder-1")).toEqual({ name: "agent", engine: "lab", project: "web", agent: "coder-1", tab: "overview" });
    expect(parse("/e/lab/p/web/a/coder-1/logs")).toEqual({ name: "agent", engine: "lab", project: "web", agent: "coder-1", tab: "logs" });
  });

  it("answers not found for anything else", () => {
    for (const path of ["/nope", "/e/lab", "/e/lab/p", "/e/lab/p/web/bogus", "/e/lab/p/web/a", "/e/lab/p/web/a/x/terminal", "/engines/x"]) {
      expect(parse(path).name).toBe("notfound");
    }
  });

  it("decodes names and builds addresses that parse back", () => {
    const path = href.agent("my lab", "web app", "fix/bug", "logs");
    expect(path).toBe("/e/my%20lab/p/web%20app/a/fix%2Fbug/logs");
    expect(parse(path)).toEqual({ name: "agent", engine: "my lab", project: "web app", agent: "fix/bug", tab: "logs" });
    expect(parse(href.project("lab", "web", "spec"))).toMatchObject({ tab: "spec" });
    expect(href.project("lab", "web")).toBe("/e/lab/p/web");
  });
});
