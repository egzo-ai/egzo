import { describe, expect, it } from "vitest";
import { sourceURL } from "./format";

describe("sourceURL", () => {
  it("points a release at its tag", () => {
    expect(sourceURL("0.1.0")).toBe("https://github.com/egzo-ai/egzo/tree/0.1.0");
    expect(sourceURL("0.2.0-rc1")).toBe("https://github.com/egzo-ai/egzo/tree/0.2.0-rc1");
  });

  it("points anything else at the repo", () => {
    expect(sourceURL("dev")).toBe("https://github.com/egzo-ai/egzo");
    expect(sourceURL("")).toBe("https://github.com/egzo-ai/egzo");
  });
});
