<script lang="ts">
  import { api } from "../lib/api";
  import { app, errorText, refreshProjects, refreshSession, toast } from "../lib/app.svelte";
  import { ask } from "../lib/confirm.svelte";
  import { ago, stamp, until } from "../lib/format";
  import { poll } from "../lib/live";
  import { href } from "../lib/router.svelte";
  import type { Link, LinkCode, NewLinkCode } from "../lib/types";
  import ErrorPage from "./ErrorPage.svelte";

  let links = $state<Link[] | null>(null);
  let codes = $state<LinkCode[]>([]);
  /** The code that was just made: this is the only time it is seen, so it goes when the page does. */
  let made = $state<NewLinkCode | null>(null);
  let busy = $state(false);
  let failure = $state<unknown>(null);

  async function load() {
    try {
      // a person with a quota has their own codes; the projects of the hub are the operators' to look after
      [links, codes] = await Promise.all([app.user?.isOperator ? api.links() : Promise.resolve(null), api.linkCodes()]);
    } catch (e) {
      failure = e;
    }
  }
  $effect(() => poll(load));

  async function makeCode() {
    busy = true;
    try {
      made = await api.createLinkCode();
      await Promise.all([load(), refreshSession()]);
    } catch (e) {
      toast(errorText(e), "error");
    } finally {
      busy = false;
    }
  }

  async function cancelCode(c: LinkCode) {
    try {
      await api.cancelLinkCode(c.id);
      if (made?.id === c.id) made = null;
      await load();
    } catch (e) {
      toast(errorText(e), "error");
    }
  }

  async function revoke(l: Link) {
    const ok = await ask({
      title: `Revoke ${l.name}?`,
      lines: ["It leaves the hub: its terminals and what runs over its link end, and its link is refused from now on.", "The project keeps running on its machine."],
      confirm: "Revoke", typed: l.name,
    });
    if (!ok) return;
    try {
      await api.revokeLink(l.id, "");
      toast(`Revoked ${l.name}`);
      await Promise.all([load(), refreshProjects()]);
    } catch (e) {
      toast(errorText(e), "error");
    }
  }

  async function forget(l: Link) {
    const ok = await ask({
      title: `Forget ${l.name}?`,
      lines: ["A new link code can then register it, as a new project with no roles."],
      confirm: "Forget",
    });
    if (!ok) return;
    try {
      await api.forgetLink(l.id);
      toast(`Forgot ${l.name}`);
      await load();
    } catch (e) {
      toast(errorText(e), "error");
    }
  }

  const tone: Record<string, string> = { approved: "running", revoked: "error" };
  const ownerless = (l: Link) => l.state === "approved" && l.owners.length === 0;
  // what needs somebody comes first
  const rows = $derived([...(links ?? [])].sort((a, b) => Number(ownerless(b)) - Number(ownerless(a))));
</script>

{#if failure}
  <ErrorPage error={failure} />
{:else}
  <div style="display:grid;gap:var(--space-4)">
    <p class="dim" style="margin:0">The projects linked to this hub. A project names its hub in its own file and connects itself; this hub holds no way into its machine.</p>
    <div class="btn-group">
      <button class="btn" data-variant="primary" disabled={busy || !app.account.canLink} onclick={makeCode}>Link a project</button>
      {#if !app.user?.isOperator}<span class="muted" data-testid="quota">{app.account.connectedProjects} of {app.user?.projectQuota} projects</span>{/if}
    </div>
    {#if made}
      <section class="panel" data-testid="new-code" style="max-width:80ch">
        <div class="pad" style="display:grid;gap:var(--space-3)">
          <p style="margin:0">This code is shown once. It registers one project, as you, until {stamp(made.expiresAt)}. Whoever holds it can use it: hand it over the way you would a password.</p>
          <dl class="kv">
            <dt>code</dt><dd><code data-testid="link-code">{made.code}</code></dd>
          </dl>
          <p class="muted" style="margin:0">In the project's file, <code>hub: {made.hub}</code>. Then run these, and paste the code when it asks:</p>
          <pre class="art" style="margin:0" data-testid="link-command">egzo project up
egzo project register</pre>
        </div>
      </section>
    {/if}
    {#if codes.length}
      <div class="table-wrap">
        <table class="table" data-testid="link-codes">
          <thead><tr><th>Code</th><th>Made by</th><th>Expires</th><th></th></tr></thead>
          <tbody>
            {#each codes as c (c.id)}
              <tr data-code={c.id}>
                <td class="muted">{c.id}</td>
                <td>{c.createdBy}</td>
                <td class="muted" title={stamp(c.expiresAt)}>{until(c.expiresAt)}</td>
                <td class="actions"><button class="btn" data-size="sm" aria-label="Cancel code {c.id}" onclick={() => cancelCode(c)}>Cancel</button></td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
    {#if app.user?.isOperator}
    <div class="table-wrap">
      <table class="table" data-testid="links">
        <thead><tr><th>Project</th><th>Host</th><th>State</th><th>Link</th><th>Owners</th><th>Connected by</th><th></th></tr></thead>
        <tbody>
          {#each rows as l (l.id)}
            <tr data-link={l.name}>
              <td>{#if l.state === "approved"}<a class="name" href={href.project(l.id)}>{l.name}</a>{:else}{l.name}{/if}</td>
              <td class="muted">{l.host}</td>
              <td><span class="pill" data-s={tone[l.state]}>{l.state}</span> {#if l.message}<span class="muted">{l.message}</span>{/if}</td>
              <td>{#if !l.offlineSince}connected{:else}<span class="muted" title={stamp(l.offlineSince)}>offline {ago(l.offlineSince)}</span>{/if}</td>
              <td>{#if ownerless(l)}<a class="pill" data-s="warn" data-testid="no-owner" href={href.project(l.id, "members")}>no owner</a>{:else}{l.owners.join(", ") || "–"}{/if}</td>
              <td class="muted">{l.owner ?? "–"}</td>
              <td class="actions">
                {#if l.state === "approved"}<button class="btn" data-variant="danger" data-size="sm" aria-label="Revoke {l.name}" onclick={() => revoke(l)}>Revoke</button>{/if}
                {#if l.state === "revoked"}<button class="btn" data-size="sm" aria-label="Forget {l.name}" onclick={() => forget(l)}>Forget</button>{/if}
              </td>
            </tr>
          {:else}
            <tr><td colspan="7" class="muted">No project has linked to this hub yet.</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
    {/if}
  </div>
{/if}
