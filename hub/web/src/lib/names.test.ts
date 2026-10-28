import { describe, expect, it } from "vitest";
import { suggestName } from "./names";

describe("suggestName", () => {
  it("is the template, a word pair and a number, which a name may be", () => {
    for (let i = 0; i < 200; i++) {
      const name = suggestName("coder");
      expect(name).toMatch(/^coder-[a-z]+[0-9]{4}$/);
      expect(name).toMatch(/^[a-z0-9][a-z0-9_-]{0,62}$/);
    }
  });

  it("is not a name an agent has", () => {
    let n = 0;
    const random = () => [0, 0, 0, 0.5, 0.5, 0.5][n++ % 6]!; // the first name drawn is taken, the second is not
    expect(suggestName("coder", [], random)).toBe("coder-amberacorn1000");
    n = 0;
    const second = suggestName("coder", ["coder-amberacorn1000"], random);
    expect(second).not.toBe("coder-amberacorn1000");
    expect(second).toMatch(/^coder-[a-z]+5500$/);
  });

  it("stays within 63 characters for a long template", () => {
    expect(suggestName("t".repeat(63)).length).toBeLessThanOrEqual(63);
  });
});
