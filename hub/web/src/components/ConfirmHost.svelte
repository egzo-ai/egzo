<script lang="ts">
  import { answer, confirmation } from "../lib/confirm.svelte";
  import Modal from "./Modal.svelte";

  let typed = $state("");
  const req = $derived(confirmation.request);
  $effect(() => {
    if (req) typed = "";
  });
  const ready = $derived(!req?.typed || typed === req.typed);
</script>

{#if req}
  <Modal title={req.title} tone={req.tone === "normal" ? undefined : "danger"} onclose={() => answer(false)}>
    <ul class="consequence">
      {#each req.lines as line}<li>{line}</li>{/each}
    </ul>
    {#if req.typed}
      <div class="field">
        <label for="confirm-typed">Type <code>{req.typed}</code> to confirm</label>
        <input id="confirm-typed" class="input" bind:value={typed} autocomplete="off" />
      </div>
    {/if}
    {#snippet footer()}
      <button class="btn" data-autofocus onclick={() => answer(false)}>Cancel</button>
      <button class="btn" data-variant={req.tone === "normal" ? "primary" : "danger-solid"} disabled={!ready} onclick={() => answer(true)}>{req.confirm}</button>
    {/snippet}
  </Modal>
{/if}
