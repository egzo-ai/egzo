<script lang="ts">
  import { app } from "../lib/app.svelte";
  import { sourceURL } from "../lib/format";

  // AGPL-3.0 §13: people who use the hub over a network are offered its source
  let { class: cls = "" }: { class?: string } = $props();
  const version = $derived(app.account.version || "dev");
</script>

<p class="about {cls}" data-testid="about">
  <span title="hub version">egzo {version}</span> ·
  <a href="https://www.gnu.org/licenses/agpl-3.0.html" target="_blank" rel="noopener noreferrer">AGPL-3.0</a> ·
  <a href={sourceURL(version)} target="_blank" rel="noopener noreferrer">Source</a>
</p>
