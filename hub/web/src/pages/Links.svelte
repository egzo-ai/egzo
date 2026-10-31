<script lang="ts">
  import { api } from "../lib/api";
  import { errorText, refreshProjects, toast } from "../lib/app.svelte";
  import { ask } from "../lib/confirm.svelte";
  import { ago, stamp } from "../lib/format";
  import { poll } from "../lib/live";
  import { href } from "../lib/router.svelte";
  import type { Link } from "../lib/types";
  import ErrorPage from "./ErrorPage.svelte";

  let links = $state<Link[] | null>(null);
  let failure = $state<unknown>(null);

  async function load() {
    try {
      links = await api.links();
    } catch (e) {
      failure = e;
    }
  }
  $effect(() => poll(load));

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
      lines: ["Its link may ask again, as a new project with no roles."],
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

  const tone: Record<string, string> = { pending: "warn", approved: "running", refused: "error", revoked: "error" };
</script>

{#if failure}
  <ErrorPage error={failure} />
{:else}
  <div style="display:grid;gap:var(--space-4)">
    <p class="dim" style="margin:0">The projects that dialled this hub. A project names its hub in its own file and connects itself; this hub holds no way into its machine.</p>
    <div class="table-wrap">
      <table class="table" data-testid="links">
        <thead><tr><th>Project</th><th>Host</th><th>State</th><th>Link</th><th>Owner</th><th></th></tr></thead>
        <tbody>
          {#each links ?? [] as l (l.id)}
            <tr data-link={l.name}>
              <td>{#if l.state === "approved"}<a class="name" href={href.project(l.id)}>{l.name}</a>{:else}{l.name}{/if}</td>
              <td class="muted">{l.host}</td>
              <td><span class="pill" data-s={tone[l.state]}>{l.state}</span> {#if l.message}<span class="muted">{l.message}</span>{/if}</td>
              <td>{#if !l.offlineSince}connected{:else}<span class="muted" title={stamp(l.offlineSince)}>offline {ago(l.offlineSince)}</span>{/if}</td>
              <td>{l.owner ?? "–"}</td>
              <td class="actions">
                {#if l.state === "pending"}<a class="btn" data-size="sm" href="/link/{l.code}">Review</a>{/if}
                {#if l.state === "approved"}<button class="btn" data-variant="danger" data-size="sm" aria-label="Revoke {l.name}" onclick={() => revoke(l)}>Revoke</button>{/if}
                {#if l.state === "refused" || l.state === "revoked"}<button class="btn" data-size="sm" aria-label="Forget {l.name}" onclick={() => forget(l)}>Forget</button>{/if}
              </td>
            </tr>
          {:else}
            <tr><td colspan="6" class="muted">No project has dialled this hub yet.</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  </div>
{/if}
