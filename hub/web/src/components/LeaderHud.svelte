<script lang="ts">
  import { LEADER_KEYS } from "../lib/leader";
</script>

<div class="leader" role="status" aria-live="polite">
  <header><span class="kbd">Ctrl</span><span class="kbd">]</span> <span class="grow">then a key</span><span>Esc cancels</span></header>
  <div class="keys">
    {#each LEADER_KEYS as [key, text]}
      <div><span class="kbd">{key}</span>{text}</div>
    {/each}
  </div>
</div>
