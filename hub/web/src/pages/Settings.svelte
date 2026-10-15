<script lang="ts">
  import { ApiError } from "../lib/api";
  import { app } from "../lib/app.svelte";
  import { href, type SettingsTab } from "../lib/router.svelte";
  import Engines from "./Engines.svelte";
  import ErrorPage from "./ErrorPage.svelte";
  import Users from "./Users.svelte";

  let { tab }: { tab: SettingsTab } = $props();

  const tabs: [SettingsTab, string][] = [["engines", "Engines"], ["users", "Users"]];
</script>

{#if app.user?.isOperator}
  <div class="page">
    <div class="phead"><h1 class="h-page">Settings</h1></div>
    <div class="tabs" role="tablist" aria-label="Settings sections">
      {#each tabs as [id, label]}
        <a class="tab" role="tab" href={href.settings(id)} aria-selected={tab === id}>{label}</a>
      {/each}
    </div>
    {#if tab === "engines"}<Engines />{:else}<Users />{/if}
  </div>
{:else}
  <ErrorPage error={new ApiError(403, "forbidden", "Only an operator manages the hub's settings.")} />
{/if}
