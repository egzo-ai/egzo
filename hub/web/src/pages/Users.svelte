<script lang="ts">
  import { api } from "../lib/api";
  import { errorText, refreshProjects, refreshSession, toast } from "../lib/app.svelte";
  import { ask } from "../lib/confirm.svelte";
  import { initials } from "../lib/format";
  import { poll } from "../lib/live";
  import type { UserRow } from "../lib/types";
  import ErrorPage from "./ErrorPage.svelte";

  let users = $state<UserRow[] | null>(null);
  let me = $state("");
  let failure = $state<unknown>(null);
  let busy = $state("");

  const operators = $derived((users ?? []).filter((u) => u.isOperator).length);

  async function load() {
    try {
      const answer = await api.users();
      users = answer.users;
      me = answer.me;
    } catch (e) {
      failure = e;
    }
  }

  $effect(() => poll(load));

  const joined = (iso: string) => new Date(iso).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });

  async function set(u: UserRow, on: boolean) {
    const ok = await ask(
      on
        ? {
            title: `Make ${u.name} an operator?`,
            lines: [
              `${u.name} will manage the engines and the users, and hold every project on every engine: other people's agents and terminals included.`,
              "Everything they do stays in the audit log under their name.",
            ],
            confirm: "Make operator", tone: "normal",
          }
        : {
            title: `Remove operator from ${u.name}?`,
            lines: [
              `${u.name} stops managing the engines and the users, and stops holding every project.`,
              "They keep any role they were given on a project, and lose the rest.",
            ],
            confirm: "Remove operator",
          },
    );
    if (!ok) return;
    busy = u.id;
    try {
      await api.setOperator(u.id, on);
      toast(on ? `${u.name} is an operator` : `${u.name} is no longer an operator`);
      await load();
      // the person may be the one signed in: what they may see changes with it
      await Promise.all([refreshSession(), refreshProjects()]);
    } catch (e) {
      toast(errorText(e), "error");
    } finally {
      busy = "";
    }
  }
</script>

{#if failure}
  <ErrorPage error={failure} />
{:else}
  <div style="display:grid;gap:var(--space-4)">
    <p class="dim" style="margin:0">Everybody who has signed in. An operator manages the engines and the users and holds every project; everyone else holds what a project owner gave them. Roles on a project are managed on its Members tab.</p>
    <div class="table-wrap">
      <table class="table" data-testid="users">
        <thead><tr><th>Person</th><th>Access</th><th>First signed in</th><th></th></tr></thead>
        <tbody>
          {#each users ?? [] as u (u.id)}
            <tr data-user={u.id}>
              <td><span class="presence"><span class="avatar" data-me={u.id === me ? "" : undefined}>{initials(u.name)}</span>{u.name}</span> <span class="muted">{u.email}</span>{#if u.id === me}&nbsp;<span class="muted">you</span>{/if}</td>
              <td>{#if u.isOperator}<span class="badge" data-testid="operator-badge">operator</span>{:else}<span class="muted">roles on projects</span>{/if}</td>
              <td class="muted">{joined(u.createdAt)}</td>
              <td class="actions">
                {#if u.isOperator}
                  <button class="btn" data-variant="danger" data-size="sm" aria-label="Remove operator from {u.name}" disabled={busy === u.id || operators < 2}
                    title={operators < 2 ? "The last operator cannot be removed" : ""} onclick={() => set(u, false)}>Remove operator</button>
                {:else}
                  <button class="btn" data-size="sm" aria-label="Make {u.name} an operator" disabled={busy === u.id} onclick={() => set(u, true)}>Make operator</button>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    {#if users && operators < 2}<p class="help" style="margin:0">There is one operator, and the last one cannot be removed. Make somebody else an operator first.</p>{/if}
  </div>
{/if}
