import { describe, expect, it } from "vitest";
import { cleanDir, filesHref, filesPlace, humanSize } from "./files";

describe("files", () => {
  it("cleans a folder of the address bar", () => {
    expect(cleanDir("")).toBe("");
    expect(cleanDir("/")).toBe("");
    expect(cleanDir("notes")).toBe("notes/");
    expect(cleanDir("/a//b/./../c/")).toBe("a/b/c/");
  });

  it("reads where it is from the query, and nothing without a workspace", () => {
    expect(filesPlace("/p/p1/files")).toEqual({ workspace: "", instance: "", dir: "" });
    expect(filesPlace("/p/p1/files?i=x&path=a")).toEqual({ workspace: "", instance: "", dir: "" });
    expect(filesPlace("/p/p1/files?w=repo&i=coder-1&path=src%2Fa")).toEqual({ workspace: "repo", instance: "coder-1", dir: "src/a/" });
  });

  it("makes addresses that read back the same", () => {
    expect(filesHref("p1")).toBe("/p/p1/files");
    const address = filesHref("p1", "repo", "coder-1", "src/a b/");
    expect(filesPlace(address)).toEqual({ workspace: "repo", instance: "coder-1", dir: "src/a b/" });
  });

  it("says sizes for people", () => {
    expect(humanSize(0)).toBe("0 B");
    expect(humanSize(1023)).toBe("1023 B");
    expect(humanSize(1536)).toBe("1.5 KB");
    expect(humanSize(20 * 1024 * 1024)).toBe("20 MB");
  });
});
