<script lang="ts">
  import { clock } from "../lib/format";
  import type { TimelineEvent } from "../lib/types";

  let { events }: { events: TimelineEvent[] | null } = $props();
</script>

{#if events === null}
  <p class="muted">Loading…</p>
{:else if events.length === 0}
  <p class="muted">Nothing has happened yet.</p>
{:else}
  <div class="table-wrap">
    <ol class="timeline" data-testid="timeline">
      {#each events as e}
        <li>
          <time datetime={e.time}>{clock(e.time)}</time><span class="type" data-t={e.type}>{e.type}</span>
          <span class="text">{#if e.agent}<b>{e.agent}</b>{" "}{/if}{e.text}{#if e.actor}{" "}<span class="muted">· {e.actor}</span>{/if}</span>
        </li>
      {/each}
    </ol>
  </div>
{/if}
