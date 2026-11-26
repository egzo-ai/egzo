<script lang="ts">
  // One tool call of a turn: a line saying what it does and how it went, which opens on what it was called with and what it gave back.
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
    {#if tool.status !== "done" && tool.status !== "running" && tool.status !== "preparing"}<span class="tag">{label[tool.status]}</span>{/if}
  </summary>
  <div class="body">
    {#if tool.note}<p class="note">{tool.note}</p>{/if}
    {#if tool.command}
      <div class="label">Command</div>
      <pre class="io" data-testid="tool-command">$ {tool.command}</pre>
    {:else if tool.input}
      <div class="label">Input</div>
      <pre class="io" data-testid="tool-input">{tool.input}</pre>
    {/if}
    {#if tool.error}
      <div class="label">Error</div>
      <pre class="io err" data-testid="tool-error">{tool.error}</pre>
    {/if}
    {#if tool.output}
      <div class="label">Output{#if lines.length > maxLines && !full} <span class="muted">· last {maxLines} of {lines.length} lines</span>{/if}</div>
      {#if lines.length > maxLines}<button type="button" class="more" onclick={() => (full = !full)}>{full ? "Show the end only" : "Show all"}</button>{/if}
      <pre class="io" data-testid="tool-output">{output}</pre>
    {/if}
    {#if !hasDetails}<p class="note">{tool.status === "running" || tool.status === "preparing" ? "Nothing yet." : "No details."}</p>{/if}
  </div>
</details>

<style>
  .tool { border: 0; padding: 0; display: block; font-size: var(--text-xs); }
  .tool > summary { list-style: none; display: flex; align-items: baseline; gap: var(--space-2); padding: 3px var(--space-2); margin: 0 calc(-1 * var(--space-2)); border-radius: var(--radius-1); color: var(--text-2); font-size: var(--text-xs); min-width: 0; }
  .tool > summary::-webkit-details-marker { display: none; }
  .tool > summary:hover { background: var(--surface-1); color: var(--text); }
  .tool[open] > summary { margin-bottom: 0; }
  .glyph { flex: none; width: 12px; text-align: center; color: var(--status-running); }
  .glyph::before { content: "✓"; }
  .tool[data-status="running"] .glyph::before, .tool[data-status="preparing"] .glyph::before { content: "●"; }
  .tool[data-status="running"] .glyph, .tool[data-status="preparing"] .glyph { animation: pulse 1s infinite alternate; }
  .tool[data-status="failed"] .glyph { color: var(--status-error); }
  .tool[data-status="failed"] .glyph::before { content: "✕"; }
  .tool[data-status="cancelled"] .glyph { color: var(--status-stopped); }
  .tool[data-status="cancelled"] .glyph::before { content: "–"; }
  .tool[data-status="waiting"] .glyph { color: var(--status-waiting); }
  .tool[data-status="waiting"] .glyph::before { content: "?"; font-weight: 800; }
  .name { flex: none; color: var(--text); font-weight: var(--weight-medium); }
  .said { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .tool[open] .said { white-space: normal; overflow-wrap: anywhere; }
  .tag { flex: none; color: var(--text-3); }
  .tool[data-status="failed"] .tag { color: var(--status-error); }
  .tool[data-status="waiting"] .tag { color: var(--status-waiting); }
  .body { display: grid; gap: var(--space-1); margin: var(--space-1) 0 var(--space-2) 20px; padding-left: var(--space-3); border-left: 1px solid var(--border); min-width: 0; }
  .label { margin-top: var(--space-1); font-size: var(--text-2xs); text-transform: uppercase; letter-spacing: var(--tracking-label); color: var(--text-3); }
  .io { margin: 0; max-height: 24em; overflow: auto; background: var(--surface-1); border: 1px solid var(--border); border-radius: var(--radius-1); padding: var(--space-2); white-space: pre-wrap; overflow-wrap: anywhere; font-size: var(--text-2xs); line-height: 1.5; color: var(--text-2); }
  .io.err { color: var(--status-error); }
  .note { margin: 0; color: var(--text-3); }
  .more { justify-self: start; font: inherit; font-size: var(--text-2xs); color: var(--accent-text); background: none; border: 0; padding: 0; cursor: pointer; text-decoration: underline; text-decoration-color: var(--accent-line); }
  @media (prefers-reduced-motion: reduce) { .glyph { animation: none !important; } }
</style>
