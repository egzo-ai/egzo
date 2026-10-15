# Roadmap: the hub web UI as a Svelte single-page app, on an API-first back-end

**Status.** Built (see "What was built" at the end). A rewrite of the front end and of the layer that serves it. [hub.md](hub.md) describes what
the hub does and is built; this item changes how its UI is built and served, and **adds no hub feature**: no new
operation, permission, data or setting. The new things are in the front end only (layouts, shortcuts, navigation).

The design is in [egzo-design/](egzo-design/README.md): tokens, components, icons, logo, a clickable prototype of every
screen (`egzo-screens.html`) and the design system (`egzo-system.html`).

## What changed

- **Back-end: API first.** `hub/internal/web` stops rendering HTML. It serves JSON under `/api/v1/...` for what the pages do
  today (list projects, spawn, start / stop / remove, timeline, logs, spec, audit, members, engines CRUD and confirm) and the
  terminal websocket. It calls the same `hub.*` methods as now. One error shape (`{code, message}`) with real status codes.
- **Front-end: a Svelte 5 + TypeScript SPA, built with Vite** (no SvelteKit: no server rendering). Plain CSS on the design
  tokens, no UI framework. xterm.js stays the terminal. The build runs in docker (no node on the host) and its output is
  embedded in the hub binary (`go:embed`), so the hub stays one binary and one image.
- **Contract.** An OpenAPI file is the source of the TypeScript types, so the two sides cannot drift.
- **Auth is unchanged.** The OIDC login stays server-side redirects (`/login`, `/auth/callback`) and the session cookie stays.
  The SPA treats a 401 as "go to /login". The same-origin check on state changes stays, and they also require a JSON content
  type. Keep the strict security headers (no inline scripts).
- **Serving.** Every non-API GET returns `index.html`, so deep links work and URLs keep the engine
  (`/e/{engine}/p/{project}/...`). Hashed assets are cached for good. Fonts (JetBrains Mono, Martian Mono, both OFL) are
  vendored, not loaded from Google.
- **Freshness:** the data is fetched on navigation and the pages poll the hub. The hub itself holds what the engines push (its event
  and status streams, see [hub.md](hub.md)), so a poll asks the engine for nothing; the timelines read the sidecar and are polled slowly.
  No push channel to the browser.

## Only what exists

The design is a target, not a list of required data. **Use only what the hub has today.** Where the prototype shows
something the hub does not know (a "since 14:22" on a busy terminal, "took over at 14:41", "exit 0 at 14:44", "Workspace
kept", retry counters beyond what the client itself knows), drop it or show the part that exists. The data that exists
(`engines.Agent`: activity, status, waiting, actor, stale, state; the holder of a terminal; the engine's reachability; the
role) is enough for every screen. Nothing in the back-end changes to feed the design.

One addition is only a different cut of the same data: the sidebar needs, for every project, the question count and, for
every open terminal, the agent's activity and question. Serve that as one summary endpoint and poll it, instead of a request
per terminal.

## Where this plan departs from the design

- **No pop-out to a window.** The prototype has it (`p`, a header button). Remove it: a second window would be a second
  websocket on a terminal that has one holder.
- **Browser-reserved keys.** The design says every key inside a terminal goes to the agent. A web page cannot intercept
  Ctrl+W, Ctrl+T, Ctrl+N and a few more (the browser acts first). Say so in the docs and the keyboard reference: everything
  else goes to the agent, and the Ctrl+] leader works in a plain browser tab.
- Fix the design's README and the prototype text for both when the design folder is next regenerated; until then this page wins.

## What was built

- `hub/internal/web`: the JSON API (`api.go`, documented in `hub/docs/api.md`), the login redirects, the terminal websocket (now at
  `/api/v1/e/.../terminal`), and `appHandler`, which serves the embedded app and falls back to `index.html`. The templates, the
  `?fragment=1` refresh and the form posts are gone. State changes need JSON and the hub's Origin.
- `hub/web`: Svelte 5 + TypeScript + Vite. The sidebar, the pages, the dialogs, and the terminal workspace of the design: terminals
  mounted above the router, one xterm and one websocket each, layouts 1 / 2 / 4, the Ctrl+] leader, the command menu, the rail,
  full screen, the phone navigation, light and dark. Fonts are vendored from `@fontsource`.
- Not built, on purpose: pop-out to a window; the design's invented terminal texts and the prototype's "Retry" and counters that
  the hub has no data for (reconnecting is manual, no retry timer); OpenAPI (the contract is `types.ts` and `api.md`, kept honest by
  the Go API tests and the browser tests).
- Tests: the Go API tests in `hub/internal/web`, Vitest for the terminal session protocol, the workspace, the leader key, the
  router and the API client, and the Playwright suite in `hub/e2e` rewritten for the new markup, with new tests for several
  terminals, the leader key, the command menu and the confirmations.
- Still open: the same person in two windows meets "busy" against themselves and can take it over, with a click. Whether the
  same user re-attaching should count as the same holder (a take-over without a prompt) is undecided.
