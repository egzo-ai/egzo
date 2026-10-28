<script lang="ts">
  import { onMount, untrack } from "svelte";
  import { Terminal } from "@xterm/xterm";
  import { FitAddon } from "@xterm/addon-fit";
  import { WebglAddon } from "@xterm/addon-webgl";
  import "@xterm/xterm/css/xterm.css";
  import { terminalURL } from "../lib/api";
  import { operate } from "../lib/agentops";
  import { app, setRenderer, toast } from "../lib/app.svelte";
  import { href, navigate } from "../lib/router.svelte";
  import { copyText, osc52Text } from "../lib/clipboard";
  import { registerSender } from "../lib/registry";
  import { TermSession, type TermState } from "../lib/termsession";
  import { closeTerm, cycleLayout, focusPane, noteOutput, reconnect, setTermState, workspace, type Term } from "../lib/workspace.svelte";
  import { activityKind, activityLabel } from "../lib/format";
  import Icon from "./Icon.svelte";
  import Pill from "./Pill.svelte";

  let { term, slot, compact }: { term: Term; slot: number; compact: boolean } = $props();

  let host: HTMLDivElement;
  let size = $state({ cols: 80, rows: 24 });
  let xterm: Terminal | undefined;
  let fit: FitAddon | undefined;
  let session: TermSession | undefined;
  let gl: WebglAddon | undefined;
  let renderer = $state<"webgl" | "dom">("dom");
  let pendingCopy: string | null = null;

  const visible = $derived(slot >= 0 && workspace.active !== null);
  const focused = $derived(workspace.active === term.id);
  const agent = $derived(app.projects.find((p) => p.engine === term.engine && p.name === term.project)?.agents.find((a) => a.name === term.agent));
  const shown: TermState | "stopped" = $derived(term.state !== "connected" && term.state !== "connecting" && agent && !agent.running ? "stopped" : term.state);

  function cssTheme() {
    const v = (n: string) => getComputedStyle(document.documentElement).getPropertyValue(n).trim();
    return {
      background: v("--term-bg"), foreground: v("--term-fg"), cursor: v("--term-cursor"), cursorAccent: v("--term-cursor-accent"), selectionBackground: v("--term-selection"),
      black: v("--term-black"), red: v("--term-red"), green: v("--term-green"), yellow: v("--term-yellow"), blue: v("--term-blue"), magenta: v("--term-magenta"), cyan: v("--term-cyan"), white: v("--term-white"),
      brightBlack: v("--term-bright-black"), brightRed: v("--term-bright-red"), brightGreen: v("--term-bright-green"), brightYellow: v("--term-bright-yellow"),
      brightBlue: v("--term-bright-blue"), brightMagenta: v("--term-bright-magenta"), brightCyan: v("--term-bright-cyan"), brightWhite: v("--term-bright-white"),
    };
  }

  function refit() {
    if (!xterm || !fit || !visible || host.clientWidth === 0 || host.clientHeight === 0) return;
    try {
      fit.fit();
    } catch {
      /* not laid out yet */
    }
  }

  function flushPendingCopy() {
    if (pendingCopy === null) return;
    const text = pendingCopy;
    pendingCopy = null;
    void copyText(text).then((ok) => { if (ok) toast("Copied"); });
  }

  // The DOM renderer draws box characters from the font, so a line height above 1 breaks the vertical borders of a TUI into dashes.
  // The WebGL renderer draws them to fill the cell. Without WebGL (or after it loses its context) the DOM renderer stays.
  function useRenderer(want: "auto" | "dom") {
    if (!xterm) return;
    if (want === "dom") {
      gl?.dispose();
      gl = undefined;
    } else if (!gl) {
      try {
        const addon = new WebglAddon();
        addon.onContextLoss(() => {
          addon.dispose();
          if (gl === addon) gl = undefined;
          renderer = "dom";
        });
        xterm.loadAddon(addon);
        gl = addon;
      } catch {
        /* no WebGL */
      }
    }
    renderer = gl ? "webgl" : "dom";
  }

  $effect(() => {
    useRenderer(app.renderer);
  });

  onMount(() => {
    const v = (n: string) => getComputedStyle(document.documentElement).getPropertyValue(n).trim();
    xterm = new Terminal({
      cursorBlink: !matchMedia("(prefers-reduced-motion: reduce)").matches,
      fontFamily: v("--font-term"), fontSize: 13, lineHeight: 1.2, scrollback: 3000, theme: cssTheme(),
    });
    fit = new FitAddon();
    xterm.loadAddon(fit);
    xterm.open(host);
    // Firefox opens the inspector on Ctrl+Shift+C before xterm's textarea sees a copy: copy the selection ourselves and cancel the shortcut.
    xterm.attachCustomKeyEventHandler((e) => {
      if (e.type === "keydown" && e.ctrlKey && e.shiftKey && !e.altKey && !e.metaKey && e.code === "KeyC") {
        e.preventDefault();
        const text = xterm?.getSelection();
        if (text) void copyText(text);
        return false;
      }
      return true;
    });
    // A TUI copies a selection with OSC 52. Over plain http the browser only allows the copy inside a user gesture, so a refused one waits for the next.
    xterm.parser.registerOscHandler(52, (data) => {
      const text = osc52Text(data);
      if (text !== null) void copyText(text).then((ok) => { if (ok) toast("Copied"); else pendingCopy = text; });
      return true;
    });
    useRenderer(app.renderer);
    host.addEventListener("pointerup", flushPendingCopy, true);
    host.addEventListener("keydown", flushPendingCopy, true);
    xterm.onData((data) => session?.send(data));
    xterm.onResize((s) => {
      size = { cols: s.cols, rows: s.rows };
      session?.resize(s.cols, s.rows);
    });
    const unregister = registerSender(term.id, (data) => session?.send(data));
    const observer = new ResizeObserver(() => refit());
    observer.observe(host);
    const scheme = matchMedia("(prefers-color-scheme: dark)");
    const retheme = () => queueMicrotask(() => { if (xterm) xterm.options.theme = cssTheme(); });
    scheme.addEventListener("change", retheme);
    return () => {
      scheme.removeEventListener("change", retheme);
      observer.disconnect();
      unregister();
      session?.close();
      xterm?.dispose();
    };
  });

  // (re)connect: on mount and whenever the workspace bumps the epoch
  $effect(() => {
    const epoch = term.epoch;
    const takeover = untrack(() => term.takeover);
    if (!xterm) return;
    void epoch;
    session?.close();
    xterm.reset();
    const url = terminalURL(term.engine, term.project, term.agent, takeover);
    const s = new TermSession(
      url,
      {
        onState: (state, info) => setTermState(term.id, state, info),
        onData: (bytes) => {
          xterm?.write(bytes);
          noteOutput(term.id);
        },
      },
      () => ({ cols: xterm?.cols ?? 80, rows: xterm?.rows ?? 24 }),
    );
    session = s;
    s.start();
  });

  // fit when shown, focus when it is the focused pane
  $effect(() => {
    void visible;
    void workspace.layout;
    void workspace.fullscreen;
    void workspace.rail;
    requestAnimationFrame(refit);
  });
  $effect(() => {
    if (visible && focused && shown === "connected") requestAnimationFrame(() => xterm?.focus());
  });
  $effect(() => {
    void app.theme;
    queueMicrotask(() => {
      if (xterm) xterm.options.theme = cssTheme();
    });
  });

  // the hub starts the agent as a task: the terminal connects again once it has
  async function startAndReconnect() {
    await operate(term.engine, term.project, term.agent, "start", (task) => {
      if (task.state === "done") reconnect(term.id);
    });
  }

  const stateLabel: Record<string, string> = {
    connecting: "connecting", connected: "connected", busy: "in use by someone else", taken: "taken over", disconnected: "disconnected", error: "error", stopped: "agent stopped",
  };
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<section
  class="tpane"
  class:compact
  data-state={shown}
  data-focus={focused ? "" : undefined}
  data-pane={slot}
  hidden={!visible}
  style:grid-column={workspace.layout === 1 ? 1 : (slot % 2) + 1}
  style:grid-row={workspace.layout === 4 ? Math.floor(slot / 2) + 1 : 1}
  aria-label="Terminal of {term.agent}"
  onmousedown={() => focusPane(slot)}
>
  <div class="thead">
    <span class="id" title="{term.engine}:{term.project}/{term.agent}"><b>{term.agent}</b> {term.project}{app.engines.length > 1 ? " · " + term.engine : ""}</span>
    {#if agent}<Pill kind={activityKind(agent.activity, agent.waiting)} label={activityLabel(agent.activity, agent.waiting)} />{/if}
    <span class="grow"></span>
    <span class="tools">
      {#if slot === 0}
        <button class="btn" data-variant="quiet" data-size="sm" data-icon-only aria-label="Layout: {workspace.layout === 1 ? 'single' : workspace.layout === 2 ? 'split' : 'grid'}. Change" title="Layout (Ctrl+] s)" onclick={cycleLayout}>
          <Icon name={workspace.layout === 1 ? "single" : workspace.layout === 2 ? "split" : "grid"} size="sm" />
        </button>
      {/if}
      <button class="btn" data-variant="quiet" data-size="sm" title="Terminal renderer: {renderer === 'webgl' ? 'WebGL' : 'DOM'}{app.renderer === 'dom' ? ' (forced)' : ' (auto)'}. Click to switch" onclick={() => setRenderer(app.renderer === "auto" ? "dom" : "auto")}>{renderer === "webgl" ? "WebGL" : "DOM"}</button>
      <a class="btn" data-variant="quiet" data-size="sm" data-icon-only aria-label="Agent page" title="Agent page" href={href.agent(term.engine, term.project, term.agent)} onclick={() => (workspace.active = null)}><Icon name="file" size="sm" /></a>
      <button class="btn" data-variant="quiet" data-size="sm" data-icon-only aria-label="Full screen" title="Full screen (Ctrl+] f)" onclick={() => (workspace.fullscreen = !workspace.fullscreen)}><Icon name="max" size="sm" /></button>
      <button class="btn" data-variant="quiet" data-size="sm" data-icon-only aria-label="Close terminal" title="Detach and close (Ctrl+] d)" onclick={() => { closeTerm(term.id); toast(`Detached from ${term.agent}. The agent keeps running.`); }}><Icon name="x" size="sm" /></button>
    </span>
  </div>

  <div class="tbody">
    <div class="xt" bind:this={host}></div>
    {#if shown !== "connected"}
      <div class="tstate">
        <div class="tcard" data-tone={shown === "error" ? "error" : shown === "busy" || shown === "taken" ? shown : undefined} role="status">
          {#if shown === "connecting"}
            <h3>Connecting to {term.agent}…</h3>
            <div class="connecting"><span class="now">attaching to the terminal…</span></div>
          {:else if shown === "busy"}
            <h3><Icon name="lock" /> {term.holder || "Someone"} is at this terminal</h3>
            <p>One person holds a terminal at a time. Taking it over ends their session.</p>
            <div class="acts"><button class="btn" data-variant="primary" onclick={() => reconnect(term.id, true)}>Take over</button><button class="btn" onclick={() => closeTerm(term.id)}>Close</button></div>
          {:else if shown === "taken"}
            <h3><Icon name="swap" /> {term.holder || "Someone"} took over this terminal</h3>
            <p>The last screen is kept. The agent keeps running.</p>
            <div class="acts"><button class="btn" data-variant="primary" onclick={() => reconnect(term.id, true)}>Take it back</button><button class="btn" onclick={() => closeTerm(term.id)}>Close</button></div>
          {:else if shown === "disconnected"}
            <h3><Icon name="plug-off" /> Connection lost</h3>
            <p>The connection to the hub ended. The agent keeps running.</p>
            <div class="acts"><button class="btn" data-variant="primary" onclick={() => reconnect(term.id)}>Reconnect</button><button class="btn" onclick={() => closeTerm(term.id)}>Close</button></div>
          {:else if shown === "error"}
            <h3>✕ Could not attach</h3>
            <p>{term.message || "The hub could not open the terminal."}</p>
            <div class="acts"><button class="btn" data-variant="primary" onclick={() => reconnect(term.id)}>Reconnect</button><button class="btn" onclick={() => closeTerm(term.id)}>Close</button></div>
          {:else if shown === "stopped"}
            <h3><span class="dot" data-s="stopped"></span> Agent stopped</h3>
            <p>{term.agent} is not running, so it has no terminal.</p>
            <div class="acts"><button class="btn" data-variant="primary" onclick={startAndReconnect}>Start and reconnect</button><button class="btn" onclick={() => closeTerm(term.id)}>Close</button></div>
          {/if}
        </div>
      </div>
    {/if}
  </div>

  <div class="tfoot">
    <span class="st"><span class="dot" data-s={shown === "connected" ? "running" : shown === "error" ? "error" : shown === "connecting" ? "starting" : "stopped"}></span><span data-testid="terminal-state">{stateLabel[shown]}</span></span>
    {#if term.holder && (shown === "busy" || shown === "taken")}<span>{term.holder}</span>{/if}
    <span class="grow"></span>
    {#if shown === "connected"}<span>{size.cols}×{size.rows}</span>{/if}
    <span class="hide-sm">Ctrl+] then ? for shortcuts</span>
  </div>
</section>

<style>
  .xt { position: absolute; inset: 0; padding: 4px 6px; }
  .xt :global(.xterm) { height: 100%; }
  .tpane[hidden] { display: none; }
  .tpane[data-state]:not([data-state="connected"]) .xt { opacity: 0.28; filter: saturate(0.4); }
  .tpane[data-state="connecting"] .xt { opacity: 0; }
</style>
