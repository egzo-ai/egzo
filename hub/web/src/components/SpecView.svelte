<script lang="ts">
  let { text }: { text: string } = $props();
  const lines = $derived(text.replace(/\n$/, "").split("\n"));
</script>

<pre class="spec" data-testid="text">{#each lines as line}<span class="l">{line}</span>{/each}</pre>
