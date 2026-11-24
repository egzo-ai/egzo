<script lang="ts">
  // The Rich UI: the chat of an agent spawned with it, live from its harness through the project's link. What is typed here is a prompt in
  // the agent's main session, as if typed in its terminal; Stop cancels the turn that runs. Someone who may only watch sees the chat and no box.
  import { chatURL } from "../lib/api";
  import { apply, emptyChat, fromSnapshot, messages, running, type Chat } from "../lib/richchat";

  let { project, agent }: { project: string; agent: string } = $props();

  let chat = $state<Chat>(emptyChat());
  let readOnly = $state(true);
  let status = $state<"connecting" | "waiting" | "live" | "closed">("connecting");
  let failure = $state("");
  let draft = $state("");
  let socket: WebSocket | null = null;
  let scroller: HTMLElement | undefined = $state();

  function send(message: object) {
    if (socket?.readyState === WebSocket.OPEN) socket.send(JSON.stringify(message));
  }

  function handle(m: any) {
    switch (m.method) {
      case "x-egzo/chatOpened":
        readOnly = !!m.params?.readOnly;
        send(messages.initialize());
        return;
      case "x-egzo/chatError":
        failure = String(m.params?.message ?? "The chat failed.");
        return;
      case "x-egzo/mainSession":
        if (m.params?.chat && m.params.chat !== chat.channel) {
          chat = { ...emptyChat(), channel: m.params.chat };
          send(messages.subscribe(m.params.chat));
        }
        return;
      case "action":
        if (m.params?.channel === chat.channel) chat = apply(chat, m.params.action);
        return;
    }
    if (m.id === 1 && m.result) status = chat.channel ? "live" : "waiting";
    if (m.id === 2 && m.result?.snapshot) {
      chat = fromSnapshot(chat.channel, m.result.snapshot.state);
      status = "live";
    }
    if (m.error && m.id !== undefined) failure = String(m.error.message ?? "refused");
  }

  $effect(() => {
    const ws = new WebSocket(chatURL(project, agent));
    socket = ws;
    status = "connecting";
    failure = "";
    chat = emptyChat();
    ws.onmessage = (e) => {
      try {
        handle(JSON.parse(e.data));
      } catch {
        /* not ours */
      }
    };
    ws.onclose = () => {
      if (socket === ws) status = "closed";
    };
    return () => {
      socket = null;
      ws.close();
    };
  });

  // the newest turn stays in view while the agent writes
  $effect(() => {
    void chat.turns.length;
    void chat.turns.at(-1)?.parts.length;
    queueMicrotask(() => scroller?.scrollTo({ top: scroller.scrollHeight }));
  });

  const turn = $derived(running(chat));

  function submit(e: Event) {
    e.preventDefault();
    const text = draft.trim();
    if (!text || !chat.channel) return;
    send(messages.prompt(chat.channel, text, crypto.randomUUID()));
    draft = "";
  }

  function stop() {
    if (turn) send(messages.cancel(chat.channel, turn.id));
  }

  function keydown(e: KeyboardEvent) {
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) submit(e);
  }
</script>

<section class="chat" data-testid="rich-chat" aria-label="Chat with {agent}">
  <div class="turns" bind:this={scroller} aria-live="polite">
    {#if status === "connecting"}
      <p class="muted">Connecting to the agent…</p>
    {:else if status === "waiting" && !chat.turns.length}
      <p class="muted">The agent has no conversation yet: it starts with its first prompt.</p>
    {:else if status === "live" && !chat.turns.length}
      <p class="muted">Nothing said yet.</p>
    {/if}
    {#each chat.turns as t (t.id)}
      <article class="turn" data-state={t.state}>
        <div class="prompt">{t.prompt}</div>
        {#each t.parts as p, i (i)}
          {#if p.kind === "markdown"}
            <div class="answer">{p.content}</div>
          {:else if p.kind === "reasoning"}
            <details class="reasoning"><summary>Thinking</summary><div>{p.content}</div></details>
          {:else if p.kind === "tool"}
            <div class="tool" data-status={p.tool.status}><span class="dot" aria-hidden="true"></span><span class="title">{p.tool.title || p.tool.name}</span>{#if p.tool.status === "failed"}<span class="tag">failed</span>{:else if p.tool.status === "cancelled"}<span class="tag">cancelled</span>{/if}</div>
          {:else if p.kind === "error"}
            <div class="error" role="alert">{p.content}</div>
          {/if}
        {/each}
        {#if t.state === "running"}<div class="working muted">Working…</div>{:else if t.state === "cancelled"}<div class="muted">Stopped.</div>{/if}
      </article>
    {/each}
  </div>
  {#if failure}<div class="banner" role="alert"><div class="body">{failure}</div></div>{/if}
  {#if status === "closed"}<div class="banner" data-tone="info" role="status"><div class="body">The chat is closed: the agent stopped, or the connection ended. Reload to reconnect.</div></div>{/if}
  {#if readOnly}
    <p class="muted foot" data-testid="chat-readonly">You watch this chat: sending a prompt takes the right to operate the agent.</p>
  {:else}
    <form class="compose" onsubmit={submit}>
      <textarea class="textarea" rows="2" placeholder={chat.channel ? "Ask the agent…" : "Waiting for the agent's first session…"} bind:value={draft} onkeydown={keydown} disabled={!chat.channel || status === "closed"} aria-label="Prompt"></textarea>
      <div class="actions">
        {#if turn}<button type="button" class="btn" data-variant="danger" onclick={stop}>Stop</button>{/if}
        <button type="submit" class="btn" data-variant="primary" disabled={!draft.trim() || !chat.channel}>Send</button>
      </div>
    </form>
  {/if}
</section>

<style>
  .chat { display: flex; flex-direction: column; gap: var(--space-2, 8px); min-height: 60vh; max-height: 78vh; }
  .turns { flex: 1; overflow-y: auto; display: flex; flex-direction: column; gap: var(--space-3, 12px); padding-block: var(--space-2, 8px); }
  .turn { display: flex; flex-direction: column; gap: 6px; }
  .prompt { align-self: flex-end; max-width: min(90%, 60ch); background: var(--surface-3); color: var(--text); padding: 8px 12px; border-radius: 10px; white-space: pre-wrap; overflow-wrap: anywhere; }
  .answer { max-width: 100%; white-space: pre-wrap; overflow-wrap: anywhere; line-height: 1.5; }
  .reasoning { color: var(--text-3); font-size: 0.92em; }
  .reasoning div { white-space: pre-wrap; overflow-wrap: anywhere; padding-left: 12px; border-left: 2px solid var(--border); }
  .tool { display: flex; align-items: center; gap: 8px; font-family: var(--font-mono, monospace); font-size: 0.88em; color: var(--text-2); overflow-wrap: anywhere; }
  .tool .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--status-running); flex: none; }
  .tool[data-status="running"] .dot { animation: pulse 1s infinite alternate; }
  .tool[data-status="failed"] .dot { background: var(--status-error); }
  .tool[data-status="cancelled"] .dot { background: var(--status-stopped); }
  .tool .tag { color: var(--text-3); }
  .error { color: var(--status-error); white-space: pre-wrap; }
  .working { font-size: 0.9em; }
  .compose { display: flex; flex-direction: column; gap: 6px; }
  .compose .actions { display: flex; justify-content: flex-end; gap: 8px; }
  .foot { margin: 0; }
  @keyframes pulse { from { opacity: 0.35; } to { opacity: 1; } }
  @media (prefers-reduced-motion: reduce) { .tool[data-status="running"] .dot { animation: none; } }
</style>
