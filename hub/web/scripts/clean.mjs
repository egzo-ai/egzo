// Empties ../internal/web/dist before a build, except .gitkeep (the directory must exist for go:embed).
import { readdirSync, rmSync, mkdirSync } from "node:fs";
import { join } from "node:path";

const dist = new URL("../../internal/web/dist/", import.meta.url).pathname;
mkdirSync(dist, { recursive: true });
for (const entry of readdirSync(dist)) {
  if (entry !== ".gitkeep") rmSync(join(dist, entry), { recursive: true, force: true });
}
