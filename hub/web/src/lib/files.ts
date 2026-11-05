import { href } from "./router.svelte";

/** Where the Files section is: a workspace (none: the list of them), the instance whose worktree, and a folder ("" or ending with "/"). */
export interface FilesPlace {
  workspace: string;
  instance: string;
  dir: string;
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
  return { workspace, instance: workspace ? query.get("i") ?? "" : "", dir: workspace ? cleanDir(query.get("path") ?? "") : "" };
}

/** The address of a place in the Files section. */
export function filesHref(project: string, workspace = "", instance = "", dir = ""): string {
  const query = new URLSearchParams();
  if (workspace) query.set("w", workspace);
  if (workspace && instance) query.set("i", instance);
  if (workspace && cleanDir(dir)) query.set("path", cleanDir(dir));
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
