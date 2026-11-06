<script lang="ts">
  // An agent's messaging status, as counts: what waits to be delivered to it, and what was announced and never picked up (stuck).
  // Never a message's text: the hub is not a chat, people answer in the terminal or through a bridge.
  import Icon from "./Icon.svelte";

  let { queued = 0, unconfirmed = 0 }: { queued?: number; unconfirmed?: number } = $props();

  const messages = (n: number) => (n === 1 ? "1 message" : `${n} messages`);
  const stuckTitle = $derived(
    `${unconfirmed === 1 ? "A message was" : `${unconfirmed} messages were`} announced to this agent three times and never picked up. ` +
      "egzo announces again in 5 minutes, 15 minutes, then every hour. Open its terminal to see what it is doing.",
  );
  const queuedTitle = $derived(
    `${queued === 1 ? "A message is" : `${queued} messages are`} waiting to be delivered. egzo announces them when the agent is idle and ` +
      "nobody is typing in its terminal, or when it starts again if it is stopped.",
  );
</script>

{#if unconfirmed}<span class="pill" data-s="error" data-testid="stuck" title={stuckTitle}><Icon name="mail" size="sm" /> {messages(unconfirmed)} stuck</span>{/if}
{#if queued}<span class="pill" data-s="neutral" data-testid="queued" title={queuedTitle}><Icon name="mail" size="sm" /> {messages(queued)} queued</span>{/if}
