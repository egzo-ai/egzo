<script lang="ts">
  import { app } from "../lib/app.svelte";

  let { name }: { name: string } = $props();
  const down = $derived(app.engines.some((e) => e.name === name && !e.up) || app.unreachable.some((e) => e.name === name));
</script>

<span class="engine" data-down={down ? "" : undefined} title="engine {name}{down ? ' (unreachable)' : ''}">{name}</span>
