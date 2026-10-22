<script lang="ts">
  import { api } from "../lib/api";
  import { errorText, refreshProjects, refreshSession, toast } from "../lib/app.svelte";
  import { ask } from "../lib/confirm.svelte";
  import { poll } from "../lib/live";
  import type { Identity, UserRow } from "../lib/types";
  import PersonCell from "../components/PersonCell.svelte";
  import ErrorPage from "./ErrorPage.svelte";

  let users = $state<UserRow[] | null>(null);
  let me = $state("");
  let failure = $state<unknown>(null);
  let busy = $state("");
  let showDisabled = $state(false);

  const operators = $derived((users ?? []).filter((u) => u.isOperator).length);
  const disabled = $derived((users ?? []).filter((u) => u.disabled).length);
  const shown = $derived((users ?? []).filter((u) => showDisabled || !u.disabled));

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

  const day = (iso: string) => new Date(iso).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
  const where = (i: Identity) => (i.current ? "" : " (not the current provider)");

  async function run(u: UserRow, action: () => Promise<unknown>, done: string) {
    busy = u.id;
    try {
      await action();
      toast(done);
      await load();
      // the person may be the one signed in: what they may see changes with it
      await Promise.all([refreshSession(), refreshProjects()]);
    } catch (e) {
      toast(errorText(e), "error");
    } finally {
      busy = "";
    }
  }

  async function setOperator(u: UserRow, on: boolean) {
    const ok = await ask(
      on
        ? {
            title: `Make ${u.username} an operator?`,
            lines: [
              `${u.username} will manage the engines and the users, and hold every project on every engine: other people's agents and terminals included.`,
              "Everything they do stays in the audit log under their name.",
            ],
            confirm: "Make operator", tone: "normal",
          }
        : {
            title: `Remove operator from ${u.username}?`,
            lines: [`${u.username} stops managing the engines and the users, and stops holding every project.`, "They keep any role they were given on a project, and lose the rest."],
            confirm: "Remove operator",
          },
    );
    if (ok) await run(u, () => api.setOperator(u.id, on), on ? `${u.username} is an operator` : `${u.username} is no longer an operator`);
  }

  async function setDisabled(u: UserRow, on: boolean) {
    const ok = await ask(
      on
        ? {
            title: `Disable ${u.username}?`,
            lines: [
              `${u.username} is signed out, their open terminals close, and they cannot sign in again.`,
              "Their roles stay, unused, and come back if they are enabled. Their agents keep running.",
            ],
            confirm: "Disable",
          }
        : { title: `Enable ${u.username}?`, lines: [`${u.username} can sign in again, with the roles they had.`], confirm: "Enable", tone: "normal" },
    );
    if (ok) await run(u, () => api.setDisabled(u.id, on), on ? `${u.username} is disabled` : `${u.username} is enabled`);
  }

  async function removeIdentity(u: UserRow, i: Identity) {
    const ok = await ask({
      title: `Remove ${i.email || i.issuer} from ${u.username}?`,
      lines: ["The account at the provider is freed: somebody who signs in with it can then link it to their own user."],
      confirm: "Remove account",
    });
    if (ok) await run(u, () => api.removeIdentity(u.id, i.id), "Account removed");
  }
</script>

{#if failure}
  <ErrorPage error={failure} />
{:else}
  <div style="display:grid;gap:var(--space-4)">
    <p class="dim" style="margin:0">Everybody who has signed in. An operator manages the engines and the users and holds every project; everyone else holds what a project owner gave them. Roles on a project are managed on its Members tab.</p>
    {#if disabled}
      <label class="check"><input type="checkbox" bind:checked={showDisabled} /> Show disabled users ({disabled})</label>
    {/if}
    <div class="table-wrap">
      <table class="table" data-testid="users">
        <thead><tr><th>Person</th><th>Signs in with</th><th>Last signed in</th><th>Access</th><th></th></tr></thead>
        <tbody>
          {#each shown as u (u.id)}
            <tr data-user={u.id} data-dim={u.disabled ? "" : undefined}>
              <td><PersonCell user={u} me={u.id === me} />{#if u.id === me}&nbsp;<span class="muted">you</span>{/if}</td>
              <td>
                {#if u.hasPassword}<div>password</div>{/if}
                {#each u.identities as i (i.id)}
                  <div class={i.current ? "" : "muted"}>{i.email || i.name || "an account"} at {i.issuer}{where(i)}
                    {#if u.disabled}<button class="btn" data-size="sm" aria-label="Remove {i.email || i.issuer} from {u.username}" disabled={busy === u.id} onclick={() => removeIdentity(u, i)}>Remove</button>{/if}
                  </div>
                {/each}
              </td>
              <td class="muted">{u.lastLoginAt ? day(u.lastLoginAt) : "never"}</td>
              <td>{#if u.isOperator}<span class="badge" data-testid="operator-badge">operator</span>{:else}<span class="muted">roles on projects</span>{/if}</td>
              <td class="actions">
                {#if u.isOperator}
                  <button class="btn" data-variant="danger" data-size="sm" aria-label="Remove operator from {u.username}" disabled={busy === u.id || operators < 2}
                    title={operators < 2 ? "The last operator cannot be removed" : ""} onclick={() => setOperator(u, false)}>Remove operator</button>
                {:else if u.disabled}
                  <button class="btn" data-size="sm" aria-label="Enable {u.username}" disabled={busy === u.id} onclick={() => setDisabled(u, false)}>Enable</button>
                {:else}
                  <span class="btn-group">
                    <button class="btn" data-size="sm" aria-label="Make {u.username} an operator" disabled={busy === u.id} onclick={() => setOperator(u, true)}>Make operator</button>
                    <button class="btn" data-variant="danger" data-size="sm" aria-label="Disable {u.username}" disabled={busy === u.id} onclick={() => setDisabled(u, true)}>Disable</button>
                  </span>
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
