import { describe, expect, it } from "vitest";
import { cleanDir, cleanFile, filesHref, filesPlace, humanSize, viewLines } from "./files";

describe("files", () => {
  it("cleans a folder of the address bar", () => {
    expect(cleanDir("")).toBe("");
    expect(cleanDir("/")).toBe("");
    expect(cleanDir("notes")).toBe("notes/");
    expect(cleanDir("/a//b/./../c/")).toBe("a/b/c/");
  });

  it("reads where it is from the query, and nothing without a workspace", () => {
    expect(filesPlace("/p/p1/files")).toEqual({ workspace: "", instance: "", dir: "", file: "" });
    expect(filesPlace("/p/p1/files?i=x&path=a&file=b")).toEqual({ workspace: "", instance: "", dir: "", file: "" });
    expect(filesPlace("/p/p1/files?w=repo&i=coder-1&path=src%2Fa")).toEqual({ workspace: "repo", instance: "coder-1", dir: "src/a/", file: "" });
    expect(filesPlace("/p/p1/files?w=docs&path=notes%2F&file=plan.md").file).toBe("plan.md");
  });

  it("makes addresses that read back the same", () => {
    expect(filesHref("p1")).toBe("/p/p1/files");
    const address = filesHref("p1", "repo", "coder-1", "src/a b/");
    expect(filesPlace(address)).toEqual({ workspace: "repo", instance: "coder-1", dir: "src/a b/", file: "" });
    expect(filesPlace(filesHref("p1", "docs", "", "notes/", "a b?.md"))).toEqual({ workspace: "docs", instance: "", dir: "notes/", file: "a b?.md" });
  });

  it("says sizes for people", () => {
    expect(humanSize(0)).toBe("0 B");
    expect(humanSize(1023)).toBe("1023 B");
    expect(humanSize(1536)).toBe("1.5 KB");
    expect(humanSize(20 * 1024 * 1024)).toBe("20 MB");
  });

  it("takes a file's name only as one part of a path", () => {
    expect(cleanFile("plan.md")).toBe("plan.md");
    expect(cleanFile("../secret")).toBe("");
    expect(cleanFile("..")).toBe("");
    expect(cleanFile(".")).toBe("");
    expect(filesPlace("/p/p1/files?w=docs&file=..%2Fx").file).toBe("");
  });

  it("splits a text into lines, with the direction controls apart and named", () => {
    expect(viewLines("")).toEqual([]);
    expect(viewLines("a\n\nb\n")).toEqual([[{ text: "a" }], [{ text: "" }], [{ text: "b" }]]);
    expect(viewLines("if \u202e} admin {\u2066x")).toEqual([[{ text: "if " }, { bidi: "U+202E" }, { text: "} admin {" }, { bidi: "U+2066" }, { text: "x" }]]);
    expect(viewLines("<script>alert(1)</script>")).toEqual([[{ text: "<script>alert(1)</script>" }]]);
  });
});
