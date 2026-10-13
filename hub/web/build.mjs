// Copies what the browser needs into ../internal/web/dist, which the hub binary embeds. There is no bundler: the
// pages load a handful of plain scripts.
import { cpSync, mkdirSync, rmSync, readdirSync } from "node:fs";
import { join } from "node:path";

const dist = new URL("../internal/web/dist/", import.meta.url).pathname;
for (const entry of readdirSync(dist)) {
  if (entry !== ".gitkeep") rmSync(join(dist, entry), { recursive: true, force: true });
}
mkdirSync(join(dist, "xterm"), { recursive: true });
for (const file of ["app.css", "app.js", "terminal.js"]) cpSync(join("src", file), join(dist, file));
cpSync("node_modules/@xterm/xterm/lib/xterm.js", join(dist, "xterm/xterm.js"));
cpSync("node_modules/@xterm/xterm/css/xterm.css", join(dist, "xterm/xterm.css"));
cpSync("node_modules/@xterm/addon-fit/lib/addon-fit.js", join(dist, "xterm/addon-fit.js"));
console.log("built", dist);
