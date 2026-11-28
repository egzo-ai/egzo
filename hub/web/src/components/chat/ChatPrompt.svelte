<script lang="ts">
  // What started a turn. A prompt someone typed shows as they typed it. egzo's line announcing a message ("check egzo message m… and handle
  // the request for me.") shows the message it stands for, from the hub, when the hub has it: who sent it and what it says.
  import { announced, announcement, shortId } from "../../lib/richchat";
  import type { ChatMessage } from "../../lib/types";
  import Markdown from "./Markdown.svelte";

  let { prompt, origin, resolve, pending = false }: { prompt: string; origin: string; resolve: (id: string) => Promise<ChatMessage | null>; pending?: boolean } = $props();

  const ids = $derived(announced(prompt));
  const said = $derived(announcement(prompt) ?? []);
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

{#snippet line(sentences: { text: string; ids: string[] }[])}
  <div class="prompt egzo" data-testid="prompt-egzo" data-pending={pending || undefined} title={prompt}>
    <span class="from">egzo</span>
    {#each sentences as a, i (i)}
      <span class="said">{a.text}{#each a.ids as id (id)}<code class="id" title={id}>{shortId(id)}</code>{/each}</span>
    {/each}
  </div>
{/snippet}

{#if ids && shown.length}
  {#each shown as m (m.id)}
    <div class="prompt message" data-testid="prompt-message" data-pending={pending || undefined} data-from-agent={m.fromAgent || undefined}>
      <div class="who">
        {#if m.fromAgent}<span class="tag">agent</span>{/if}<span class="from">{m.from}</span>
        <span class="muted">{kinds[m.kind] ?? "says"}{#if m.re}<span title={m.re}> on {shortId(m.re)}</span>{/if}, through egzo</span>
        <code class="id" title={m.id}>{shortId(m.id)}</code>
      </div>
      <Markdown source={m.text} />
    </div>
  {/each}
  {#if shown.length < ids.length}{@render line(said.filter((a) => a.ids.some((id) => !shown.some((m) => m.id === id))))}{/if}
{:else if ids}
  {@render line(said)}
{:else}
  <div class="prompt" data-origin={origin} data-testid="prompt-text" data-pending={pending || undefined}>{prompt}</div>
{/if}

<style>
  .prompt { align-self: flex-end; max-width: min(80%, 40rem); background: var(--bubble); color: var(--text); padding: 10px 16px; border-radius: 20px 20px 6px 20px; white-space: pre-wrap; overflow-wrap: anywhere; line-height: 1.55; font-size: 16px; }
  .prompt[data-pending] { opacity: 0.55; }
  .prompt[data-origin="agent"], .prompt[data-origin="tool"], .prompt[data-origin="automation"], .prompt[data-origin="systemNotification"] { background: var(--surface-2); color: var(--text-2); }
  .message { white-space: normal; display: grid; gap: 4px; }
  .who { font-size: 13px; color: var(--text-2); }
  .from { font-weight: 600; color: var(--text); }
  .egzo { background: transparent; border: 1px dashed var(--border-strong); color: var(--text-3); font-size: 14px; border-radius: 14px; }
  .egzo .from { color: var(--text-2); margin-right: 6px; }
  .egzo .said + .said::before { content: " "; }
  .id { font-family: var(--mono, ui-monospace, monospace); font-size: 12px; margin-left: 6px; padding: 1px 6px; border-radius: 6px; background: var(--surface-2); color: var(--text-2); }
  .muted { color: var(--text-3); }
  .tag { font-size: 11px; text-transform: uppercase; letter-spacing: 0.04em; padding: 1px 6px; margin-right: 6px; border-radius: 6px; background: var(--surface-2); color: var(--text-2); }
  .message[data-from-agent] { background: var(--surface-2); }
  @media (max-width: 720px) { .prompt { max-width: 88%; } }
</style>
