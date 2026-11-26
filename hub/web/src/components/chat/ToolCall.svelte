<script lang="ts">
  // One tool call of a turn: a row saying what it does and how it went, which opens on what it was called with and what it gave back.
  import type { ToolView } from "../../lib/richchat";
  import Markdown from "./Markdown.svelte";

  let { tool }: { tool: ToolView } = $props();
  // a long output shows its end: the part that says how it went
  const maxLines = 40;
  let full = $state(false);
  const lines = $derived(tool.output.split("\n"));
  const output = $derived(full || lines.length <= maxLines ? tool.output : lines.slice(-maxLines).join("\n"));
  const said = $derived(tool.said && tool.said !== tool.display ? tool.said : "");
  const label: Record<ToolView["status"], string> = { preparing: "preparing", waiting: "waiting", running: "running", done: "done", failed: "failed", cancelled: "cancelled" };
  const hasDetails = $derived(!!(tool.command || tool.input || tool.output || tool.error || tool.note));
</script>

<details class="tool" data-status={tool.status} data-testid="tool">
  <summary aria-label="{tool.display}{said ? ': ' + said : ''} ({label[tool.status]})">
    <span class="glyph" aria-hidden="true"></span>
    <span class="name">{tool.display}</span>
    {#if said}<span class="said"><Markdown source={said} inline /></span>{/if}
    {#if tool.status === "failed" || tool.status === "waiting" || tool.status === "cancelled"}<span class="tag">{label[tool.status]}</span>{/if}
  </summary>
  <div class="body">
    {#if tool.note}<p class="note">{tool.note}</p>{/if}
    {#if tool.command}
      <div class="block"><div class="label">Command</div><pre class="io" data-testid="tool-command">$ {tool.command}</pre></div>
    {:else if tool.input}
      <div class="block"><div class="label">Input</div><pre class="io" data-testid="tool-input">{tool.input}</pre></div>
    {/if}
    {#if tool.error}
      <div class="block"><div class="label">Error</div><pre class="io err" data-testid="tool-error">{tool.error}</pre></div>
    {/if}
    {#if tool.output}
      <div class="block">
        <div class="label">Output{#if lines.length > maxLines && !full}<span class="dim"> · last {maxLines} of {lines.length} lines</span>{/if}{#if lines.length > maxLines}<button type="button" class="more" onclick={() => (full = !full)}>{full ? "Show the end only" : "Show all"}</button>{/if}</div>
        <pre class="io" data-testid="tool-output">{output}</pre>
      </div>
    {/if}
    {#if !hasDetails}<p class="note">{tool.status === "running" || tool.status === "preparing" ? "Nothing yet." : "No details."}</p>{/if}
  </div>
</details>

<style>
  .tool { border: 0; padding: 0; display: block; font-size: 14px; }
  .tool > summary { list-style: none; display: flex; align-items: center; gap: 10px; padding: 6px 8px; border-radius: 8px; color: var(--text-2); font-size: 14px; min-width: 0; cursor: pointer; }
  .tool > summary::-webkit-details-marker { display: none; }
  .tool > summary:hover { background: var(--surface-2); color: var(--text); }
  .tool[open] > summary { margin-bottom: 0; background: var(--surface-2); }
  .glyph { flex: none; width: 18px; height: 18px; border-radius: 50%; display: grid; place-items: center; font-size: 11px; font-weight: 700; color: var(--status-running); background: var(--status-running-soft); }
  .glyph::before { content: "✓"; }
  .tool[data-status="running"] .glyph, .tool[data-status="preparing"] .glyph { background: transparent; border: 2px solid var(--border-strong); border-top-color: var(--accent); animation: rot 0.9s linear infinite; }
  .tool[data-status="running"] .glyph::before, .tool[data-status="preparing"] .glyph::before { content: ""; }
  .tool[data-status="failed"] .glyph { color: var(--status-error); background: var(--status-error-soft); }
  .tool[data-status="failed"] .glyph::before { content: "✕"; }
  .tool[data-status="cancelled"] .glyph { color: var(--text-3); background: var(--surface-3); }
  .tool[data-status="cancelled"] .glyph::before { content: "–"; }
  .tool[data-status="waiting"] .glyph { color: var(--status-waiting); background: var(--status-waiting-soft); }
  .tool[data-status="waiting"] .glyph::before { content: "?"; }
  .name { flex: none; color: var(--text); font-weight: 500; }
  .said { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-2); }
  .said :global(code) { font-size: 12.5px; }
  .tool[open] .said { white-space: normal; overflow-wrap: anywhere; }
  .tag { flex: none; margin-left: auto; font-size: 12px; color: var(--text-3); }
  .tool[data-status="failed"] .tag { color: var(--status-error); }
  .tool[data-status="waiting"] .tag { color: var(--status-waiting); }
  .body { display: grid; gap: 10px; margin: 8px 8px 12px 36px; min-width: 0; }
  .block { display: grid; gap: 4px; min-width: 0; }
  .label { display: flex; align-items: baseline; gap: 6px; font-size: 12px; font-weight: 600; color: var(--text-3); text-transform: uppercase; letter-spacing: 0.04em; }
  .dim { font-weight: 400; text-transform: none; letter-spacing: 0; }
  .io { margin: 0; max-height: 26em; overflow: auto; background: var(--bg); border: 1px solid var(--border); border-radius: 10px; padding: 10px 12px; white-space: pre-wrap; overflow-wrap: anywhere; font-family: var(--font-mono); font-size: 12.5px; line-height: 1.55; color: var(--text); }
  .io.err { color: var(--status-error); }
  .note { margin: 0; color: var(--text-3); font-size: 13.5px; }
  .more { margin-left: auto; font: inherit; font-size: 12px; font-weight: 500; text-transform: none; letter-spacing: 0; color: var(--text-2); background: none; border: 0; padding: 0; cursor: pointer; text-decoration: underline; }
  @keyframes rot { to { transform: rotate(360deg); } }
  @media (prefers-reduced-motion: reduce) { .glyph { animation: none !important; } }
  @media (max-width: 720px) { .body { margin-left: 8px; } }
</style>
