<script lang="ts">
  import { ApiError } from "../lib/api";
  import { app, seesLinks } from "../lib/app.svelte";
  import { href, type SettingsTab } from "../lib/router.svelte";
  import Account from "./Account.svelte";
  import ErrorPage from "./ErrorPage.svelte";
  import HubAudit from "./HubAudit.svelte";
  import Invites from "./Invites.svelte";
  import Links from "./Links.svelte";
  import Users from "./Users.svelte";

  let { tab }: { tab: SettingsTab } = $props();

  // everyone has an account; who may connect a project has links, and an operator manages the invites and the users and reads the audit log
  const tabs = $derived.by<[SettingsTab, string][]>(() => {
    if (app.user?.isOperator) return [["links", "Links"], ["invites", "Invites"], ["users", "Users"], ["audit", "Audit log"], ["account", "Account"]];
    return seesLinks() ? [["links", "Links"], ["account", "Account"]] : [["account", "Account"]];
  });
</script>

{#if tabs.some(([id]) => id === tab)}
  <div class="page">
    <div class="phead"><h1 class="h-page">Settings</h1></div>
    <div class="tabs" role="tablist" aria-label="Settings sections">
      {#each tabs as [id, label]}
        <a class="tab" role="tab" href={href.settings(id)} aria-selected={tab === id}>{label}</a>
      {/each}
    </div>
    {#if tab === "links"}<Links />{:else if tab === "invites"}<Invites />{:else if tab === "users"}<Users />{:else if tab === "audit"}<HubAudit />{:else}<Account />{/if}
  </div>
{:else}
  <ErrorPage error={new ApiError(403, "forbidden", "That part of the settings is not yours to manage.")} />
{/if}
