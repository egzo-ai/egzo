<script lang="ts">
  // The Rich UI: the chat of an agent spawned with it, live from its harness through the project's link. What is typed here is a prompt in
  // the agent's main session, as if typed in its terminal; while a turn runs, it steers that turn or waits for the next one; Stop cancels
  // the turn. Someone who may only watch sees the chat and no box. A dropped connection comes back on its own: AHP's reconnect replays what
  // was missed, from the last serverSeq seen.
  import type { ActionEnvelope, Snapshot } from "@microsoft/agent-host-protocol";
  import { api, chatURL } from "../lib/api";
  import { apply, emptyChat, messages, modelName, newTurnId, pendingOf, ROOT, running, titleOf, turnsOf, withSnapshot, type Chat } from "../lib/richchat";
  import type { ChatMessage } from "../lib/types";
  import ChatPrompt from "./chat/ChatPrompt.svelte";
  import ChatTurn from "./chat/ChatTurn.svelte";

  let { project, agent }: { project: string; agent: string } = $props();

  let chat = $state<Chat>(emptyChat());
  let readOnly = $state(true);
  let status = $state<"connecting" | "waiting" | "live" | "reconnecting">("connecting");
  let failure = $state("");
  let notice = $state("");
  let draft = $state("");
  /** What this page sent that the agent has not taken yet: shown, faded, until it does. */
  let outgoing = $state<{ id: string; text: string; kind: "prompt" | "steering" | "queued" }[]>([]);
  let scroller: HTMLElement | undefined = $state();
  let box: HTMLTextAreaElement | undefined = $state();
  let stuck = $state(true); // the view follows the newest turn while the reader is at the bottom
  let now = $state(Date.now());

  // one id for this page, kept across reconnects: the server knows what it sent it
  const clientId = `egzo-hub-${newTurnId()}`;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  type Json = any;

  // ─── The connection ─────────────────────────────────────────────────────────────────────────────────────────────────────────────

  let socket: WebSocket | null = null;
  let alive = true;
  let attempts = 0;
  let retry: ReturnType<typeof setTimeout> | undefined;

  function send(message: object) {
    if (socket?.readyState === WebSocket.OPEN) socket.send(JSON.stringify(message));
  }

  function connect() {
    clearTimeout(retry);
    const ws = new WebSocket(chatURL(project, agent));
    socket = ws;
    let next = 1;
    const waiting = new Map<number, (m: Json) => void>();
    // what this connection did: initialize or reconnect first, then the main session's channels
    let started = false;
    let subscribing = false;
    const request = (m: object): Promise<Json> => {
      const id = next++;
      if (socket === ws) send({ jsonrpc: "2.0", id, ...m });
      return new Promise((resolve) => waiting.set(id, resolve));
    };
    const mine = () => socket === ws;

    async function subscribeAll() {
      if (subscribing || !chat.channel) return;
      subscribing = true;
      try {
        // the server may not know the main session yet (opencode-ahp registers a session the TUI made 250 ms after it was made): listing
        // the sessions registers it, and a refused subscribe is asked again a little later
        for (let refused = 0; mine(); refused++) {
          await request(messages.listSessions());
          const r = await request(messages.subscribe(chat.channel));
          if (!mine()) return;
          if (r.result?.snapshot) {
            chat = withSnapshot(chat, r.result.snapshot as Snapshot);
            status = "live";
            attempts = 0;
            break;
          }
          if (refused >= 10) {
            failure = String(r.error?.message ?? "The agent's chat cannot be opened.");
            return;
          }
          await new Promise((res) => setTimeout(res, 300 * (refused + 1)));
        }
        const s = await request(messages.subscribe(chat.session));
        if (mine() && s.result?.snapshot) chat = withSnapshot(chat, s.result.snapshot as Snapshot);
      } finally {
        subscribing = false;
      }
    }

    async function initialize() {
      started = true;
      const r = await request(messages.initialize(clientId));
      if (!mine()) return;
      if (r.error) {
        failure = String(r.error.message ?? "The agent refused the chat.");
        return;
      }
      if (chat.channel) await subscribeAll();
      else status = "waiting";
    }

    async function resume() {
      started = true;
      const r = await request(messages.reconnect(clientId, chat.seq, [ROOT, chat.session, chat.channel]));
      if (!mine()) return;
      const result = r.result;
      if (!result) return initialize();
      let have = false;
      if (result.type === "replay") {
        for (const envelope of result.actions ?? []) handleAction(envelope);
        have = !(result.missing ?? []).includes(chat.channel);
      } else {
        for (const s of result.snapshots ?? []) {
          chat = withSnapshot(chat, s as Snapshot);
          have ||= s.resource === chat.channel;
        }
      }
      if (have) {
        status = "live";
        attempts = 0;
      } else {
        await subscribeAll();
      }
    }

    function handle(m: Json) {
      if (m.id !== undefined && m.method === undefined) {
        waiting.get(m.id)?.(m);
        waiting.delete(m.id);
        return;
      }
      switch (m.method) {
        case "x-egzo/chatOpened":
          readOnly = !!m.params?.readOnly;
          failure = "";
          // a chat this page had can be resumed where it was: once egzo says which session is the main one, the link lets the reconnect through
          if (!(chat.state && chat.channel)) void initialize();
          return;
        case "x-egzo/chatError":
          failure = String(m.params?.message ?? "The chat failed.");
          return;
        case "x-egzo/mainSession": {
          const session = String(m.params?.session ?? "");
          const channel = String(m.params?.chat ?? "");
          if (!channel) return;
          if (channel !== chat.channel) {
            chat = emptyChat(session, channel);
            if (!started) void initialize();
            else void subscribeAll();
          } else if (!started) {
            void resume();
          } else if (!chat.state) {
            void subscribeAll();
          }
          return;
        }
        case "action":
          handleAction(m.params as ActionEnvelope);
          return;
      }
    }

    ws.onmessage = (e) => {
      if (!mine()) return;
      try {
        handle(JSON.parse(e.data));
      } catch {
        /* not ours */
      }
    };
    ws.onclose = () => {
      if (!mine() || !alive) return;
      socket = null;
      for (const resolve of waiting.values()) resolve({ error: { message: "closed" } });
      status = "reconnecting";
      const delay = Math.min(15000, 500 * 2 ** attempts++);
      retry = setTimeout(() => alive && connect(), delay);
    };
  }

  function handleAction(envelope: ActionEnvelope) {
    const action = envelope.action as Json;
    const id = action?.turnId ?? action?.id;
    if (envelope.channel === chat.channel && id !== undefined && outgoing.some((o) => o.id === id)) {
      outgoing = outgoing.filter((o) => o.id !== id);
      if (envelope.rejectionReason !== undefined) notice = `The agent did not take it: ${envelope.rejectionReason}`;
    }
    chat = apply(chat, envelope);
  }

  /** Back now rather than at the end of the wait: the page is shown again, or the network came back. */
  function soon() {
    if (status === "reconnecting" && alive) {
      attempts = 0;
      connect();
    }
  }

  $effect(() => {
    void project;
    void agent;
    alive = true;
    chat = emptyChat();
    status = "connecting";
    failure = "";
    connect();
    const visible = () => document.visibilityState === "visible" && soon();
    document.addEventListener("visibilitychange", visible);
    window.addEventListener("online", soon);
    return () => {
      alive = false;
      clearTimeout(retry);
      document.removeEventListener("visibilitychange", visible);
      window.removeEventListener("online", soon);
      const ws = socket;
      socket = null;
      ws?.close();
    };
  });

  // ─── The messages egzo's line announces ─────────────────────────────────────────────────────────────────────────────────────────

  const found = new Map<string, Promise<ChatMessage | null>>();
  function resolve(id: string): Promise<ChatMessage | null> {
    let p = found.get(id);
    if (!p) {
      p = api.chatMessage(project, agent, id).catch(() => null);
      found.set(id, p);
    }
    return p;
  }

  // ─── The view ───────────────────────────────────────────────────────────────────────────────────────────────────────────────────

  const turns = $derived(turnsOf(chat.state));
  const turn = $derived(running(chat));
  const pending = $derived(pendingOf(chat.state));
  const title = $derived(titleOf(chat));
  const model = $derived(modelName([...turns].reverse().find((t) => t.usage?.model)?.usage?.model));
  const canSend = $derived(!!chat.channel && status === "live");

  // the clock of the turn that runs
  $effect(() => {
    if (!turn) return;
    now = Date.now();
    const t = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(t);
  });

  function onScroll() {
    if (scroller) stuck = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 80;
  }
  function toBottom() {
    scroller?.scrollTo({ top: scroller.scrollHeight });
    stuck = true;
  }
  // the newest turn stays in view while the agent writes, unless the reader scrolled up
  $effect(() => {
    void chat.state;
    void outgoing.length;
    if (stuck) queueMicrotask(() => scroller?.scrollTo({ top: scroller.scrollHeight }));
  });

  function submit(kind: "prompt" | "steering" | "queued") {
    const text = draft.trim();
    if (!text || !canSend) return;
    const id = newTurnId();
    if (kind === "prompt" && !turn) send(messages.prompt(chat.channel, text, id));
    else send(messages.pending(chat.channel, kind === "queued" ? "queued" : "steering", id, text));
    outgoing = [...outgoing, { id, text, kind: kind === "prompt" && turn ? "steering" : kind }];
    notice = "";
    draft = "";
    toBottom();
  }

  function stop() {
    if (turn) send(messages.cancel(chat.channel, turn.id));
  }

  function keydown(e: KeyboardEvent) {
    if (e.key !== "Enter" || e.shiftKey || e.isComposing) return;
    e.preventDefault();
    submit(turn && e.altKey ? "queued" : turn ? "steering" : "prompt");
  }

  const live = $derived(status === "live" ? (turn ? "working" : "idle") : status === "reconnecting" ? "unreachable" : "starting");
  const liveWord = $derived(status === "live" ? (turn ? "working" : "ready") : status === "reconnecting" ? "reconnecting" : status === "waiting" ? "waiting" : "connecting");
</script>

<section class="chat" data-testid="rich-chat" aria-label="Chat with {agent}">
  <header class="head">
    <span class="title" data-testid="chat-title">{title || agent}</span>
    {#if model}<span class="muted model" data-testid="chat-model">{model}</span>{/if}
    <span class="pill" data-s={live} data-testid="chat-status">{liveWord}</span>
  </header>

  <div class="scroll" bind:this={scroller} onscroll={onScroll}>
    <div class="turns" aria-live="polite">
      {#if status === "connecting"}
        <p class="muted empty">Connecting to the agent…</p>
      {:else if status === "waiting" && !turns.length}
        <p class="muted empty">The agent has no conversation yet: it starts with its first prompt.</p>
      {:else if chat.state && !turns.length && !outgoing.length}
        <p class="muted empty">Nothing said yet.</p>
      {/if}
      {#each turns as t (t.id)}
        <ChatTurn turn={t} {resolve} {now} activity={t.state === "running" ? (chat.state?.activity ?? "") : ""} />
      {/each}
      {#each outgoing.filter((o) => o.kind === "prompt") as o (o.id)}
        <ChatPrompt prompt={o.text} origin="user" {resolve} pending />
      {/each}
    </div>
  </div>
  {#if !stuck}<button type="button" class="btn jump" data-size="sm" onclick={toBottom}>↓ Latest</button>{/if}

  {#if failure}<div class="banner" role="alert"><div class="body">{failure}</div></div>{/if}
  {#if status === "reconnecting"}<div class="banner" data-tone="unreachable" role="status" data-testid="chat-reconnecting"><div class="body">The connection dropped. Reconnecting…</div><button class="btn" data-size="sm" onclick={soon}>Retry now</button></div>{/if}
  {#if notice}<div class="banner" data-tone="warn" role="status"><div class="body">{notice}</div><button class="btn" data-size="sm" data-variant="quiet" onclick={() => (notice = "")}>Dismiss</button></div>{/if}

  {#if pending.length || outgoing.some((o) => o.kind !== "prompt")}
    <ul class="pending" data-testid="chat-pending" aria-label="Messages waiting">
      {#each pending as p (p.id)}
        <li data-kind={p.kind}><span class="k">{p.kind === "steering" ? "steering" : "next"}</span> <span class="t">{p.text}</span></li>
      {/each}
      {#each outgoing.filter((o) => o.kind !== "prompt") as o (o.id)}
        <li data-kind={o.kind} data-pending><span class="k">{o.kind === "steering" ? "steering" : "next"}</span> <span class="t">{o.text}</span></li>
      {/each}
    </ul>
  {/if}

  {#if readOnly}
    <p class="muted foot" data-testid="chat-readonly">You watch this chat: sending a prompt takes the right to operate the agent.</p>
  {:else}
    <form class="compose" onsubmit={(e) => { e.preventDefault(); submit(turn ? "steering" : "prompt"); }}>
      <textarea
        class="textarea"
        rows="1"
        bind:this={box}
        bind:value={draft}
        onkeydown={keydown}
        disabled={!canSend}
        aria-label="Prompt"
        placeholder={!chat.channel ? "Waiting for the agent's first session…" : turn ? "Guide the running turn, or queue what comes next…" : "Ask the agent…"}
      ></textarea>
      <div class="actions">
        <span class="hint muted">{turn ? "Enter steers · Alt+Enter queues" : "Enter sends · Shift+Enter: new line"}</span>
        {#if turn}
          <button type="button" class="btn" data-variant="danger" onclick={stop}>Stop</button>
          <button type="button" class="btn" disabled={!draft.trim() || !canSend} onclick={() => submit("queued")}>Queue</button>
          <button type="submit" class="btn" data-variant="primary" disabled={!draft.trim() || !canSend}>Steer</button>
        {:else}
          <button type="submit" class="btn" data-variant="primary" disabled={!draft.trim() || !canSend}>Send</button>
        {/if}
      </div>
    </form>
  {/if}
</section>

<style>
  .chat { position: relative; display: flex; flex-direction: column; gap: var(--space-2); height: max(440px, calc(100dvh - 250px)); min-height: 0; }
  .head { display: flex; align-items: center; gap: var(--space-3); min-width: 0; padding-bottom: var(--space-2); border-bottom: 1px solid var(--border); }
  .title { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-family: var(--font-display); font-size: var(--text-sm); }
  .model { font-size: var(--text-2xs); white-space: nowrap; }
  .scroll { flex: 1; min-height: 0; overflow-y: auto; overscroll-behavior: contain; scrollbar-width: thin; }
  .turns { display: flex; flex-direction: column; gap: var(--space-6); max-width: 92ch; margin: 0 auto; padding: var(--space-3) var(--space-1) var(--space-4); }
  .empty { text-align: center; margin: var(--space-6) 0; }
  .jump { position: absolute; left: 50%; transform: translateX(-50%); bottom: 120px; box-shadow: var(--shadow-float); z-index: 1; }
  .pending { list-style: none; margin: 0; padding: 0; display: grid; gap: 2px; max-width: 92ch; width: 100%; margin-inline: auto; }
  .pending li { display: flex; gap: var(--space-2); font-size: var(--text-xs); color: var(--text-2); min-width: 0; }
  .pending li[data-pending] { opacity: 0.6; }
  .pending .k { flex: none; font-size: var(--text-2xs); text-transform: uppercase; letter-spacing: var(--tracking-label); color: var(--accent-text); padding-top: 1px; }
  .pending li[data-kind="queued"] .k { color: var(--text-3); }
  .pending .t { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .compose { display: flex; flex-direction: column; gap: var(--space-2); max-width: 92ch; width: 100%; margin-inline: auto; }
  .compose .textarea { min-height: 44px; max-height: 40vh; field-sizing: content; resize: none; line-height: 1.5; }
  .compose .actions { display: flex; align-items: center; justify-content: flex-end; gap: var(--space-2); flex-wrap: wrap; }
  .hint { flex: 1; font-size: var(--text-2xs); }
  .foot { margin: 0; text-align: center; }
  @media (max-width: 720px) {
    .chat { height: calc(100dvh - 200px); }
    .hint { display: none; }
    .turns { gap: var(--space-5); }
  }
</style>
