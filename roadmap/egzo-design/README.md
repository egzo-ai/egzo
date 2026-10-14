# egzo hub UI design: handoff for implementation

This folder is the design for the rewrite of the egzo hub web UI (`hub/internal/web`) as a Svelte single-page app that the Go binary embeds and serves. It covers a design system and every screen of the current hub. **It adds no feature**: every screen maps to a route the hub already serves.

## What's in this folder

| File | What it is | How to use it |
|---|---|---|
| `tokens.css` | Every colour, font, size, space, radius, motion and layer value, as CSS custom properties. Dark on `:root`; light via `prefers-color-scheme` and `data-theme="light"` on `<html>`. | Copy as-is into the app. It is the single source of truth: components never use literal colours. |
| `components.css` | Plain-CSS components (buttons, pills, badges, tables, tabs, forms, banners, toasts, dialogs, menus, presence, timeline, empty states). Variants are data-attributes. | Copy as-is, or split per Svelte component. `<Button variant="danger" size="sm">` renders `class="btn" data-variant="danger" data-size="sm"`. |
| `icons.svg` | Icon sprite: 16-unit grid, 1.5 stroke, `currentColor`. | Inline once in the app shell; use `<svg class="icon"><use href="#i-term"/></svg>`. |
| `logo/` | The egzo mark, logo and hub logo, dark and light, plus `favicon.svg` (follows the OS theme). Text is outlined. | Serve the favicon; the app shell inlines the mark using the `--logo-*` tokens. |
| `egzo-screens.html` | Clickable prototype of every screen and terminal state, with example data. Open it in a browser; the **Prototype screens** button (bottom right) jumps to each screen and state. | The visual and behavioural reference. Its inline `<script>` is prototype code, not production code. |
| `egzo-system.html` | The design system reference: rules, specimens, keyboard scheme, xterm theme mapping, responsive and accessibility rules. | Read it before building a component. |
| `src/` | Sources the two HTML pages are built from. | Ignore unless you are changing the pages. |

Published copies (same content): design system https://claude.ai/artifact/6E1H1zDfhoBXyLMpnsnsnw and screens https://claude.ai/artifact/5YBah3ZoHUa96dUbWViS5a.

## Constraints

- Svelte SPA embedded by the Go back-end (`//go:embed all:dist` already exists in `hub/internal/web/server.go`). No UI framework CSS; plain CSS on the tokens.
- Fonts are self-hosted, not loaded from a CDN: **JetBrains Mono** (UI and terminal) and **Martian Mono** (page titles and the wordmark only). Both are OFL.
- Terminal is **xterm.js** (already vendored under `/static/xterm/`), themed from the `--term-*` tokens (snippet below).
- Dark mode first, light mode required. WCAG AA contrast (already verified for every token pair), visible focus everywhere, full keyboard use.
- No scanlines or other CRT effects. The retro feel comes from amber, monospace and a faint `--glow-accent` in dark mode only.

## App shell

```
┌──────────────┬──────────────────────────────────────────────┐
│ sidebar      │ stage                                        │
│ 272px        │  - a page (scrolls), or                      │
│  Go to… [:]  │  - 1, 2 or 4 terminal panes (fill the stage) │
│  TERMINALS   │                                              │
│  PROJECTS    │                                              │
│  engines/me  │                                              │
└──────────────┴──────────────────────────────────────────────┘
```

- **Sidebar** (`--nav-w` 272px, `--nav-w-md` 232px at ≤1024px, `--nav-w-rail` 52px collapsed):
  - **Terminals**: one entry per open terminal, in the order opened (drag to reorder). Three lines: status glyph + agent name + number (1–9); `project · engine` (engine only when the hub has more than one); the agent's status text. A question from the agent turns that line cyan and shows the question. Unread output: bold name + amber dot; a question shows a cyan `?` chip instead. Close (×) detaches; the agent keeps running.
  - **Projects**: "All projects" (home), then every project with its question count and engine badge. The current project unfolds into Agents, Services, Timeline, Spec, Audit log, Members (only the sections the user's role can open).
  - Footer: engines (reachable/total, ⌁ when one is down; operators only get the link), account, theme.
- **Phone (≤720px)**: sidebar hidden; a 44px bar with ☰, the page or terminal name, a `?` when any agent is waiting, and search. ☰ opens the sidebar full screen; choosing anything closes it.
- **Terminals stay mounted.** Each open terminal owns one xterm instance and one WebSocket for its whole life. Switching pages, switching terminals, splitting, collapsing the sidebar or going full screen hides it; only Close destroys it. Persist the open list in `sessionStorage` so a reload reattaches.

Suggested store, above the router:

```ts
type TermState = 'connecting' | 'connected' | 'busy' | 'taken' | 'disconnected' | 'error' | 'stopped';
interface Terminal { id: string /* "lab:payments/fix-auth" */; state: TermState; holder?: string; unread: boolean; ask?: string; }
interface Workspace { terminals: Terminal[]; active: string | null /* null = a page is shown */; layout: 1 | 2 | 4; panes: string[]; focusPane: number; sidebar: 'expanded' | 'rail'; fullscreen: boolean; }
```

## Screens → existing routes

| Screen | Route today | Notes |
|---|---|---|
| Sign-in | `GET /login` → `/login/start` (OIDC) | Lockup, small boot readout, one primary button. Error variant for a failed sign-in. |
| Home: all projects | `GET /` (`?engine=` filter) | Table: project, engine badge, agents bar + "n running of m", questions waiting, your role. Engine filter chips and badges **only when there is more than one engine**. An unreachable engine is a dashed violet banner + a disabled chip; the rest of the page works. Empty state for a user with no role anywhere. |
| Engines (operator) | `GET/POST /engines`, `/engines/confirm`, `/engines/{name}/remove` | Settings-style page: table + add/edit panel. Credentials are write-only ("Stored, date · Replace"). ssh host key confirmation dialog with the SHA256 fingerprint and the `ssh-keygen -lf` command. Forgetting an engine asks you to type its name. |
| Project | `GET /e/{engine}/p/{project}` (+ `/spec`, `/members`, `/audit`) | Header with engine badge, role, Spawn. Section tabs: Agents, Services, Timeline, Spec (read-only resolved YAML), Audit log (administrators), Members (owners). Banner when the project's sidecars are down (spawn disabled). |
| Spawn | `POST …/spawn` | Dialog: template select, optional name. |
| Agent | `GET …/a/{agent}`, `…/logs`, `…/terminal` | Header with Go to terminal / Attach, Stop or Start, Remove. Question banner when waiting. Terminal card (who holds it). Timeline and Logs. Details panel. |
| Remove agent / change role / remove member | `POST …/a/{agent}/rm`, `POST …/members` | Confirmation dialogs that list consequences; focus starts on Cancel. |
| Errors | — | No access yet (home empty state), engine unreachable (agent page), 403, 404. |

The SPA needs JSON for these views; today the routes render HTML (with `?fragment=1` for live refresh). Adding JSON endpoints is implementation work, not a design decision, and is not covered here.

## Terminal panes

Pane = 30px header (`engine:project/agent`, activity pill, layout switch on the first pane, agent page, pop out, full screen, close) + xterm + 22px footer (state, holder, pty size, `Ctrl+] then ?`).

States map to the existing control frames in `hub/internal/web/terminal.go` (`busy`, `taken`, `error`):

| State | Overlay (over the dimmed last frame) | Sidebar glyph |
|---|---|---|
| connecting | checklist: websocket open, agent running, attaching to the pty, blinking caret | blinking amber dot |
| connected | none | activity glyph (● ▶ ○ ?) |
| busy | "Maria Chen is at this terminal. Since 14:22." No watch-only mode. **Take over** / Close | lock |
| taken | "Maria Chen took over at 14:41." Last frame frozen. **Take it back** / Close | swap |
| disconnected | "Connection lost. Retrying in 4s (2 of 5)." **Reconnect now** | unplugged |
| error | the engine's message, e.g. engine unreachable. **Reconnect** | red ✕ |
| stopped | "Agent stopped, exit 0 at 14:44. Workspace kept." **Start and reconnect** | grey ■ |

xterm theme from tokens (re-run on theme change):

```js
const v = (n) => getComputedStyle(document.documentElement).getPropertyValue(n).trim();
term.options.theme = {
  background: v('--term-bg'), foreground: v('--term-fg'), cursor: v('--term-cursor'), cursorAccent: v('--term-cursor-accent'),
  selectionBackground: v('--term-selection'),
  black: v('--term-black'), red: v('--term-red'), green: v('--term-green'), yellow: v('--term-yellow'),
  blue: v('--term-blue'), magenta: v('--term-magenta'), cyan: v('--term-cyan'), white: v('--term-white'),
  brightBlack: v('--term-bright-black'), brightRed: v('--term-bright-red'), brightGreen: v('--term-bright-green'),
  brightYellow: v('--term-bright-yellow'), brightBlue: v('--term-bright-blue'), brightMagenta: v('--term-bright-magenta'),
  brightCyan: v('--term-bright-cyan'), brightWhite: v('--term-bright-white'),
};
term.options.fontFamily = v('--font-term'); term.options.fontSize = 13; term.options.lineHeight = 1.2;
term.options.cursorBlink = !matchMedia('(prefers-reduced-motion: reduce)').matches;
```

## Keyboard

Inside a terminal every key goes to the agent (Ctrl+C, Ctrl+W, Ctrl+R, Ctrl+K, Esc, Alt/Meta, Shift+Tab). The hub binds exactly one chord there: the leader **Ctrl+]** (match `event.code === 'BracketRight'` so it works on every layout), then one key. A small HUD lists the keys while waiting.

| After Ctrl+] | Action |
|---|---|
| 1–9 | go to terminal n |
| ↑ ↓ (also ← →) | previous / next terminal |
| h | back to the last page |
| n | focus the sidebar |
| b | collapse / expand the sidebar |
| s | layout: single → split → grid |
| o | focus the other pane |
| f | full screen on / off |
| p | pop out to a window |
| d | detach and close the terminal |
| : | command menu |
| ] | send a literal Ctrl+] to the agent |
| ? | shortcut reference |
| Esc | cancel (consumed only while the HUD is open) |

On pages (focus not in a field or terminal): `:` or Ctrl+K command menu, `/` filter, `j`/`k` move rows, Enter open, `a` attach, `s` spawn, `g p` / `g e` projects / engines, `?` reference. Single-key shortcuts can be turned off (WCAG 2.1.4). Sidebar lists are one tab stop each: ↑/↓ move, Enter opens, Delete closes a terminal.

## Rules that are easy to miss

- Status is never colour alone: every state has a glyph and a word (see the Status section of `egzo-system.html`). Unreachable uses a dashed outline.
- One primary (amber) action per region. Danger buttons are outlined in rows, solid only inside the confirmation.
- Row actions are always visible, never on hover only.
- Tab switching and terminal focus are instant (no animation). Durations come from `--dur-*`; all become 0 under reduced motion.
- With a single engine, the UI never shows engine badges or filters (URLs still carry the engine).
- Names in copy: "Remove fix-auth?", "Take over", "Detached from fix-auth. The agent keeps running." Errors name the engine, the cause and the next step.
