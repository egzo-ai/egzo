<script lang="ts">
  // What an agent wrote, as Markdown: renderMarkdown never lets raw HTML through, so its output is safe to put in the page. A code block
  // gets a copy button.
  import { copyText } from "../../lib/clipboard";
  import { renderInline, renderMarkdown } from "../../lib/markdown";

  let { source, inline = false }: { source: string; inline?: boolean } = $props();
  const html = $derived(inline ? renderInline(source) : renderMarkdown(source));
  let root: HTMLElement | undefined = $state();

  $effect(() => {
    void html;
    if (!root || inline) return;
    for (const pre of root.querySelectorAll("pre")) {
      if (pre.parentElement?.classList.contains("md-code")) continue;
      const wrap = document.createElement("div");
      wrap.className = "md-code";
      pre.replaceWith(wrap);
      wrap.append(pre);
      const button = document.createElement("button");
      button.type = "button";
      button.className = "md-copy";
      button.textContent = "copy";
      button.setAttribute("aria-label", "Copy the code");
      button.addEventListener("click", async () => {
        button.textContent = (await copyText(pre.textContent ?? "")) ? "copied" : "failed";
        setTimeout(() => (button.textContent = "copy"), 1500);
      });
      wrap.append(button);
    }
  });
</script>

{#if inline}
  <span class="md md-inline">{@html html}</span>
{:else}
  <div class="md" bind:this={root}>{@html html}</div>
{/if}

<style>
  .md { line-height: 1.6; overflow-wrap: anywhere; min-width: 0; }
  .md :global(> :first-child) { margin-top: 0; }
  .md :global(> :last-child) { margin-bottom: 0; }
  .md :global(p), .md :global(ul), .md :global(ol), .md :global(blockquote), .md :global(.md-code), .md :global(.md-table) { margin: 0 0 0.75em; }
  .md :global(h1), .md :global(h2), .md :global(h3), .md :global(h4) { font-family: var(--font-display); font-weight: var(--weight-regular); line-height: var(--leading-tight); margin: 1.1em 0 0.5em; letter-spacing: -0.01em; }
  .md :global(h1) { font-size: var(--text-lg); }
  .md :global(h2) { font-size: var(--text-md); }
  .md :global(h3), .md :global(h4) { font-size: var(--text-sm); font-weight: var(--weight-bold); font-family: var(--font-mono); }
  .md :global(ul), .md :global(ol) { padding-left: 1.5em; }
  .md :global(li + li) { margin-top: 0.2em; }
  .md :global(li > p) { margin: 0; }
  .md :global(blockquote) { border-left: 2px solid var(--border-strong); padding-left: var(--space-3); color: var(--text-2); }
  .md :global(hr) { border: 0; border-top: 1px solid var(--border); margin: 1em 0; }
  .md :global(code) { font-size: 0.92em; overflow-wrap: anywhere; }
  .md :global(.md-code) { position: relative; }
  .md :global(pre) { margin: 0; background: var(--surface-1); border: 1px solid var(--border); border-radius: var(--radius-2); padding: var(--space-3); overflow-x: auto; font-size: var(--text-xs); line-height: 1.5; }
  .md :global(pre code) { background: none; padding: 0; overflow-wrap: normal; white-space: pre; font-size: inherit; }
  .md :global(.md-copy) { position: absolute; top: 6px; right: 6px; font: inherit; font-size: var(--text-2xs); color: var(--text-3); background: var(--surface-2); border: 1px solid var(--border); border-radius: var(--radius-1); padding: 1px 6px; cursor: pointer; opacity: 0; transition: opacity var(--dur-fast); }
  .md :global(.md-code:hover .md-copy), .md :global(.md-copy:focus-visible) { opacity: 1; }
  @media (hover: none) { .md :global(.md-copy) { opacity: 1; } }
  .md :global(.md-table) { overflow-x: auto; }
  .md :global(table) { border-collapse: collapse; font-size: var(--text-xs); }
  .md :global(th), .md :global(td) { border: 1px solid var(--border); padding: 4px 8px; text-align: left; vertical-align: top; }
  .md :global(th) { background: var(--surface-1); font-weight: var(--weight-bold); }
  .md-inline :global(code) { font-size: 0.95em; }
</style>
