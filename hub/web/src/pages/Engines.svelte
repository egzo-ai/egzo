<script lang="ts">
  import { api } from "../lib/api";
  import { app, errorText, refreshProjects, refreshSession, toast } from "../lib/app.svelte";
  import { ask } from "../lib/confirm.svelte";
  import { poll } from "../lib/live";
  import type { EngineState, PendingHostKey } from "../lib/types";
  import ErrorPage from "./ErrorPage.svelte";
  import Modal from "../components/Modal.svelte";

  let engines = $state<EngineState[] | null>(null);
  let failure = $state<unknown>(null);
  let formError = $state("");
  let busy = $state(false);
  let pending = $state<PendingHostKey | null>(null);
  let form = $state({ name: "", host: "", sshUser: "", ca: "", cert: "", key: "", sshKey: "" });

  $effect(() => poll(async () => {
    try {
      engines = await api.engines();
    } catch (e) {
      failure = e;
    }
  }));

  function clear() {
    form = { name: "", host: "", sshUser: "", ca: "", cert: "", key: "", sshKey: "" };
  }

  async function afterSave(name: string) {
    toast(`Saved engine ${name}`);
    clear();
    pending = null;
    engines = await api.engines();
    await Promise.all([refreshSession(), refreshProjects()]);
  }

  async function save(e: Event) {
    e.preventDefault();
    busy = true;
    formError = "";
    try {
      const answer = await api.saveEngine({ ...form });
      if ("pending" in answer) {
        pending = answer.pending;
        form.sshKey = "";
      } else await afterSave(answer.saved);
    } catch (err) {
      formError = errorText(err);
    } finally {
      busy = false;
    }
  }

  async function pin() {
    if (!pending) return;
    busy = true;
    try {
      const answer = await api.confirmEngine(pending.token);
      if ("saved" in answer) await afterSave(answer.saved);
    } catch (err) {
      pending = null;
      formError = errorText(err);
    } finally {
      busy = false;
    }
  }

  async function forget(name: string) {
    const ok = await ask({
      title: `Forget engine ${name}?`,
      lines: ["The hub stops managing it and drops the roles on its projects.", "Nothing on the engine is touched."],
      confirm: "Forget engine", typed: name,
    });
    if (!ok) return;
    try {
      await api.removeEngine(name);
      toast(`Removed engine ${name}`);
      engines = await api.engines();
      await Promise.all([refreshSession(), refreshProjects()]);
    } catch (err) {
      toast(errorText(err), "error");
    }
  }
</script>

{#if failure}
  <ErrorPage error={failure} />
{:else}
  <div class="page">
    <div class="phead"><h1 class="h-page">Engines</h1><span class="muted">{engines?.length ?? ""}</span></div>
    <p class="dim" style="margin:0">The container engines the hub manages. Credentials are encrypted, never shown again, and left unchanged when a field is blank.</p>

    <div class="table-wrap">
      <table class="table" data-testid="engines">
        <thead><tr><th>Name</th><th>Address</th><th>State</th><th></th></tr></thead>
        <tbody>
          {#each engines ?? [] as e (e.name)}
            <tr>
              <td>{e.name}</td><td><code>{e.host}</code></td>
              <td>{#if e.up}<span class="pill" data-s="running">reachable</span>{:else}<span class="pill" data-s="unreachable">unreachable</span> <span class="muted">{e.err}</span>{/if}</td>
              <td class="actions"><button class="btn" data-variant="danger" data-size="sm" aria-label="Remove {e.name}" onclick={() => forget(e.name)}>Remove</button></td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>

    <section class="drawer" aria-labelledby="add-engine">
      <header><h2 id="add-engine">Add or change an engine</h2></header>
      <form onsubmit={save}>
        <div class="content">
          {#if formError}<div class="banner" role="alert"><span class="tag">✕ Not saved</span><div class="body">{formError}</div></div>{/if}
          <div class="grid2">
            <div class="field"><label for="e-name">Name</label>
              <input id="e-name" class="input" bind:value={form.name} required pattern="[a-z0-9][a-z0-9_\-]*" maxlength="31" placeholder="lab" />
              <span class="help">Lowercase letters, digits, - and _. Saving an existing name changes that engine.</span></div>
            <div class="field"><label for="e-host">Address</label>
              <input id="e-host" class="input" bind:value={form.host} required placeholder="unix:///var/run/docker.sock · tcp://host:2376 · ssh://user@host" /></div>
          </div>
          <details>
            <summary>TLS (tcp://)</summary>
            <div class="field"><label for="e-ca">CA certificate (PEM)</label><textarea id="e-ca" class="textarea" rows="3" autocomplete="off" bind:value={form.ca}></textarea></div>
            <div class="field"><label for="e-cert">Client certificate (PEM)</label><textarea id="e-cert" class="textarea" rows="3" autocomplete="off" bind:value={form.cert}></textarea></div>
            <div class="field"><label for="e-key">Client key (PEM)</label><textarea id="e-key" class="textarea" rows="3" autocomplete="off" bind:value={form.key}></textarea></div>
          </details>
          <details>
            <summary>ssh (ssh://)</summary>
            <div class="field"><label for="e-sshuser">User, when not in the address</label><input id="e-sshuser" class="input" bind:value={form.sshUser} /></div>
            <div class="field"><label for="e-sshkey">Private key (PEM, no passphrase)</label><textarea id="e-sshkey" class="textarea" rows="3" autocomplete="off" bind:value={form.sshKey}></textarea></div>
          </details>
        </div>
        <footer><button class="btn" data-variant="primary" type="submit" disabled={busy}>Test and save</button></footer>
      </form>
    </section>
  </div>
{/if}

{#if pending}
  <Modal title="Confirm the host key" onclose={() => (pending = null)}>
    <p style="margin:0">The server <code>{pending.host}</code> presented this ssh host key. If it is the one you expect, pin it: a different key will be refused from now on.</p>
    <div class="fp" data-testid="fingerprint">{pending.fingerprint}</div>
    <p class="help" style="margin:0">Compare it with <code>ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub</code> on that machine.</p>
    {#snippet footer()}
      <button class="btn" onclick={() => (pending = null)}>Cancel</button>
      <button class="btn" data-variant="primary" data-autofocus disabled={busy} onclick={pin}>Pin this key and save</button>
    {/snippet}
  </Modal>
{/if}
