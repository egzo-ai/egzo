<script lang="ts">
  import { ApiError, api } from "../lib/api";
  import { errorText, refreshProjects, toast } from "../lib/app.svelte";
  import { ask } from "../lib/confirm.svelte";
  import { ago, stamp } from "../lib/format";
  import { href, navigate } from "../lib/router.svelte";
  import type { Link } from "../lib/types";

  let { code }: { code: string } = $props();

  let link = $state<Link | null>(null);
  let failure = $state<unknown>(null);
  let refused = $state(false);
  let busy = $state(false);
  let why = $state("");

  $effect(() => {
    api.pendingLink(code).then((l) => (link = l)).catch((e) => (failure = e));
  });

  async function approve() {
    busy = true;
    try {
      const project = await api.approveLink(code);
      toast(`Linked ${link?.name}`);
      await refreshProjects();
      navigate(href.project(project), true);
    } catch (e) {
      toast(errorText(e), "error");
    } finally {
      busy = false;
    }
  }

  async function refuse() {
    const ok = await ask({
      title: `Refuse ${link?.name}?`,
      lines: ["Its link is told, and is refused from now on.", "The project keeps running on its machine."],
      confirm: "Refuse",
    });
    if (!ok) return;
    busy = true;
    try {
      await api.refuseLink(code, why.trim());
      refused = true;
    } catch (e) {
      toast(errorText(e), "error");
    } finally {
      busy = false;
    }
  }
</script>

<div class="page">
  <div class="phead"><h1 class="h-page">Register a project</h1></div>
  {#if refused}
    <p data-testid="refused">{link?.name} was refused. Its link says so.</p>
  {:else if failure instanceof ApiError && failure.status === 403}
    <div class="empty"><h2>Only an operator approves a project</h2><p>Hand this address to an operator of the hub.</p></div>
  {:else if failure instanceof ApiError && failure.status === 404}
    <div class="empty"><h2>Nothing waits under this code</h2><p>It was decided already, or its link went away. <code>egzo ps</code> on the project says where it stands.</p></div>
  {:else if failure}
    <div class="banner" role="alert"><span class="tag">✕ Not read</span><div class="body">{errorText(failure)}</div></div>
  {:else if link}
    <p class="dim" style="margin:0">A project asks to link to this hub. What it says of itself below is what it says: approve it if you expect it.</p>
    <section class="panel" style="max-width:60ch">
      <div class="pad">
        <dl class="kv" data-testid="registration">
          <dt>project</dt><dd>{link.name}</dd>
          <dt>host</dt><dd>{link.host || "—"}</dd>
          <dt>templates</dt><dd>{link.templates.join(", ") || "—"}</dd>
          <dt>egzo</dt><dd>{link.release || "—"}</dd>
          <dt>asked</dt><dd title={stamp(link.createdAt)}>{ago(link.createdAt)}</dd>
          <dt>code</dt><dd><code>{code}</code></dd>
        </dl>
      </div>
    </section>
    <p class="muted" style="margin:0">Once approved, you own it on this hub. The hub can spawn from its templates, start, stop and remove its agents, and attach to their sessions. It can do nothing else on that machine.</p>
    <div class="field" style="max-width:60ch"><label for="why">Why, if you refuse (the link shows it)</label><input id="why" class="input" bind:value={why} maxlength="500" /></div>
    <div class="btn-group">
      <button class="btn" data-variant="primary" disabled={busy} onclick={approve}>Approve</button>
      <button class="btn" data-variant="danger" disabled={busy} onclick={refuse}>Refuse</button>
    </div>
  {:else}
    <p class="muted">Loading…</p>
  {/if}
</div>
