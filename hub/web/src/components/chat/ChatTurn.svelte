<script lang="ts">
  // One turn: what started it, then what the agent did and said, in the order it happened, and a line saying how it ended. What it did
  // between two answers (its thinking and its tool calls) is one block of steps, which folds away once the turn is over.
  import { copyText } from "../../lib/clipboard";
  import { formatDuration, formatTokens, modelName, type PartView, type SteerView, type TurnView } from "../../lib/richchat";
  import type { ChatMessage } from "../../lib/types";
  import ChatPrompt from "./ChatPrompt.svelte";
  import ChatQuestion from "./ChatQuestion.svelte";
  import Markdown from "./Markdown.svelte";
  import ToolCall from "./ToolCall.svelte";

  let {
    turn,
    resolve,
    activity = "",
    now = Date.now(),
    steers = [],
    readOnly = true,
    onAnswer,
    onConfirm,
  }: {
    turn: TurnView;
    resolve: (id: string) => Promise<ChatMessage | null>;
    activity?: string;
    now?: number;
    steers?: SteerView[];
    readOnly?: boolean;
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    onAnswer?: (requestId: string, response: "accept" | "decline", answers?: Record<string, any>) => void;
    onConfirm?: (toolCallId: string, approved: boolean) => void;
  } = $props();

  type Segment = { kind: "steps"; key: string; parts: PartView[] } | { kind: "part"; key: string; part: PartView } | { kind: "steer"; key: string; text: string };
  // the parts, with the steps between two answers gathered, and where the person steered the turn
  const segments = $derived.by(() => {
    const out: Segment[] = [];
    const steerAt = (i: number) => {
      for (const st of steers) if (st.at === i) out.push({ kind: "steer", key: st.key, text: st.text });
    };
    turn.parts.forEach((p, i) => {
      steerAt(i);
      if (p.kind === "tool" || p.kind === "reasoning") {
        const last = out.at(-1);
        if (last?.kind === "steps") last.parts.push(p);
        else out.push({ kind: "steps", key: `s-${p.key}`, parts: [p] });
      } else {
        out.push({ kind: "part", key: p.key, part: p });
      }
    });
    for (const st of steers) if (st.at >= turn.parts.length) out.push({ kind: "steer", key: st.key, text: st.text });
    return out;
  });
  const running = $derived(turn.state === "running");
  const elapsed = $derived(running && turn.startedAt ? now - Date.parse(turn.startedAt) : turn.duration);
  const tokens = $derived.by(() => {
    const u = turn.usage;
    if (!u) return "";
    const parts = [];
    if (u.inputTokens !== undefined) parts.push(`${formatTokens(u.inputTokens)} in`);
    if (u.outputTokens !== undefined) parts.push(`${formatTokens(u.outputTokens)} out`);
    return parts.join(" · ");
  });
  const answer = $derived(turn.parts.filter((p) => p.kind === "markdown").map((p) => (p.kind === "markdown" ? p.content : "")).join("\n\n"));
  const lastIsSteps = $derived(segments.at(-1)?.kind === "steps");
  let copied = $state(false);
  // a block of steps is open while it is the newest thing the running turn does; a reader's choice stays
  let opened = $state<Record<string, boolean>>({});

  function summary(parts: PartView[]): string {
    const tools = parts.filter((p) => p.kind === "tool").length;
    const thoughts = parts.filter((p) => p.kind === "reasoning").length;
    const failed = parts.some((p) => p.kind === "tool" && p.tool.status === "failed");
    const words = [];
    if (thoughts) words.push("Thought");
    if (tools) words.push(`${thoughts ? "used" : "Used"} ${tools} tool${tools === 1 ? "" : "s"}`);
    return words.join(", ") + (failed ? " · one failed" : "");
  }

  async function copyAnswer() {
    if (await copyText(answer)) {
      copied = true;
      setTimeout(() => (copied = false), 1500);
    }
  }
</script>

<article class="turn" data-state={turn.state} data-turn={turn.id}>
  <ChatPrompt prompt={turn.prompt} origin={turn.origin} {resolve} />
  <div class="response">
    {#each segments as seg, i (seg.key)}
      {#if seg.kind === "steps"}
        {@const live = running && i === segments.length - 1}
        {@const asks = seg.parts.some((p) => p.kind === "tool" && p.tool.confirm)}
        {@const open = asks || (opened[seg.key] ?? live)}
        <div class="steps" data-live={live || undefined} data-open={open || undefined}>
          <button type="button" class="steps-head" aria-expanded={open} onclick={() => (opened = { ...opened, [seg.key]: !open })}>
            {#if live}<span><span class="shimmer">{activity || "Working"}…</span>{#if formatDuration(elapsed)}<span class="elapsed">{formatDuration(elapsed)}</span>{/if}</span>{:else}<span>{summary(seg.parts)}</span>{/if}
            <svg class="chev" viewBox="0 0 24 24" aria-hidden="true"><path d="M9 6l6 6-6 6" /></svg>
          </button>
          {#if open}
            <div class="steps-body">
              {#each seg.parts as p (p.key)}
                {#if p.kind === "tool"}
                  <ToolCall tool={p.tool} onConfirm={!readOnly && onConfirm ? (approved) => onConfirm(p.tool.id, approved) : undefined} />
                {:else if p.kind === "reasoning"}
                  <details class="thought" open={live && p === seg.parts.at(-1)}>
                    <summary><span class="ic" aria-hidden="true">✦</span>{live && p === seg.parts.at(-1) ? "Thinking" : "Thought"}</summary>
                    <div class="thought-text">{p.content}</div>
                  </details>
                {/if}
              {/each}
            </div>
          {/if}
        </div>
      {:else if seg.kind === "steer"}
        <div class="steer" data-testid="chat-steer"><span class="arrow" aria-hidden="true">↪</span><span class="who">You steered:</span> <span class="t">{seg.text}</span></div>
      {:else if seg.part.kind === "markdown"}
        <div class="answer"><Markdown source={seg.part.content} /></div>
      {:else if seg.part.kind === "error"}
        <div class="error" role="alert">{seg.part.message}</div>
      {:else if seg.part.kind === "notice"}
        <div class="notice"><Markdown source={seg.part.content} inline /></div>
      {:else if seg.part.kind === "question"}
        {@const q = seg.part}
        <ChatQuestion request={q.request} open={q.open} response={q.response} {readOnly} onAnswer={onAnswer ? (response, answers) => onAnswer(q.request.id, response, answers) : undefined} />
      {:else if seg.part.kind === "resource"}
        <div class="notice">{seg.part.title}</div>
      {/if}
    {/each}
    {#if running && !lastIsSteps}<div class="typing" aria-label="Working"><span></span><span></span><span></span></div>{/if}
    {#if running}<span class="sr-only">Working…</span>{/if}
  </div>
  {#if !running}
    <footer class="meta" data-testid="turn-meta">
      {#if answer}<button type="button" class="act" onclick={copyAnswer} aria-label="Copy the answer" title="Copy the answer">{#if copied}<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 12l5 5 9-10" /></svg>{:else}<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="8" y="8" width="12" height="12" rx="2" /><path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2" /></svg>{/if}</button>{/if}
      {#if turn.state === "cancelled"}<span class="end" data-end="cancelled">Stopped</span>{:else if turn.state === "error"}<span class="end" data-end="error">Failed</span>{/if}
      {#if formatDuration(elapsed)}<span>{formatDuration(elapsed)}</span>{/if}
      {#if turn.usage?.model}<span title={turn.usage.model}>{modelName(turn.usage.model)}</span>{/if}
      {#if tokens}<span title="tokens">{tokens}</span>{/if}
    </footer>
  {/if}
</article>

<style>
  .turn { display: flex; flex-direction: column; gap: 16px; }
  .response { display: flex; flex-direction: column; gap: 14px; min-width: 0; }
  .response:empty { display: none; }
  .answer { min-width: 0; }

  .steps { border: 1px solid var(--border); border-radius: 14px; background: var(--card); overflow: hidden; }
  .steps-head { width: 100%; display: flex; align-items: center; gap: 8px; font: inherit; font-size: 14px; color: var(--text-2); background: transparent; border: 0; padding: 10px 14px; cursor: pointer; text-align: left; }
  .steps-head:hover { color: var(--text); }
  .steps-head > span:first-child { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .chev { width: 16px; height: 16px; flex: none; fill: none; stroke: currentColor; stroke-width: 2; stroke-linecap: round; stroke-linejoin: round; transition: transform var(--dur-fast); }
  .steps[data-open] .chev { transform: rotate(90deg); }
  .steps-body { display: grid; gap: 2px; padding: 0 8px 8px; }
  .shimmer { background: linear-gradient(90deg, var(--text-3) 0%, var(--text) 40%, var(--text-3) 80%); background-size: 200% 100%; -webkit-background-clip: text; background-clip: text; color: transparent; animation: shimmer 1.8s linear infinite; }

  .thought { border: 0; padding: 0; display: block; }
  .thought > summary { list-style: none; display: flex; align-items: center; gap: 10px; font-size: 14px; color: var(--text-2); padding: 6px 8px; border-radius: 8px; cursor: pointer; }
  .thought > summary::-webkit-details-marker { display: none; }
  .thought > summary:hover { background: var(--surface-2); color: var(--text); }
  .thought[open] > summary { margin-bottom: 0; }
  .ic { width: 18px; text-align: center; color: var(--text-3); font-size: 12px; }
  .thought-text { margin: 2px 8px 8px 36px; font-size: 14px; line-height: 1.6; color: var(--text-2); white-space: pre-wrap; overflow-wrap: anywhere; max-height: 22em; overflow-y: auto; }

  .error { color: var(--text); background: var(--status-error-soft); border: 1px solid color-mix(in srgb, var(--status-error) 40%, transparent); padding: 10px 14px; border-radius: 12px; white-space: pre-wrap; overflow-wrap: anywhere; font-size: 15px; }
  .notice { color: var(--text-3); font-size: 14px; font-style: italic; }
  .steer { align-self: flex-end; max-width: 85%; display: flex; gap: 6px; align-items: baseline; font-size: 14px; color: var(--text-2); padding: 6px 12px; border-radius: 12px; border: 1px dashed var(--border-strong); }
  .steer .arrow { color: var(--accent-text, var(--accent)); }
  .steer .who { color: var(--text-3); flex: none; }
  .steer .t { color: var(--text); white-space: pre-wrap; overflow-wrap: anywhere; }

  .typing { display: inline-flex; gap: 5px; padding: 6px 0; }
  .typing span { width: 7px; height: 7px; border-radius: 50%; background: var(--text-3); animation: bounce 1.2s infinite ease-in-out; }
  .typing span:nth-child(2) { animation-delay: 0.15s; }
  .typing span:nth-child(3) { animation-delay: 0.3s; }

  .meta { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 14px; font-size: 13px; color: var(--text-3); min-height: 28px; }
  .meta:empty { display: none; }
  .act { width: 28px; height: 28px; margin-left: -6px; border-radius: 8px; border: 0; background: transparent; color: var(--text-3); display: grid; place-items: center; cursor: pointer; }
  .act:hover { background: var(--surface-2); color: var(--text); }
  .act svg { width: 16px; height: 16px; fill: none; stroke: currentColor; stroke-width: 1.8; stroke-linecap: round; stroke-linejoin: round; }
  .end[data-end="error"] { color: var(--status-error); }
  .elapsed { margin-left: 10px; color: var(--text-3); font-variant-numeric: tabular-nums; }
  .sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; }

  @keyframes shimmer { from { background-position: 100% 0; } to { background-position: -100% 0; } }
  @keyframes bounce { 0%, 80%, 100% { transform: translateY(0); opacity: 0.4; } 40% { transform: translateY(-4px); opacity: 1; } }
  @media (prefers-reduced-motion: reduce) { .shimmer, .typing span { animation: none !important; } .shimmer { color: var(--text-2); } }
</style>
