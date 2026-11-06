<script lang="ts">
  // An agent's messaging status, as counts: what waits to be announced to it, and what was announced and never fetched (stuck).
  // Never a message's text: the hub is not a chat, people answer in the terminal or through a bridge.
  let { queued = 0, unconfirmed = 0 }: { queued?: number; unconfirmed?: number } = $props();
</script>

{#if unconfirmed}<span class="pill" data-s="error" data-testid="stuck" title="announced to the agent three times and never fetched: attach to its terminal to see why, or run egzo message ls">{unconfirmed} stuck</span>{/if}
{#if queued}<span class="pill" data-s="neutral" data-testid="queued" title="messages waiting to be announced: they are when the agent is idle and nobody is typing in its terminal">{queued} queued</span>{/if}
