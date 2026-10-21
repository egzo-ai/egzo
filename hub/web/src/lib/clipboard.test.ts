import { describe, expect, it } from "vitest";
import { osc52Text } from "./clipboard";

describe("osc52Text", () => {
  it("decodes a set", () => expect(osc52Text("c;aGkg4pyT")).toBe("hi ✓"));
  it("ignores a query", () => expect(osc52Text("c;?")).toBeNull());
  it("ignores garbage", () => expect(osc52Text("c;%%%")).toBeNull());
  it("ignores an empty payload", () => expect(osc52Text("c;")).toBeNull());
});
