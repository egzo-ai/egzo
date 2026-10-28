<script lang="ts">
  import { dismissToast, toasts } from "../lib/app.svelte";
  import Icon from "./Icon.svelte";
</script>

<div class="toasts" aria-live="polite">
  {#each toasts as t (t.id)}
    <div class="toast" data-tone={t.tone} role={t.tone === "error" ? "alert" : "status"} aria-busy={t.tone === "busy"}>
      <span class="glyph">{t.tone === "error" ? "✕" : t.tone === "busy" ? "◐" : "✓"}</span>
      <div class="body">{#if t.link}<a href={t.link}>{t.text}</a>{:else}{t.text}{/if}</div>
      <button class="btn" data-variant="quiet" data-size="sm" data-icon-only aria-label="Dismiss" onclick={() => dismissToast(t.id)}><Icon name="x" /></button>
    </div>
  {/each}
</div>
