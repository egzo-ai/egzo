<script lang="ts">
  // What the agent asks the person, in its turn (an AHP chat input request: OpenCode's question tool, a consent a tool needs). A single
  // choice answers with one click; otherwise each question has its control, and Submit sends them all. Once answered it says what was
  // answered. Someone who may only watch sees the question, not the controls.
  import type { QuestionRequest, QuestionView } from "../../lib/richchat";
  import Markdown from "./Markdown.svelte";

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  type Json = any;
  let {
    request,
    open,
    response = "",
    readOnly = false,
    onAnswer,
  }: { request: QuestionRequest; open: boolean; response?: string; readOnly?: boolean; onAnswer?: (response: "accept" | "decline", answers?: Record<string, Json>) => void } = $props();

  // the answer being made, by question id: a string (text, number, single choice), a boolean, or the chosen ids (multi)
  let values = $state<Record<string, string | boolean | string[]>>({});
  let free = $state<Record<string, string>>({});
  let sent = $state(false);
  $effect(() => {
    if (!open) sent = false; // answered: whatever happens next is the server's
  });

  const quick = $derived(request.questions.length === 1 && request.questions[0]!.kind === "single-select" && !request.questions[0]!.freeform);

  function initial(q: QuestionView): string | boolean | string[] {
    if (q.kind === "multi-select") return q.options.filter((o) => o.recommended).map((o) => o.id);
    if (q.kind === "boolean") return q.defaultValue === true;
    if (q.kind === "single-select") return q.options.find((o) => o.recommended)?.id ?? "";
    return q.defaultValue !== undefined ? String(q.defaultValue) : "";
  }
  function value(q: QuestionView) {
    return values[q.id] ?? initial(q);
  }

  /** The AHP answer for one question, or undefined when it has none. */
  function answerOf(q: QuestionView): Json {
    const v = value(q);
    const extra = (free[q.id] ?? "").trim();
    switch (q.kind) {
      case "text":
        return v === "" ? undefined : { state: "submitted", value: { kind: "text", value: String(v) } };
      case "number":
      case "integer":
        return v === "" || Number.isNaN(Number(v)) ? undefined : { state: "submitted", value: { kind: "number", value: Number(v) } };
      case "boolean":
        return { state: "submitted", value: { kind: "boolean", value: v === true } };
      case "single-select":
        if (v === "" && !extra) return undefined;
        return { state: "submitted", value: { kind: "selected", value: String(v), ...(extra ? { freeformValues: [extra] } : {}) } };
      case "multi-select":
        return { state: "submitted", value: { kind: "selected-many", value: v as string[], ...(extra ? { freeformValues: [extra] } : {}) } };
    }
  }

  const missing = $derived(request.questions.some((q) => q.required && answerOf(q) === undefined));

  function submit() {
    if (!onAnswer || sent || missing) return;
    const answers: Record<string, Json> = {};
    for (const q of request.questions) {
      const a = answerOf(q);
      if (a !== undefined) answers[q.id] = a;
    }
    sent = true;
    onAnswer("accept", answers);
  }
  function choose(q: QuestionView, id: string) {
    if (!onAnswer || sent) return;
    sent = true;
    onAnswer("accept", { [q.id]: { state: "submitted", value: { kind: "selected", value: id } } });
  }
  function dismiss() {
    if (!onAnswer || sent) return;
    sent = true;
    onAnswer("decline");
  }
  function toggle(q: QuestionView, id: string) {
    const now = value(q) as string[];
    values[q.id] = now.includes(id) ? now.filter((x) => x !== id) : [...now, id];
  }

  /** What was answered, in words. */
  function said(q: QuestionView): string {
    const a = request.answers[q.id];
    if (!a || a.state === "skipped") return "";
    const v = a.value ?? {};
    const label = (id: string) => q.options.find((o) => o.id === id)?.label ?? id;
    const extra: string[] = Array.isArray(v.freeformValues) ? v.freeformValues : [];
    if (v.kind === "selected") return [v.value ? label(String(v.value)) : "", ...extra].filter(Boolean).join(", ");
    if (v.kind === "selected-many") return [...(Array.isArray(v.value) ? v.value.map((x: string) => label(x)) : []), ...extra].join(", ");
    if (v.kind === "boolean") return v.value ? "Yes" : "No";
    return String(v.value ?? "");
  }
  const summary = $derived(request.questions.map(said).filter(Boolean).join(" · "));
  const title = $derived(request.message || (request.questions.length === 1 ? "" : "The agent asks"));
</script>

<div class="ask" data-testid="chat-question" data-open={open || undefined} data-response={response || undefined}>
  <div class="head">
    <span class="q" aria-hidden="true">?</span>
    <div class="what">
      {#if title}<div class="title"><Markdown source={title} inline /></div>{/if}
      {#if !open}
        <div class="done" data-testid="question-outcome">
          {#if response === "accept"}{summary ? `Answered: ${summary}` : "Answered."}{:else if response === "decline"}Dismissed.{:else}No answer: the question was closed.{/if}
        </div>
      {/if}
    </div>
  </div>

  {#if open}
    {#each request.questions as q (q.id)}
      <fieldset class="field" disabled={readOnly || sent}>
        <legend>{#if q.title && q.title !== q.message}<span class="qt">{q.title}</span>{/if}<Markdown source={q.message} inline />{#if q.required && !quick}<span class="req" aria-label="required"> *</span>{/if}</legend>
        {#if q.kind === "single-select" && quick}
          <div class="choices">
            {#each q.options as o (o.id)}
              <button type="button" class="choice" data-recommended={o.recommended || undefined} onclick={() => choose(q, o.id)} title={o.description || undefined}>{o.label}</button>
            {/each}
          </div>
        {:else if q.kind === "single-select" || q.kind === "multi-select"}
          <div class="options">
            {#each q.options as o (o.id)}
              <label class="opt">
                {#if q.kind === "single-select"}
                  <input type="radio" name="{request.id}-{q.id}" checked={value(q) === o.id} onchange={() => (values[q.id] = o.id)} />
                {:else}
                  <input type="checkbox" checked={(value(q) as string[]).includes(o.id)} onchange={() => toggle(q, o.id)} />
                {/if}
                <span>{o.label}{#if o.description}<span class="desc"> — {o.description}</span>{/if}</span>
              </label>
            {/each}
            {#if q.freeform}<input class="text" type="text" placeholder="Or type your own" bind:value={free[q.id]} />{/if}
          </div>
        {:else if q.kind === "boolean"}
          <label class="opt"><input type="checkbox" checked={value(q) === true} onchange={(e) => (values[q.id] = e.currentTarget.checked)} /><span>Yes</span></label>
        {:else}
          <input
            class="text"
            type={q.kind === "text" ? "text" : "number"}
            step={q.kind === "integer" ? "1" : "any"}
            value={value(q)}
            oninput={(e) => (values[q.id] = e.currentTarget.value)}
            onkeydown={(e) => e.key === "Enter" && submit()}
            aria-label={q.title || q.message}
          />
        {/if}
      </fieldset>
    {/each}
    {#if request.url}<p class="link"><a href={request.url} target="_blank" rel="noopener noreferrer">Open the page it asks you to visit</a></p>{/if}
    {#if readOnly}
      <p class="muted">Waiting for the person operating the agent to answer.</p>
    {:else}
      <div class="actions">
        {#if !quick && request.questions.length}<button type="button" class="primary" onclick={submit} disabled={sent || missing}>Submit</button>{/if}
        <button type="button" class="ghost" onclick={dismiss} disabled={sent}>Dismiss</button>
        {#if sent}<span class="muted">Sending…</span>{/if}
      </div>
    {/if}
  {/if}
</div>

<style>
  .ask { border: 1px solid color-mix(in srgb, var(--status-waiting) 45%, transparent); background: var(--status-waiting-soft); border-radius: 14px; padding: 12px 14px; display: grid; gap: 12px; font-size: 15px; }
  .ask:not([data-open]) { background: var(--card); border-color: var(--border); padding: 10px 14px; gap: 0; }
  .head { display: flex; gap: 10px; align-items: baseline; }
  .q { color: var(--status-waiting); font-weight: 800; flex: none; }
  .ask:not([data-open]) .q { color: var(--text-3); }
  .what { min-width: 0; display: grid; gap: 2px; }
  .title { font-weight: 600; color: var(--text); }
  .done { color: var(--text-2); font-size: 14px; }
  .field { border: 0; padding: 0; margin: 0; display: grid; gap: 8px; min-width: 0; }
  .field legend { padding: 0; margin-bottom: 8px; color: var(--text); }
  .qt { font-weight: 600; margin-right: 6px; }
  .req { color: var(--status-error); }
  .choices { display: flex; flex-wrap: wrap; gap: 8px; }
  .choice { font: inherit; font-size: 14px; padding: 8px 14px; border-radius: 999px; border: 1px solid var(--border-strong); background: var(--card); color: var(--text); cursor: pointer; }
  .choice:hover:not(:disabled) { border-color: var(--accent); }
  .choice[data-recommended] { border-color: var(--accent); }
  .choice:first-child { background: var(--accent); border-color: var(--accent); color: var(--accent-contrast, #fff); }
  .choice:disabled { opacity: 0.6; cursor: default; }
  .options { display: grid; gap: 6px; }
  .opt { display: flex; gap: 8px; align-items: baseline; cursor: pointer; }
  .desc { color: var(--text-3); }
  .text { font: inherit; font-size: 15px; padding: 8px 10px; border-radius: 10px; border: 1px solid var(--border-strong); background: var(--card); color: var(--text); min-width: 0; width: 100%; box-sizing: border-box; }
  .actions { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .primary, .ghost { font: inherit; font-size: 14px; padding: 7px 14px; border-radius: 10px; cursor: pointer; border: 1px solid var(--border-strong); background: var(--card); color: var(--text); }
  .primary { background: var(--accent); border-color: var(--accent); color: var(--accent-contrast, #fff); }
  .primary:disabled, .ghost:disabled { opacity: 0.55; cursor: default; }
  .muted { color: var(--text-3); font-size: 14px; margin: 0; }
  .link { margin: 0; font-size: 14px; }
</style>
