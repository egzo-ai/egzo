import { href } from "./router.svelte";

/** Where the Files section is: a workspace (none: the list of them), the instance whose worktree, a folder ("" or ending with "/"),
 * and a file of that folder shown in the viewer ("" for none). */
export interface FilesPlace {
  workspace: string;
  instance: string;
  dir: string;
  file: string;
}

/** A file's name as the address bar holds it: one part of a path, or "" for none. */
export function cleanFile(name: string): string {
  return name.includes("/") || name === "." || name === ".." ? "" : name;
}

/** A folder as the address bar holds it, cleaned: no empty, "." or ".." part, and a slash at the end unless it is the root. */
export function cleanDir(path: string): string {
  const parts = path.split("/").filter((p) => p !== "" && p !== "." && p !== "..");
  return parts.length ? parts.join("/") + "/" : "";
}

/** Reads where the Files section is from an address of the app (its query: w, i, path). */
export function filesPlace(address: string): FilesPlace {
  const at = address.indexOf("?");
  const query = new URLSearchParams(at < 0 ? "" : address.slice(at));
  const workspace = query.get("w") ?? "";
  return {
    workspace,
    instance: workspace ? query.get("i") ?? "" : "",
    dir: workspace ? cleanDir(query.get("path") ?? "") : "",
    file: workspace ? cleanFile(query.get("file") ?? "") : "",
  };
}

/** The address of a place in the Files section. */
export function filesHref(project: string, workspace = "", instance = "", dir = "", file = ""): string {
  const query = new URLSearchParams();
  if (workspace) query.set("w", workspace);
  if (workspace && instance) query.set("i", instance);
  if (workspace && cleanDir(dir)) query.set("path", cleanDir(dir));
  if (workspace && cleanFile(file)) query.set("file", cleanFile(file));
  const q = query.toString();
  return href.project(project, "files") + (q ? "?" + q : "");
}

/** A size for people: bytes, then KB, MB, GB (powers of 1024), one decimal under ten. */
export function humanSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let n = bytes / 1024;
  let unit = 0;
  while (n >= 1024 && unit < units.length - 1) {
    n /= 1024;
    unit++;
  }
  return `${n < 10 ? n.toFixed(1) : Math.round(n)} ${units[unit]}`;
}

/** The characters that change the direction of text (Unicode's Bidi_Control): text can read otherwise than it is, so the viewer marks
 * each one where it is. */
const bidiControl = /[\u061C\u200E\u200F\u202A-\u202E\u2066-\u2069]/u;

/** A piece of a line in the viewer: text, or one direction control to mark. */
export type Piece = { text: string } | { bidi: string };

/** The lines of a text to show, each in pieces: the direction controls apart, named by their code point (U+202E). A last newline ends the
 * last line, and makes none. */
export function viewLines(text: string): Piece[][] {
  if (text === "") return [];
  const lines = text.split("\n");
  if (lines[lines.length - 1] === "") lines.pop();
  return lines.map((line) => {
    const pieces: Piece[] = [];
    let run = "";
    for (const c of line) {
      if (bidiControl.test(c)) {
        if (run) pieces.push({ text: run });
        run = "";
        pieces.push({ bidi: "U+" + c.codePointAt(0)!.toString(16).toUpperCase().padStart(4, "0") });
      } else {
        run += c;
      }
    }
    if (run || pieces.length === 0) pieces.push({ text: run });
    return pieces;
  });
}
