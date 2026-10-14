<script lang="ts">
  import { dismissToast, toasts } from "../lib/app.svelte";
  import Icon from "./Icon.svelte";
</script>

<div class="toasts" aria-live="polite">
  {#each toasts as t (t.id)}
    <div class="toast" data-tone={t.tone} role={t.tone === "error" ? "alert" : "status"}>
      <span class="glyph">{t.tone === "error" ? "✕" : "✓"}</span>
      <div class="body">{t.text}</div>
      <button class="btn" data-variant="quiet" data-size="sm" data-icon-only aria-label="Dismiss" onclick={() => dismissToast(t.id)}><Icon name="x" /></button>
    </div>
  {/each}
</div>
