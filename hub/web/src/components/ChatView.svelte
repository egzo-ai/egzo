<script lang="ts">
  // The Rich UI: the chat of an agent spawned with it, live from its harness through the project's link. What is typed here is a prompt in
  // the agent's main session, as if typed in its terminal; while a turn runs, it steers that turn or waits for the next one; Stop cancels
  // the turn. Someone who may only watch sees the chat and no box. A dropped connection comes back on its own: AHP's reconnect replays what
  // was missed, from the last serverSeq seen.
  import type { ActionEnvelope, Snapshot } from "@microsoft/agent-host-protocol";
  import { api, chatURL } from "../lib/api";
  import { apply, emptyChat, messages, modelName, newTurnId, pendingOf, ROOT, running, titleOf, turnsOf, withSnapshot, type Chat } from "../lib/richchat";
  import type { ChatMessage } from "../lib/types";
  import "@fontsource-variable/inter/wght.css";
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

  // the box grows with what is typed, up to a third of the screen
  $effect(() => {
    void draft;
    if (!box) return;
    box.style.height = "auto";
    box.style.height = `${Math.min(box.scrollHeight, Math.round(window.innerHeight * 0.33))}px`;
  });

  function keydown(e: KeyboardEvent) {
    if (e.key !== "Enter" || e.shiftKey || e.isComposing) return;
    e.preventDefault();
    submit(turn && e.altKey ? "queued" : turn ? "steering" : "prompt");
  }

  const liveWord = $derived(status === "live" ? (turn ? "Working" : "Ready") : status === "reconnecting" ? "Reconnecting" : status === "waiting" ? "Waiting" : "Connecting");
  const liveTone = $derived(status === "live" ? (turn ? "working" : "ready") : status === "reconnecting" ? "down" : "starting");
  const shownOutgoing = $derived(outgoing.filter((o) => o.kind === "prompt"));
  const waitingOutgoing = $derived(outgoing.filter((o) => o.kind === "steering" || o.kind === "queued"));
</script>

<section class="chat" data-testid="rich-chat" aria-label="Chat with {agent}">
  <header class="head">
    <div class="col headrow">
      <span class="title" data-testid="chat-title">{title || agent}</span>
      {#if model}<span class="model" data-testid="chat-model" title="the model of the last turn">{model}</span>{/if}
      <span class="status" data-tone={liveTone} data-testid="chat-status"><span class="dot" aria-hidden="true"></span>{liveWord}</span>
    </div>
  </header>

  <div class="scroll" bind:this={scroller} onscroll={onScroll}>
    <div class="col turns" aria-live="polite">
      {#if status === "connecting" && !turns.length}
        <div class="empty"><div class="spin" aria-hidden="true"></div><p>Connecting to {agent}…</p></div>
      {:else if !turns.length && !shownOutgoing.length}
        <div class="empty">
          <h2>What should {agent} do?</h2>
          <p>{readOnly ? "Nothing said yet. What it does shows here as it happens." : status === "waiting" ? "It starts with its first prompt: type it below." : "Ask anything. It works in its own workspace, and what it does shows here as it happens."}</p>
        </div>
      {/if}
      {#each turns as t (t.id)}
        <ChatTurn turn={t} {resolve} {now} activity={t.state === "running" ? (chat.state?.activity ?? "") : ""} />
      {/each}
      {#each shownOutgoing as o (o.id)}
        <ChatPrompt prompt={o.text} origin="user" {resolve} pending />
      {/each}
    </div>
  </div>

  <div class="dock">
    <div class="col dockcol">
      {#if !stuck}<button type="button" class="jump" onclick={toBottom} aria-label="Scroll to the latest"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 5v14M6 13l6 6 6-6" /></svg></button>{/if}
      {#if failure}<div class="note" data-tone="error" role="alert">{failure}</div>{/if}
      {#if status === "reconnecting"}<div class="note" data-tone="down" role="status" data-testid="chat-reconnecting"><span>Connection lost. Reconnecting…</span><button type="button" onclick={soon}>Retry now</button></div>{/if}
      {#if notice}<div class="note" data-tone="warn" role="status"><span>{notice}</span><button type="button" onclick={() => (notice = "")}>Dismiss</button></div>{/if}

      {#if readOnly}
        <p class="readonly" data-testid="chat-readonly">You are watching this chat. Sending a prompt takes the right to operate the agent.</p>
      {:else}
        <form class="composer" data-busy={turn ? "" : undefined} onsubmit={(e) => { e.preventDefault(); submit(turn ? "steering" : "prompt"); }}>
          {#if pending.length || waitingOutgoing.length}
            <ul class="pending" data-testid="chat-pending" aria-label="Messages waiting">
              {#each pending as p (p.id)}
                <li data-kind={p.kind}><span class="k">{p.kind === "steering" ? "Steering" : "Next"}</span><span class="t">{p.text}</span></li>
              {/each}
              {#each waitingOutgoing as o (o.id)}
                <li data-kind={o.kind} data-pending><span class="k">{o.kind === "steering" ? "Steering" : "Next"}</span><span class="t">{o.text}</span></li>
              {/each}
            </ul>
          {/if}
          <textarea
            rows="1"
            bind:this={box}
            bind:value={draft}
            onkeydown={keydown}
            disabled={!canSend}
            aria-label="Prompt"
            placeholder={!chat.channel ? "Waiting for the agent's first session…" : turn ? "Steer the running turn, or queue what comes next…" : `Message ${agent}…`}
          ></textarea>
          <div class="bar">
            <span class="hint">{turn ? "Enter steers · Alt+Enter queues" : "Enter to send · Shift+Enter for a new line"}</span>
            {#if turn}
              <button type="button" class="ghost" disabled={!draft.trim() || !canSend} onclick={() => submit("queued")}>Queue</button>
              <button type="submit" class="round send" disabled={!draft.trim() || !canSend} aria-label="Steer" title="Steer the running turn (Enter)"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 19V5M6 11l6-6 6 6" /></svg></button>
              <button type="button" class="round stop" onclick={stop} aria-label="Stop" title="Stop the running turn"><svg viewBox="0 0 24 24" aria-hidden="true"><rect x="7" y="7" width="10" height="10" rx="1.5" /></svg></button>
            {:else}
              <button type="submit" class="round send" disabled={!draft.trim() || !canSend} aria-label="Send" title="Send (Enter)"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 19V5M6 11l6-6 6 6" /></svg></button>
            {/if}
          </div>
        </form>
      {/if}
    </div>
  </div>
</section>

<style>
  .chat {
    --chat-font: "Inter Variable", "Inter", ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
    --chat-col: 48rem;
    --chat-gutter: 24px;
    --bubble: var(--surface-3);
    --card: var(--surface-1);
    position: relative; display: flex; flex-direction: column; height: 100%; min-height: 0;
    font-family: var(--chat-font); font-size: 16px; line-height: 1.65; font-variant-numeric: normal; font-variant-ligatures: normal;
    font-feature-settings: "cv11", "ss01"; letter-spacing: -0.006em;
    margin-inline: calc(-1 * max(var(--space-5), env(safe-area-inset-left, 0px)));
  }
  .col { width: 100%; max-width: calc(var(--chat-col) + 2 * var(--chat-gutter)); margin-inline: auto; padding-inline: var(--chat-gutter); }

  .head { flex: none; position: relative; z-index: 1; }
  .head::after { content: ""; position: absolute; left: 0; right: 0; top: 100%; height: 20px; background: linear-gradient(to top, transparent, var(--bg)); pointer-events: none; }
  .headrow { display: flex; align-items: center; gap: 12px; min-height: 48px; }
  .title { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 15px; font-weight: 600; letter-spacing: -0.01em; }
  .model { flex: none; font-size: 12.5px; color: var(--text-2); background: var(--surface-2); border-radius: 999px; padding: 2px 10px; white-space: nowrap; }
  .status { flex: none; display: inline-flex; align-items: center; gap: 6px; font-size: 13px; color: var(--text-2); white-space: nowrap; }
  .status .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--status-stopped); }
  .status[data-tone="ready"] .dot { background: var(--status-running); }
  .status[data-tone="working"] .dot { background: var(--accent); animation: breathe 1.2s ease-in-out infinite; }
  .status[data-tone="down"] .dot { background: var(--status-error); }

  .scroll { flex: 1; min-height: 0; overflow-y: auto; overscroll-behavior: contain; scrollbar-gutter: stable both-edges; }
  .turns { display: flex; flex-direction: column; gap: 40px; padding-top: 32px; padding-bottom: 48px; }
  .empty { margin: 14vh auto 0; text-align: center; max-width: 34rem; color: var(--text-2); }
  .empty h2 { font-family: var(--chat-font); font-size: 28px; font-weight: 600; letter-spacing: -0.02em; color: var(--text); margin: 0 0 10px; }
  .empty p { margin: 0; font-size: 15px; }
  .spin { width: 22px; height: 22px; margin: 0 auto 12px; border-radius: 50%; border: 2px solid var(--border-strong); border-top-color: var(--accent); animation: rot 0.9s linear infinite; }

  .dock { flex: none; position: relative; padding-bottom: max(16px, env(safe-area-inset-bottom, 0px)); }
  .dock::before { content: ""; position: absolute; left: 0; right: 0; bottom: 100%; height: 28px; background: linear-gradient(to bottom, transparent, var(--bg)); pointer-events: none; }
  .dockcol { position: relative; display: grid; gap: 8px; }
  .jump { position: absolute; left: 50%; top: -52px; transform: translateX(-50%); width: 36px; height: 36px; border-radius: 50%; border: 1px solid var(--border-strong); background: var(--surface-1); color: var(--text); display: grid; place-items: center; cursor: pointer; box-shadow: var(--shadow-float); z-index: 2; }
  .jump svg, .round svg { width: 18px; height: 18px; fill: none; stroke: currentColor; stroke-width: 2.2; stroke-linecap: round; stroke-linejoin: round; }
  .note { display: flex; align-items: center; gap: 12px; font-size: 14px; padding: 8px 14px; border-radius: 12px; background: var(--status-warn-soft); color: var(--text); }
  .note span { flex: 1; }
  .note[data-tone="error"] { background: var(--status-error-soft); }
  .note[data-tone="down"] { background: var(--status-unreachable-soft); }
  .note button { font: inherit; font-size: 13px; color: var(--text); background: transparent; border: 1px solid var(--border-strong); border-radius: 999px; padding: 2px 12px; cursor: pointer; }
  .readonly { margin: 0; text-align: center; font-size: 14px; color: var(--text-3); padding: 14px 0 4px; }

  .composer { display: grid; gap: 4px; background: var(--card); border: 1px solid var(--border-strong); border-radius: 24px; padding: 10px 10px 10px 18px; box-shadow: 0 6px 24px rgb(0 0 0 / 0.10), 0 1px 2px rgb(0 0 0 / 0.06); transition: border-color var(--dur-fast); }
  .composer:focus-within { border-color: var(--text-3); }
  .composer textarea { width: 100%; resize: none; border: 0; outline: none; background: transparent; color: var(--text); font: inherit; font-size: 16px; line-height: 1.55; padding: 6px 0 2px; min-height: 30px; max-height: 33vh; overflow-y: auto; }
  .composer textarea::placeholder { color: var(--text-3); }
  .composer textarea:disabled { cursor: not-allowed; }
  .bar { display: flex; align-items: center; gap: 8px; }
  .hint { flex: 1; font-size: 12.5px; color: var(--text-3); }
  .round { flex: none; width: 36px; height: 36px; border-radius: 50%; border: 0; display: grid; place-items: center; cursor: pointer; transition: background var(--dur-fast), opacity var(--dur-fast); }
  .round.send { background: var(--accent); color: var(--on-accent); }
  .round.send:hover:not(:disabled) { background: var(--accent-hover); }
  .round.send:disabled { background: var(--surface-3); color: var(--text-3); cursor: default; }
  .round.stop { background: var(--text); color: var(--bg); }
  .round.stop svg { fill: currentColor; stroke: none; }
  .ghost { font: inherit; font-size: 14px; color: var(--text); background: transparent; border: 1px solid var(--border-strong); border-radius: 999px; height: 36px; padding: 0 16px; cursor: pointer; }
  .ghost:hover:not(:disabled) { background: var(--surface-2); }
  .ghost:disabled { color: var(--text-3); cursor: default; }

  .pending { list-style: none; margin: 0 0 4px; padding: 0 0 8px; display: grid; gap: 6px; border-bottom: 1px solid var(--border); }
  .pending li { display: flex; align-items: center; gap: 10px; font-size: 14px; color: var(--text-2); min-width: 0; }
  .pending li[data-pending] { opacity: 0.55; }
  .pending .k { flex: none; font-size: 11.5px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.04em; color: var(--accent-text); background: var(--accent-soft); border-radius: 999px; padding: 1px 8px; }
  .pending li[data-kind="queued"] .k { color: var(--text-2); }
  .pending .t { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

  @keyframes breathe { 50% { opacity: 0.35; } }
  @keyframes rot { to { transform: rotate(360deg); } }
  @media (prefers-reduced-motion: reduce) { .status .dot, .spin { animation: none !important; } }
  @media (max-width: 720px) {
    .chat { --chat-gutter: 14px; }
    .hint { display: none; }
    .turns { gap: 28px; padding-top: 20px; }
    .empty { margin-top: 8vh; }
    .empty h2 { font-size: 22px; }
    .bar { justify-content: flex-end; }
  }
</style>
