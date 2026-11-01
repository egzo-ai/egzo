<script lang="ts">
  import { stamp } from "../lib/format";
  import type { AuditEntry, HubAuditEntry } from "../lib/types";

  /** `hub` is the log of the whole hub, which says what project an entry is about. */
  let { entries, hub = false }: { entries: (AuditEntry | HubAuditEntry)[]; hub?: boolean } = $props();
  const projectOf = (e: AuditEntry | HubAuditEntry) => ("projectName" in e && e.projectName) || ("project" in e && e.project) || "";
</script>

<div class="table-wrap">
  <table class="table" data-testid="audit">
    <thead><tr><th>When</th><th>Who</th><th>From</th><th>What</th>{#if hub}<th>Project</th>{/if}<th>Agent</th><th>Detail</th></tr></thead>
    <tbody>
      {#each entries as e}
        <tr class={e.allowed ? "" : "refused"}>
          <td>{stamp(e.at)}</td><td>{e.actor}</td><td>{e.client ?? ""}</td>
          <td>{e.action}{#if !e.allowed} <span class="pill" data-s="warn">refused</span>{/if}</td>
          {#if hub}<td>{projectOf(e)}</td>{/if}
          <td>{e.agent}</td><td class="wrap">{e.detail}</td>
        </tr>
      {:else}
        <tr><td colspan={hub ? 7 : 6} class="muted">Nothing recorded yet.</td></tr>
      {/each}
    </tbody>
  </table>
</div>
