<script lang="ts">
  // What an agent sent and was sent, as the hub keeps it: its record of who asked what. A row says when, with whom and which way, the
  // kind, the subject and where a request stands; it opens on the message's text.
  import { ago, stamp } from "../lib/format";
  import type { ChatMessage } from "../lib/types";
  import Pill from "./Pill.svelte";

  let { agent, messages }: { agent: string; messages: ChatMessage[] } = $props();

  let opened = $state<Record<string, boolean>>({});
  const kinds: Record<string, string> = { request: "request", question: "question", resolution: "reply", update: "update" };
  // the message went out from the agent when the agent sent it
  const out = (m: ChatMessage) => m.fromAgent && m.from === agent;
  const asks = (m: ChatMessage) => m.kind === "request" || m.kind === "question";
</script>

<div class="table-wrap">
  <table class="table" data-testid="messages">
    <thead><tr><th>When</th><th>With</th><th>Kind</th><th>Subject</th><th>State</th></tr></thead>
    <tbody>
      {#each messages as m (m.id)}
        <tr data-message={m.id} data-out={out(m) || undefined}>
          <td title={stamp(m.time)}>{ago(m.time)}</td>
          <td class="with">
            <span class="dir" title={out(m) ? "sent" : "received"} aria-label={out(m) ? "sent to" : "received from"}>{out(m) ? "→" : "←"}</span>
            {out(m) ? m.to : m.from}{#if out(m) ? m.toAgent : m.fromAgent}<span class="muted ag">agent</span>{/if}
          </td>
          <td>{kinds[m.kind] ?? m.kind}</td>
          <td class="wrap">
            <button type="button" class="subject" aria-expanded={!!opened[m.id]} onclick={() => (opened[m.id] = !opened[m.id])}>{m.subject || "(no subject)"}</button>
            <code class="id" title={m.id}>{m.id.slice(0, 9)}</code>
          </td>
          <td>
            {#if asks(m)}{#if m.resolved}<Pill kind={m.outcome === "done" ? "done" : m.outcome === "declined" ? "warn" : "error"} label={m.outcome || "resolved"} />{:else}<Pill kind="waiting" label="open" />{/if}
            {:else if m.kind === "resolution" && m.outcome}<span class="muted">{m.outcome}</span>{/if}
          </td>
        </tr>
        {#if opened[m.id]}
          <tr class="text-row" data-testid="message-text"><td colspan="5"><pre class="text">{m.text}</pre>{#if m.choices?.length}<div class="muted">Choices: {m.choices.join(" · ")}</div>{/if}</td></tr>
        {/if}
      {:else}
        <tr><td colspan="5" class="muted">No message sent or received yet.</td></tr>
      {/each}
    </tbody>
  </table>
</div>

<style>
  .with { white-space: nowrap; }
  .dir { display: inline-block; width: 1.2em; color: var(--text-3); }
  .ag { margin-left: var(--space-1); font-size: var(--text-xs); }
  tr[data-out] .dir { color: var(--accent-text); }
  .subject { font: inherit; color: var(--text); background: none; border: 0; padding: 0; cursor: pointer; text-align: left; }
  .subject:hover { color: var(--accent-text); text-decoration: underline; }
  .id { margin-left: var(--space-2); font-size: var(--text-2xs); color: var(--text-3); }
  .text-row td { background: var(--surface-1); }
  .text { margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; font-size: var(--text-sm); }
</style>
