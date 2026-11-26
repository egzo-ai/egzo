<script lang="ts">
  // What started a turn. A prompt someone typed shows as they typed it. egzo's line announcing a message ("check egzo message m… and handle
  // the request for me.") shows the message it stands for, from the hub, when the hub has it: who sent it and what it says.
  import { announced } from "../../lib/richchat";
  import type { ChatMessage } from "../../lib/types";
  import Markdown from "./Markdown.svelte";

  let { prompt, origin, resolve, pending = false }: { prompt: string; origin: string; resolve: (id: string) => Promise<ChatMessage | null>; pending?: boolean } = $props();

  const ids = $derived(announced(prompt));
  let found = $state<(ChatMessage | null)[] | null>(null);

  $effect(() => {
    const list = ids;
    found = null;
    if (!list) return;
    let live = true;
    Promise.all(list.map(resolve)).then((r) => live && (found = r));
    return () => (live = false);
  });

  const kinds: Record<string, string> = { request: "asks", question: "asks", resolution: "answers", update: "says" };
  const shown = $derived(found?.filter((m): m is ChatMessage => m !== null) ?? []);
</script>

{#if ids && shown.length}
  {#each shown as m (m.id)}
    <div class="prompt message" data-testid="prompt-message" data-pending={pending || undefined}>
      <div class="who"><span class="from">{m.from}</span> <span class="muted">{kinds[m.kind] ?? "says"}, through egzo</span></div>
      <Markdown source={m.text} />
    </div>
  {/each}
  {#if shown.length < ids.length}<div class="prompt egzo" title={prompt}><span class="from">egzo</span> {prompt}</div>{/if}
{:else if ids}
  <div class="prompt egzo" data-testid="prompt-egzo" data-pending={pending || undefined}><span class="from">egzo</span> {prompt}</div>
{:else}
  <div class="prompt" data-origin={origin} data-testid="prompt-text" data-pending={pending || undefined}>{prompt}</div>
{/if}

<style>
  .prompt { align-self: flex-end; max-width: min(88%, 72ch); background: var(--surface-3); color: var(--text); padding: var(--space-2) var(--space-3); border-radius: var(--radius-3) var(--radius-3) var(--radius-1) var(--radius-3); white-space: pre-wrap; overflow-wrap: anywhere; line-height: 1.5; }
  .prompt[data-pending] { opacity: 0.6; }
  .prompt[data-origin="agent"], .prompt[data-origin="tool"], .prompt[data-origin="automation"], .prompt[data-origin="systemNotification"] { background: var(--surface-2); color: var(--text-2); }
  .message { white-space: normal; display: grid; gap: var(--space-1); }
  .who { font-size: var(--text-2xs); }
  .from { font-weight: var(--weight-bold); color: var(--text); }
  .egzo { background: transparent; border: 1px dashed var(--border-strong); color: var(--text-3); font-size: var(--text-xs); }
  .egzo .from { color: var(--accent-text); margin-right: var(--space-1); }
</style>
