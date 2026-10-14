<script lang="ts">
  import { stamp } from "../lib/format";
  import type { AuditEntry } from "../lib/types";

  let { entries }: { entries: AuditEntry[] } = $props();
</script>

<div class="table-wrap">
  <table class="table" data-testid="audit">
    <thead><tr><th>When</th><th>Who</th><th>What</th><th>Agent</th><th>Detail</th></tr></thead>
    <tbody>
      {#each entries as e}
        <tr class={e.allowed ? "" : "refused"}>
          <td>{stamp(e.at)}</td><td>{e.actor}</td>
          <td>{e.action}{#if !e.allowed} <span class="pill" data-s="warn">refused</span>{/if}</td>
          <td>{e.agent}</td><td class="wrap">{e.detail}</td>
        </tr>
      {:else}
        <tr><td colspan="5" class="muted">Nothing recorded yet.</td></tr>
      {/each}
    </tbody>
  </table>
</div>
