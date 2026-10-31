<script lang="ts">
  import { api } from "../lib/api";
  import { errorText, toast } from "../lib/app.svelte";
  import { ask } from "../lib/confirm.svelte";
  import { poll } from "../lib/live";
  import type { Invite, NewInvite } from "../lib/types";
  import InviteLink from "../components/InviteLink.svelte";
  import InviteTable from "../components/InviteTable.svelte";
  import ErrorPage from "./ErrorPage.svelte";

  let invites = $state<Invite[] | null>(null);
  /** The invite that was just made: this is the only time its link is seen, so it goes when the page does. */
  let made = $state<NewInvite | null>(null);
  let operator = $state(false);
  let quota = $state(0);
  let busy = $state(false);
  let failure = $state<unknown>(null);

  async function load() {
    try {
      invites = await api.invites();
    } catch (e) {
      failure = e;
    }
  }
  $effect(() => poll(load));

  async function create(e: Event) {
    e.preventDefault();
    if (operator) {
      const ok = await ask({
        title: "Invite an operator?",
        lines: ["Whoever opens the link oversees every project and manages the links, the invites and the users.", "It is one link, and nothing ties it to a person."],
        confirm: "Make the invite", tone: "normal",
      });
      if (!ok) return;
    }
    busy = true;
    try {
      made = await api.createInvite(operator, quota);
      operator = false;
      quota = 0;
      await load();
    } catch (e) {
      toast(errorText(e), "error");
    } finally {
      busy = false;
    }
  }

  async function revoke(i: Invite) {
    try {
      await api.revokeInvite(i.id);
      if (made?.id === i.id) made = null;
      await load();
    } catch (e) {
      toast(errorText(e), "error");
    }
  }
</script>

{#if failure}
  <ErrorPage error={failure} />
{:else}
  <div style="display:grid;gap:var(--space-4)">
    <p class="dim" style="margin:0">Nobody gets in without an invite: an account at the login provider is not enough. An invite here makes an account with what you pick; a project's owner invites to a role on their project, on its Members tab.</p>
    <form class="toolbar" onsubmit={create}>
      <div class="field"><label for="n-quota">Projects they may connect</label>
        <input id="n-quota" class="input" style="width:12ch" type="number" min="0" bind:value={quota} /></div>
      <label class="check" style="align-self:end"><input type="checkbox" bind:checked={operator} /> Operator</label>
      <button class="btn" data-variant="primary" type="submit" style="align-self:end" disabled={busy}>Make an invite</button>
    </form>
    {#if made}<InviteLink invite={made} />{/if}
    {#if invites?.length}
      <InviteTable {invites} onrevoke={revoke} wide />
    {:else if invites}
      <p class="muted" style="margin:0">No invite is waiting.</p>
    {/if}
  </div>
{/if}
