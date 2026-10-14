import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

// The hub embeds the build output (hub/internal/web/dist). In development, `npm run dev` serves the app and sends the API, the
// terminal websocket and the login to a hub on :8080.
const hub = process.env.EGZO_HUB ?? "http://localhost:8080";

export default defineConfig({
  plugins: [svelte()],
  build: {
    outDir: "../internal/web/dist",
    emptyOutDir: false, // scripts/clean.mjs does it, and keeps .gitkeep
    assetsInlineLimit: 0, // the hub's CSP allows no data: fonts
    sourcemap: false,
    target: "es2022",
  },
  server: {
    proxy: {
      "/api": { target: hub, ws: true, changeOrigin: false },
      "/login/start": hub,
      "/auth": hub,
    },
  },
  test: { environment: "jsdom", include: ["src/**/*.test.ts"] },
});
