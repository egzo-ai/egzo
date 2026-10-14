<script lang="ts">
  let { kind, label, testid }: { kind: string; label: string; testid?: string } = $props();
</script>

<span class="pill" data-s={kind} data-testid={testid}>{label}</span>
