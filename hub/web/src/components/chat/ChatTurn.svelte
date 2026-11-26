<script lang="ts">
  // One turn: what started it, then what the agent did and said, in the order it happened, and a line saying how it ended.
  import { formatDuration, formatTokens, modelName, type TurnView } from "../../lib/richchat";
  import type { ChatMessage } from "../../lib/types";
  import ChatPrompt from "./ChatPrompt.svelte";
  import Markdown from "./Markdown.svelte";
  import ToolCall from "./ToolCall.svelte";

  let { turn, resolve, activity = "", now = Date.now() }: { turn: TurnView; resolve: (id: string) => Promise<ChatMessage | null>; activity?: string; now?: number } = $props();

  const elapsed = $derived(turn.state === "running" && turn.startedAt ? now - Date.parse(turn.startedAt) : turn.duration);
  const tokens = $derived.by(() => {
    const u = turn.usage;
    if (!u) return "";
    const parts = [];
    if (u.inputTokens !== undefined) parts.push(`${formatTokens(u.inputTokens)} in`);
    if (u.outputTokens !== undefined) parts.push(`${formatTokens(u.outputTokens)} out`);
    return parts.join(" · ");
  });
  const ended: Record<string, string> = { complete: "", cancelled: "Stopped", error: "Failed" };
  const lastReasoning = $derived(turn.parts.map((p) => p.kind).lastIndexOf("reasoning"));
  const lastIsReasoning = $derived(turn.parts.at(-1)?.kind === "reasoning");
</script>

<article class="turn" data-state={turn.state} data-turn={turn.id}>
  <ChatPrompt prompt={turn.prompt} origin={turn.origin} {resolve} />
  <div class="response">
    {#each turn.parts as p, i (p.key)}
      {#if p.kind === "markdown"}
        <div class="answer"><Markdown source={p.content} /></div>
      {:else if p.kind === "reasoning"}
        <details class="reasoning" open={turn.state === "running" && i === lastReasoning && lastIsReasoning}>
          <summary>{turn.state === "running" && i === lastReasoning && lastIsReasoning ? "Thinking…" : "Thought"}</summary>
          <div class="thought">{p.content}</div>
        </details>
      {:else if p.kind === "tool"}
        <ToolCall tool={p.tool} />
      {:else if p.kind === "error"}
        <div class="error" role="alert">{p.message}</div>
      {:else if p.kind === "notice"}
        <div class="notice"><Markdown source={p.content} inline /></div>
      {:else if p.kind === "question"}
        <div class="question" data-open={p.open || undefined}><span class="q" aria-hidden="true">?</span> <Markdown source={p.message} inline />{#if p.open}<span class="muted"> · answer it in the agent's terminal</span>{/if}</div>
      {:else if p.kind === "resource"}
        <div class="notice">{p.title}</div>
      {/if}
    {/each}
  </div>
  <footer class="meta" data-testid="turn-meta">
    {#if turn.state === "running"}
      <span class="working"><span class="spinner" aria-hidden="true"></span>{activity || "Working"}…</span>
    {:else if ended[turn.state]}
      <span class="end" data-end={turn.state}>{ended[turn.state]}</span>
    {/if}
    {#if formatDuration(elapsed)}<span>{formatDuration(elapsed)}</span>{/if}
    {#if turn.usage?.model}<span title={turn.usage.model}>{modelName(turn.usage.model)}</span>{/if}
    {#if tokens}<span title="tokens">{tokens}</span>{/if}
  </footer>
</article>

<style>
  .turn { display: flex; flex-direction: column; gap: var(--space-3); }
  .response { display: flex; flex-direction: column; gap: var(--space-2); min-width: 0; }
  .response:empty { display: none; }
  .answer { min-width: 0; }
  .reasoning { border: 0; padding: 0; display: block; color: var(--text-3); font-size: var(--text-xs); }
  .reasoning > summary { color: var(--text-3); font-size: var(--text-xs); width: fit-content; }
  .reasoning[open] > summary { margin-bottom: var(--space-1); }
  .thought { white-space: pre-wrap; overflow-wrap: anywhere; padding-left: var(--space-3); border-left: 2px solid var(--border); line-height: 1.5; max-height: 20em; overflow-y: auto; }
  .error { color: var(--status-error); background: var(--status-error-soft); border-left: 2px solid var(--status-error); padding: var(--space-2) var(--space-3); border-radius: var(--radius-1); white-space: pre-wrap; overflow-wrap: anywhere; }
  .notice { color: var(--text-3); font-size: var(--text-xs); font-style: italic; }
  .question { font-size: var(--text-xs); color: var(--text-2); }
  .question .q { color: var(--status-waiting); font-weight: 800; }
  .meta { display: flex; flex-wrap: wrap; gap: 0 var(--space-3); font-size: var(--text-2xs); color: var(--text-3); min-height: 1em; }
  .meta:empty { display: none; }
  .end[data-end="error"] { color: var(--status-error); }
  .working { color: var(--text-2); display: inline-flex; align-items: center; gap: 6px; }
  .spinner { width: 8px; height: 8px; border-radius: 50%; background: var(--accent); animation: pulse 0.9s infinite alternate; }
  @media (prefers-reduced-motion: reduce) { .spinner { animation: none; } }
</style>
