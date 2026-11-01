<script lang="ts">
  import { api } from "../lib/api";
  import { poll } from "../lib/live";
  import type { HubAuditEntry } from "../lib/types";
  import AuditTable from "../components/AuditTable.svelte";
  import ErrorPage from "./ErrorPage.svelte";

  const pageSize = 100;

  let entries = $state<HubAuditEntry[] | null>(null);
  let more = $state(false);
  let failure = $state<unknown>(null);

  function merge(rows: HubAuditEntry[]) {
    const byId = new Map((entries ?? []).map((e) => [e.id, e]));
    for (const row of rows) byId.set(row.id, row);
    entries = [...byId.values()].sort((a, b) => b.id - a.id);
  }

  async function load() {
    try {
      const rows = await api.hubAudit();
      if (entries === null) more = rows.length === pageSize;
      merge(rows);
    } catch (e) {
      failure = e;
    }
  }
  $effect(() => poll(load, 5000));

  async function older() {
    try {
      const rows = await api.hubAudit(entries?.at(-1)?.id);
      more = rows.length === pageSize;
      merge(rows);
    } catch (e) {
      failure = e;
    }
  }
</script>

{#if failure}
  <ErrorPage error={failure} />
{:else if entries}
  <p class="muted">Everything done on this hub, on projects and off them, those that are gone included.</p>
  <AuditTable {entries} hub />
  {#if more}<p><button class="btn" onclick={older}>Older</button></p>{/if}
{:else}
  <p class="muted">Loading…</p>
{/if}
