<script lang="ts">
  import { api } from "../lib/api";
  import { errorText, toast } from "../lib/app.svelte";
  import { ask } from "../lib/confirm.svelte";
  import PersonCell from "./PersonCell.svelte";
  import type { Members, Role } from "../lib/types";

  let { engine, project, data, reload }: { engine: string; project: string; data: Members; reload: () => Promise<void> } = $props();

  let chosen = $state<Record<string, Role>>({});
  let addUser = $state("");
  let addRole = $state<Role>("user");

  async function apply(user: string, name: string, role: Role | "none", note: string) {
    try {
      await api.setRole(engine, project, user, role);
      toast(note);
      await reload();
    } catch (e) {
      toast(errorText(e), "error");
    }
  }

  async function change(userId: string, name: string, current: Role) {
    const role = chosen[userId] ?? current;
    if (role === current) return;
    const ok = await ask({ title: `Change the role of ${name}?`, lines: [`${name} goes from ${current} to ${role}.`], confirm: "Change role", tone: "normal" });
    if (ok) await apply(userId, name, role, "Roles updated");
  }

  async function remove(userId: string, name: string) {
    const ok = await ask({ title: `Remove ${name}?`, lines: [`${name} loses every role on ${project} and no longer sees it.`, "Their agents keep running."], confirm: "Remove from the project" });
    if (ok) await apply(userId, name, "none", "Roles updated");
  }

  async function add(e: Event) {
    e.preventDefault();
    if (!addUser) return;
    await apply(addUser, "", addRole, "Roles updated");
    addUser = "";
  }
</script>

<div class="table-wrap">
  <table class="table" data-testid="members">
    <thead><tr><th>Person</th><th>Role</th><th></th></tr></thead>
    <tbody>
      {#each data.members as m (m.user.id)}
        {@const me = m.user.id === data.me}
        <tr data-user={m.user.id} data-dim={m.operator ? "" : undefined} data-operator={m.operator ? "" : undefined}>
          <td><PersonCell user={m.user} {me} /></td>
          {#if m.operator}
            <td><span class="badge" title="An operator holds every project. This is not a role: change it under Settings, Users.">operator</span></td>
            <td class="actions"><span class="muted">{me ? "you · " : ""}holds every project</span></td>
          {:else}
          <td>
            <span class="btn-group">
              <select class="select" style="width:auto" aria-label="Role of {m.user.username}" disabled={me} value={chosen[m.user.id] ?? m.role} onchange={(e) => (chosen[m.user.id] = e.currentTarget.value as Role)}>
                {#each data.roles as r}<option value={r}>{r}</option>{/each}
              </select>
              {#if !me}<button class="btn" data-size="sm" disabled={(chosen[m.user.id] ?? m.role) === m.role} onclick={() => change(m.user.id, m.user.username, m.role)}>Change</button>{/if}
            </span>
          </td>
          <td class="actions">{#if !me}<button class="btn" data-variant="danger" data-size="sm" aria-label="Remove {m.user.username}" onclick={() => remove(m.user.id, m.user.username)}>Remove</button>{:else}<span class="muted">you</span>{/if}</td>
          {/if}
        </tr>
      {/each}
    </tbody>
  </table>
</div>

<section class="panel" aria-labelledby="add-person">
  <header><h2 class="h-section" id="add-person">Add a person</h2></header>
  <div class="pad">
    {#if data.candidates.length}
      <form class="toolbar" onsubmit={add}>
        <div class="field"><label for="m-person">Person</label>
          <select id="m-person" class="select" bind:value={addUser} required>
            <option value="" disabled>Choose…</option>
            {#each data.candidates as c}<option value={c.id}>{c.username}{c.name && c.name !== c.username ? ` · ${c.name}` : ""}{c.email ? ` (${c.email})` : ""}</option>{/each}
          </select></div>
        <div class="field"><label for="m-role">Role</label>
          <select id="m-role" class="select" bind:value={addRole}>{#each data.roles as r}<option value={r}>{r}</option>{/each}</select></div>
        <button class="btn" data-variant="primary" type="submit" style="align-self:end">Add</button>
      </form>
    {:else}
      <p class="muted" style="margin:0">Everybody who has signed in already has a role here. A person appears in this list once they have signed in to the hub.</p>
    {/if}
    <dl class="roles">
      <dt>user</dt><dd>spawns agents, and starts, stops, attaches to and removes their own.</dd>
      <dt>administrator</dt><dd>the same on every agent of the project.</dd>
      <dt>owner</dt><dd>an administrator who also manages these roles.</dd>
    </dl>
  </div>
</section>
