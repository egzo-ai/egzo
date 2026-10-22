<script lang="ts">
  import { api } from "../lib/api";
  import { app, errorText, refreshSession, toast } from "../lib/app.svelte";
  import { ask } from "../lib/confirm.svelte";
  import { nav } from "../lib/router.svelte";
  import type { Identity } from "../lib/types";
  import PersonCell from "../components/PersonCell.svelte";

  const params = $derived(new URLSearchParams(nav.path.split("?")[1] ?? ""));
  const error = $derived(params.get("error"));
  const linked = $derived(params.get("linked") === "1");
  let busy = $state(false);

  const current = $derived(app.account.identities.find((i) => i.current));
  const former = $derived(app.account.identities.filter((i) => !i.current));
  // a way to sign in is a password (while local login is on) or an account at the current provider
  const ways = $derived((app.account.hasPassword ? 1 : 0) + (current ? 1 : 0));

  $effect(() => {
    void refreshSession().catch(() => {});
  });

  async function link() {
    busy = true;
    try {
      const { url } = await api.link();
      location.assign(url);
    } catch (e) {
      toast(errorText(e), "error");
      busy = false;
    }
  }

  async function unlink(i: Identity) {
    const ok = await ask({
      title: `Unlink ${i.email || i.issuer}?`,
      lines: i.current
        ? [`You will no longer sign in with this account at ${app.account.sso}.`, "Sessions you opened with it end, and their terminals close."]
        : [`This account is at ${i.issuer}, which is not the hub's login provider any more: it cannot sign in.`],
      confirm: "Unlink",
    });
    if (!ok) return;
    try {
      await api.unlink(i.id);
      toast("Unlinked");
      await refreshSession();
    } catch (e) {
      toast(errorText(e), "error");
    }
  }
</script>

<div style="display:grid;gap:var(--space-4)" data-testid="account">
  {#if error}<div class="banner" role="alert"><span class="tag">✕ Not linked</span><div class="body">{error}</div></div>{/if}
  {#if linked}<div class="banner" role="status"><span class="tag">✓ Linked</span><div class="body">You can now sign in with {app.account.sso}.</div></div>{/if}
  {#if app.user}<p style="margin:0"><PersonCell user={app.user} me /></p>{/if}

  <section class="panel" aria-labelledby="ways-in">
    <header><h2 class="h-section" id="ways-in">How you sign in</h2></header>
    <div class="pad" style="display:grid;gap:var(--space-3)">
      {#if app.account.hasPassword}<p style="margin:0">With a password, as <b>{app.user?.username}</b>.</p>{/if}
      {#if current}
        <p style="margin:0" data-testid="linked-as">Linked as <b>{current.email || current.name || current.id}</b> at {current.issuer}.
          <button class="btn" data-size="sm" data-variant="danger" disabled={ways < 2} title={ways < 2 ? "This is your last way to sign in" : ""} onclick={() => unlink(current)}>Unlink</button></p>
      {:else if app.account.sso}
        <p style="margin:0">
          <button class="btn" data-variant="primary" disabled={busy} onclick={link}>Link {app.account.sso}</button>
          <span class="muted">Sign in at the provider once; afterwards you can sign in with it here.</span>
        </p>
      {/if}
      {#each former as i (i.id)}
        <p style="margin:0" class="muted">{i.email || i.name || i.id} at {i.issuer} (not the current provider)
          <button class="btn" data-size="sm" onclick={() => unlink(i)}>Unlink</button></p>
      {/each}
      {#if !app.account.sso && !current}<p class="muted" style="margin:0">The hub has no single sign-on configured.</p>{/if}
    </div>
  </section>
</div>
