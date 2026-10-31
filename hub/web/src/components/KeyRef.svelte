<script lang="ts">
  import { app, setSingleKeys } from "../lib/app.svelte";
  import { LEADER_KEYS } from "../lib/leader";
  import Modal from "./Modal.svelte";

  const pages: [string, string][] = [
    [": or Ctrl+K", "command menu"], ["/", "filter the list"], ["j k", "move between rows"], ["Enter", "open the row"], ["a", "attach to the agent in the row"],
    ["s", "spawn an agent"], ["g p", "go to the projects"], ["g l", "go to the links (operator)"], ["g u", "go to the users (operator)"], ["?", "this reference"],
  ];
</script>

<Modal title="Keyboard shortcuts" onclose={() => (app.keyref = false)}>
  <p class="muted" style="margin:0">Inside a terminal every key belongs to the agent, except the leader <span class="kbd">Ctrl</span> <span class="kbd">]</span> and the key after it. The browser keeps a few chords for itself (Ctrl+W, Ctrl+T, Ctrl+N): a page cannot take them, so they act on the browser tab and never reach the agent.</p>
  <h3 class="h-section">Terminals: after the leader</h3>
  <dl class="roles">
    {#each LEADER_KEYS as [key, text]}<dt><span class="kbd">{key}</span></dt><dd>{text}</dd>{/each}
  </dl>
  <h3 class="h-section">Pages</h3>
  <dl class="roles">
    {#each pages as [key, text]}<dt><span class="kbd">{key}</span></dt><dd>{text}</dd>{/each}
  </dl>
  <label style="display:flex;gap:8px;align-items:center">
    <input type="checkbox" checked={app.singleKeys} onchange={(e) => setSingleKeys(e.currentTarget.checked)} /> Single-key shortcuts on pages
  </label>
  {#snippet footer()}<button class="btn" data-autofocus onclick={() => (app.keyref = false)}>Close</button>{/snippet}
</Modal>
