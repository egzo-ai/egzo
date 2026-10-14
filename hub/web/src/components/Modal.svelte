<script lang="ts">
  import type { Snippet } from "svelte";
  import { onMount } from "svelte";
  import Icon from "./Icon.svelte";

  let { title, tone, onclose, children, footer }: { title: string; tone?: "danger"; onclose: () => void; children: Snippet; footer?: Snippet } = $props();
  let dialog: HTMLDivElement;
  const titleId = "dlg-" + Math.random().toString(36).slice(2, 8);

  onMount(() => {
    const before = document.activeElement as HTMLElement | null;
    const target = (dialog.querySelector("[data-autofocus]") ?? dialog.querySelector("input, select, textarea, button:not([data-close])")) as HTMLElement | null;
    target?.focus();
    return () => before?.focus?.();
  });

  function keydown(e: KeyboardEvent) {
    if (e.key === "Escape") {
      e.stopPropagation();
      onclose();
    } else if (e.key === "Tab") {
      const items = [...dialog.querySelectorAll<HTMLElement>("a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled])")];
      if (!items.length) return;
      const first = items[0]!, last = items[items.length - 1]!;
      if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
      else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
    }
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events -->
<div class="scrim" role="presentation" onmousedown={(e) => e.target === e.currentTarget && onclose()}>
  <div class="dialog" role="dialog" aria-modal="true" aria-labelledby={titleId} data-tone={tone} bind:this={dialog} onkeydown={keydown} tabindex="-1">
    <header>
      <h2 id={titleId}>{title}</h2>
      <button class="btn" data-variant="quiet" data-size="sm" data-icon-only data-close aria-label="Close" onclick={onclose}><Icon name="x" /></button>
    </header>
    <div class="content">{@render children()}</div>
    {#if footer}<footer>{@render footer()}</footer>{/if}
  </div>
</div>
