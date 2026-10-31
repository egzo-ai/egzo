<script lang="ts">
  import { ApiError } from "../lib/api";
  import { app } from "../lib/app.svelte";
  import { href, type SettingsTab } from "../lib/router.svelte";
  import Account from "./Account.svelte";
  import ErrorPage from "./ErrorPage.svelte";
  import Links from "./Links.svelte";
  import Users from "./Users.svelte";

  let { tab }: { tab: SettingsTab } = $props();

  // everyone has an account; only an operator manages the links and the users
  const tabs = $derived<[SettingsTab, string][]>(app.user?.isOperator ? [["links", "Links"], ["users", "Users"], ["account", "Account"]] : [["account", "Account"]]);
</script>

{#if app.user?.isOperator || tab === "account"}
  <div class="page">
    <div class="phead"><h1 class="h-page">Settings</h1></div>
    <div class="tabs" role="tablist" aria-label="Settings sections">
      {#each tabs as [id, label]}
        <a class="tab" role="tab" href={href.settings(id)} aria-selected={tab === id}>{label}</a>
      {/each}
    </div>
    {#if tab === "links"}<Links />{:else if tab === "users"}<Users />{:else}<Account />{/if}
  </div>
{:else}
  <ErrorPage error={new ApiError(403, "forbidden", "Only an operator manages the hub's settings.")} />
{/if}
