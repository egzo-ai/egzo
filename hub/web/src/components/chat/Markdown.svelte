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
      const bar = document.createElement("div");
      bar.className = "md-bar";
      const lang = document.createElement("span");
      lang.textContent = /language-([\w+#-]+)/.exec(pre.querySelector("code")?.className ?? "")?.[1] ?? "";
      bar.append(lang);
      wrap.append(bar, pre);
      const button = document.createElement("button");
      button.type = "button";
      button.className = "md-copy";
      button.textContent = "copy";
      button.setAttribute("aria-label", "Copy the code");
      button.addEventListener("click", async () => {
        button.textContent = (await copyText(pre.textContent ?? "")) ? "copied" : "failed";
        setTimeout(() => (button.textContent = "copy"), 1500);
      });
      bar.append(button);
    }
  });
</script>

{#if inline}
  <span class="md md-inline">{@html html}</span>
{:else}
  <div class="md" bind:this={root}>{@html html}</div>
{/if}

<style>
  .md { font-size: 16px; line-height: 1.7; overflow-wrap: anywhere; min-width: 0; color: var(--text); }
  .md :global(> :first-child) { margin-top: 0; }
  .md :global(> :last-child) { margin-bottom: 0; }
  .md :global(p), .md :global(ul), .md :global(ol), .md :global(blockquote), .md :global(.md-code), .md :global(.md-table) { margin: 0 0 1em; }
  .md :global(h1), .md :global(h2), .md :global(h3), .md :global(h4) { font-family: inherit; font-weight: 650; line-height: 1.3; margin: 1.5em 0 0.6em; letter-spacing: -0.015em; color: var(--text); }
  .md :global(h1) { font-size: 1.5em; }
  .md :global(h2) { font-size: 1.3em; }
  .md :global(h3) { font-size: 1.12em; }
  .md :global(h4) { font-size: 1em; }
  .md :global(strong) { font-weight: 650; }
  .md :global(a) { color: inherit; text-decoration-color: var(--accent); text-decoration-thickness: 1.5px; }
  .md :global(ul), .md :global(ol) { padding-left: 1.6em; }
  .md :global(li) { margin: 0.3em 0; padding-left: 0.2em; }
  .md :global(li::marker) { color: var(--text-3); }
  .md :global(li > p) { margin: 0.3em 0; }
  .md :global(blockquote) { border-left: 3px solid var(--border-strong); padding: 0.1em 0 0.1em 1em; color: var(--text-2); }
  .md :global(hr) { border: 0; border-top: 1px solid var(--border); margin: 1.6em 0; }
  .md :global(:not(pre) > code) { font-family: var(--font-mono); font-size: 0.85em; background: var(--surface-2); color: var(--text); padding: 0.15em 0.4em; border-radius: 6px; overflow-wrap: anywhere; }
  .md :global(.md-code) { border: 1px solid var(--border); border-radius: 12px; overflow: hidden; background: var(--surface-1); }
  .md :global(.md-bar) { display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 6px 8px 6px 14px; font-size: 12.5px; color: var(--text-3); background: var(--surface-2); border-bottom: 1px solid var(--border); }
  .md :global(pre) { margin: 0; padding: 14px 16px; overflow-x: auto; font-family: var(--font-mono); font-size: 13.5px; line-height: 1.6; background: transparent; }
  .md :global(pre code) { background: none; padding: 0; overflow-wrap: normal; white-space: pre; font-size: inherit; font-family: inherit; }
  .md :global(.md-copy) { font: inherit; font-size: 12.5px; color: var(--text-2); background: transparent; border: 0; border-radius: 6px; padding: 2px 8px; cursor: pointer; }
  .md :global(.md-copy:hover) { background: var(--surface-3); color: var(--text); }
  .md :global(.md-table) { overflow-x: auto; border: 1px solid var(--border); border-radius: 12px; }
  .md :global(table) { border-collapse: collapse; width: 100%; font-size: 15px; }
  .md :global(th), .md :global(td) { border-bottom: 1px solid var(--border); padding: 8px 12px; text-align: left; vertical-align: top; }
  .md :global(tr:last-child td) { border-bottom: 0; }
  .md :global(th) { background: var(--surface-1); font-weight: 600; }
  .md-inline { font-size: inherit; }
  .md-inline :global(code) { font-family: var(--font-mono); font-size: 0.88em; background: var(--surface-2); padding: 0.1em 0.35em; border-radius: 5px; }
</style>
