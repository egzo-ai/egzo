<script lang="ts">
  import { workspace } from "../lib/workspace.svelte";
  import TermPane from "./TermPane.svelte";

  let { hidden }: { hidden: boolean } = $props();
  const compact = $derived(workspace.layout === 4);
</script>

<!-- Every open terminal stays mounted here; panes that are not in a slot are hidden, never destroyed. -->
<div class="termview" data-layout={workspace.layout} {hidden}>
  {#each workspace.terms as t (t.id)}
    <TermPane term={t} slot={workspace.panes.indexOf(t.id)} {compact} />
  {/each}
</div>

<style>
  .termview[hidden] { display: none; }
</style>
