<script lang="ts">
  import { api } from "../lib/api";
  import { app, errorText, refreshProjects, toast } from "../lib/app.svelte";
  import { ask } from "../lib/confirm.svelte";
  import InviteLink from "./InviteLink.svelte";
  import InviteTable from "./InviteTable.svelte";
  import PersonCell from "./PersonCell.svelte";
  import type { Invite, Members, NewInvite, Project, Role } from "../lib/types";

  let { project, data, invites, reload }: { project: Project; data: Members; invites: Invite[]; reload: () => Promise<void> } = $props();

  let chosen = $state<Record<string, Role>>({});
  let addUser = $state("");
  let addRole = $state<Role>("user");
  let ownerUser = $state("");
  let selfRole = $state<Role>("admin");
  let inviteRole = $state<Role>("user");
  /** The invite that was just made: this is the only time its link is seen. */
  let made = $state<NewInvite | null>(null);

  const operator = $derived(!!app.user?.isOperator);
  /** An owner lowers anyone but themselves; an operator takes back what they gave themselves, and a project keeps its last owner. */
  const editable = (userId: string) => project.canManage && (userId !== data.me || operator) || operator && userId === data.me;

  async function apply(user: string, name: string, role: Role | "none", note: string) {
    try {
      await api.setRole(project.id, user, role);
      toast(note);
      await Promise.all([reload(), refreshProjects()]);
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
    const ok = await ask({ title: `Remove ${name}?`, lines: [`${name} loses every role on ${project.name} and no longer sees it.`, "Their agents keep running."], confirm: "Remove from the project" });
    if (ok) await apply(userId, name, "none", "Roles updated");
  }

  async function add(e: Event) {
    e.preventDefault();
    if (!addUser) return;
    await apply(addUser, "", addRole, "Roles updated");
    addUser = "";
  }

  async function giveOwner(e: Event) {
    e.preventDefault();
    if (!ownerUser) return;
    await apply(ownerUser, "", "owner", "The project has an owner");
    ownerUser = "";
  }

  async function grantMyself(e: Event) {
    e.preventDefault();
    const ok = await ask({
      title: `Give yourself ${selfRole} on ${project.name}?`,
      lines: ["It goes in the project's audit log, and you are listed here like any member until you remove it."],
      confirm: "Give me the role", tone: "normal",
    });
    if (ok) await apply(data.me, "", selfRole, "Your role is set");
  }

  async function invite(e: Event) {
    e.preventDefault();
    try {
      made = await api.createProjectInvite(project.id, inviteRole);
      await reload();
    } catch (e) {
      toast(errorText(e), "error");
    }
  }

  async function revoke(i: Invite) {
    try {
      await api.revokeInvite(i.id);
      if (made?.id === i.id) made = null;
      await reload();
    } catch (e) {
      toast(errorText(e), "error");
    }
  }
</script>

<div class="table-wrap">
  <table class="table" data-testid="members">
    <thead><tr><th>Person</th><th>Role</th><th></th></tr></thead>
    <tbody>
      {#each data.members as m (m.user.id)}
        {@const me = m.user.id === data.me}
        <tr data-user={m.user.id} data-dim={m.user.disabled ? "" : undefined}>
          <td><PersonCell user={m.user} {me} />{#if m.user.disabled} <span class="pill" data-s="warn">disabled</span>{/if}</td>
          <td>
            <span class="btn-group">
              <select class="select" style="width:auto" aria-label="Role of {m.user.username}" disabled={!editable(m.user.id)} value={chosen[m.user.id] ?? m.role} onchange={(e) => (chosen[m.user.id] = e.currentTarget.value as Role)}>
                {#each data.roles as r}<option value={r}>{r}</option>{/each}
              </select>
              {#if editable(m.user.id)}<button class="btn" data-size="sm" disabled={(chosen[m.user.id] ?? m.role) === m.role} onclick={() => change(m.user.id, m.user.username, m.role)}>Change</button>{/if}
            </span>
          </td>
          <td class="actions">{#if editable(m.user.id)}<button class="btn" data-variant="danger" data-size="sm" aria-label="Remove {m.user.username}" onclick={() => remove(m.user.id, m.user.username)}>Remove</button>{:else if me}<span class="muted">you</span>{/if}</td>
        </tr>
      {:else}
        <tr><td colspan="3" class="muted">Nobody holds a role on this project.</td></tr>
      {/each}
    </tbody>
  </table>
</div>

{#if project.ownerless}
  <div class="banner" data-tone="warn" role="status" data-testid="ownerless"><span class="tag">! No owner</span>
    <div class="body">Nobody who can sign in owns this project. Give it an owner, or revoke it under Settings, Links.</div></div>
{/if}

{#if operator && !project.canManage}
  <section class="panel" aria-labelledby="go-further">
    <header><h2 class="h-section" id="go-further">Go further</h2></header>
    <div class="pad" style="display:grid;gap:var(--space-3)">
      <p class="muted" style="margin:0">As an operator you see this project and its agents. A terminal, its logs and its network take a role, which you give yourself here, on the record.</p>
      <form class="toolbar" onsubmit={grantMyself}>
        <div class="field"><label for="m-self">Your role</label>
          <select id="m-self" class="select" bind:value={selfRole}>{#each data.roles as r}<option value={r}>{r}</option>{/each}</select></div>
        <button class="btn" type="submit" style="align-self:end">Give me the role</button>
      </form>
      {#if project.ownerless && data.candidates.length}
        <form class="toolbar" onsubmit={giveOwner}>
          <div class="field"><label for="m-owner">New owner</label>
            <select id="m-owner" class="select" bind:value={ownerUser} required>
              <option value="" disabled>Choose…</option>
              {#each data.candidates as c}<option value={c.id}>{c.username}</option>{/each}
            </select></div>
          <button class="btn" data-variant="primary" type="submit" style="align-self:end">Make owner</button>
        </form>
      {/if}
    </div>
  </section>
{/if}

{#if project.canManage}
<section class="panel" aria-labelledby="invite-person">
  <header><h2 class="h-section" id="invite-person">Invite a person</h2></header>
  <div class="pad" style="display:grid;gap:var(--space-3)">
    <p class="muted" style="margin:0">A link for somebody who may not have an account here yet: they make one, or sign in, and get the role.</p>
    <form class="toolbar" onsubmit={invite}>
      <div class="field"><label for="m-invite">Invite as</label>
        <select id="m-invite" class="select" bind:value={inviteRole}>{#each data.roles as r}<option value={r}>{r}</option>{/each}</select></div>
      <button class="btn" data-variant="primary" type="submit" style="align-self:end">Make an invite</button>
    </form>
    {#if made}<InviteLink invite={made} />{/if}
    {#if invites.length}<InviteTable {invites} onrevoke={revoke} />{/if}
  </div>
</section>

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
      <p class="muted" style="margin:0">Everybody on this hub already has a role here. Invite whoever is not on it yet.</p>
    {/if}
    <dl class="roles">
      <dt>viewer</dt><dd>opens the previews of its agents from their links, and sees nothing else of the project.</dd>
      <dt>user</dt><dd>spawns agents, and starts, stops, attaches to and removes their own.</dd>
      <dt>admin</dt><dd>the same on every agent of the project, those of the CLI included, with the audit log.</dd>
      <dt>owner</dt><dd>an admin who also manages these roles and can unlink the project.</dd>
    </dl>
  </div>
</section>
{/if}
