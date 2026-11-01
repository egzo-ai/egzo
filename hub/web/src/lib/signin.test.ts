import { describe, expect, it } from "vitest";
import { failureText } from "./signin";

describe("what a failed sign-in says", () => {
  it("has words for the hub's codes", () => {
    expect(failureText("no_invite")).toContain("takes an invite");
    expect(failureText("refused")).toBe("Sign-in refused.");
  });

  it("says nothing when there is no code", () => {
    expect(failureText(null)).toBeNull();
  });

  it("never shows what an address says", () => {
    for (const text of ["Call 555-0100 to keep your account", "constructor", "__proto__", ""]) {
      expect(failureText(text)).toBe("That did not work. Try again.");
    }
  });
});
