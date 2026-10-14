<script lang="ts">
  let { name, size }: { name: string; size?: "sm" } = $props();
</script>

<svg class="icon" aria-hidden="true" data-size={size}><use href="#i-{name}" /></svg>
