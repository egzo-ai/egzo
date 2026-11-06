<script lang="ts">
  import { api } from "../lib/api";
  import { app, errorText } from "../lib/app.svelte";
  import { stamp } from "../lib/format";
  import { href, navigate } from "../lib/router.svelte";
  import type { ChatAccount, ChatLinkPreview } from "../lib/types";
  import Logo from "../components/Logo.svelte";

  // The code is after the # of the link a bridge gave: it is sent to no server. Signing in first leaves this page, so the code waits in
  // this tab's session storage, and goes once it is used.
  const key = "egzo:chat-link";
  const fromLink = decodeURIComponent(location.hash.slice(1));
  if (fromLink) {
    sessionStorage.setItem(key, fromLink);
    history.replaceState(history.state, "", location.pathname);
  }
  const code = fromLink || sessionStorage.getItem(key) || "";

  let preview = $state<ChatLinkPreview | null>(null);
  let linked = $state<ChatAccount | null>(null);
  let gone = $state("");
  let failure = $state("");
  let busy = $state(false);

  const bridgeName = (b: string) => b.charAt(0).toUpperCase() + b.slice(1);

  $effect(() => {
    if (!code) {
      gone = "This address holds no code. Open the whole link the chat gave you.";
      return;
    }
    if (!app.user) return;
    api.peekChatLink(code).then((p) => (preview = p), (e) => (gone = errorText(e)));
  });

  async function confirm() {
    busy = true;
    failure = "";
    try {
      linked = await api.confirmChatLink(code);
      sessionStorage.removeItem(key);
    } catch (e) {
      failure = errorText(e);
    } finally {
      busy = false;
    }
  }

  function cancel() {
    sessionStorage.removeItem(key);
    navigate(href.home(), true);
  }
</script>

<div class="signin">
  <div class="box" data-testid="chat-link">
    <div class="mark"><Logo /><b>egzo hub</b></div>
    <h1 class="sr-only">Link a chat account</h1>
    {#if gone}
      <div class="banner" role="alert" data-testid="chat-link-gone"><span class="tag">✕ Link</span><div class="body">{gone}</div></div>
      <p><a class="btn" href={href.home()}>Go to the hub</a></p>
    {:else if linked}
      <div class="banner" role="status" data-testid="chat-link-done"><span class="tag">✓ Linked</span>
        <div class="body">Your {bridgeName(linked.bridge)} account <b>{linked.name}</b> is linked to <b>{app.user?.username}</b>. Go back to {bridgeName(linked.bridge)}: you can talk to your agents there.</div></div>
      <p><a class="btn" href={href.account()}>Your chat accounts</a></p>
    {:else if !app.user}
      <p>A chat wants to link an account to your hub user. Sign in first.</p>
      <p><a class="btn" data-variant="primary" data-size="lg" href="/login?next=/chat-link">Sign in</a></p>
    {:else if preview}
      <p data-testid="chat-link-asks">Link the {bridgeName(preview.bridge)} account <b>{preview.name}</b> to your hub user <b>{app.user.username}</b>?</p>
      <p class="muted">From then on, what this account says to your agents on {bridgeName(preview.bridge)} is from you, and they answer you there. Only do this for your own account. The code is good until {stamp(preview.expires)}.</p>
      {#if failure}<div class="banner" role="alert"><span class="tag">✕ Refused</span><div class="body">{failure}</div></div>{/if}
      <p style="display:flex;gap:var(--space-2)">
        <button class="btn" data-variant="primary" data-size="lg" disabled={busy} onclick={confirm}>Link</button>
        <button class="btn" data-size="lg" disabled={busy} onclick={cancel}>Cancel</button>
      </p>
    {:else}
      <p class="muted">Loading…</p>
    {/if}
  </div>
</div>
