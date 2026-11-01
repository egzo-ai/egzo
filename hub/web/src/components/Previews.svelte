<script lang="ts">
  import { copyText } from "../lib/clipboard";
  import { toast } from "../lib/app.svelte";
  import type { Preview } from "../lib/types";

  let { agent, previews = [], heading = true }: { agent: string; previews?: Preview[]; heading?: boolean } = $props();

  async function copy(text: string) {
    toast((await copyText(text)) ? "Copied" : "Copy it by hand: the browser refused", "ok");
  }
</script>

{#if previews.length}
  <section class="panel" data-testid="previews" data-agent={agent} aria-label="Previews of {agent}">
    {#if heading}<header><h2 class="h-section">Previews</h2></header>{/if}
    <div class="pad">
      <ul class="plain">
        {#each previews as p (p.port)}
          <li data-port={p.port}>
            <b>{p.name || p.port}</b> <span class="muted">{p.port}/{p.protocol}</span>
            {#if p.address}
              <a class="btn" data-size="sm" data-external target="_blank" rel="noopener noreferrer" href={p.address}>Open</a>
              <button class="btn" data-size="sm" onclick={() => copy(p.address)}>Copy link</button>
            {:else}
              <code>{p.command}</code> <button class="btn" data-size="sm" aria-label="Copy the command" onclick={() => copy(p.command)}>Copy</button>
            {/if}
          </li>
        {/each}
      </ul>
    </div>
  </section>
{/if}
