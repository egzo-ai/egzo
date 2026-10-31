<script lang="ts">
  import { api } from "../lib/api";
  import { app, errorText, signedIn } from "../lib/app.svelte";
  import { stamp } from "../lib/format";
  import { href, navigate } from "../lib/router.svelte";
  import type { InvitePreview } from "../lib/types";
  import Logo from "../components/Logo.svelte";

  // the code is after the # of the link: it is sent to no server, and is read here once
  const code = decodeURIComponent(location.hash.slice(1));

  let preview = $state<InvitePreview | null>(null);
  let gone = $state("");
  let failure = $state("");
  let busy = $state(false);
  let username = $state("");
  let password = $state("");
  let signIn = $state({ username: "", password: "" });

  $effect(() => {
    if (!code) {
      gone = "This address holds no invite. Open the whole link you were given.";
      return;
    }
    api.peekInvite(code).then((p) => (preview = p), (e) => (gone = errorText(e)));
  });

  async function run(action: () => Promise<void>) {
    busy = true;
    failure = "";
    try {
      await action();
    } catch (e) {
      failure = errorText(e);
    } finally {
      busy = false;
    }
  }

  /** The invite is spent: the code leaves the address, and the person goes to what it gave them. */
  async function done() {
    await signedIn();
    const project = preview?.invite.role && preview.invite.role !== "viewer" ? preview.invite.project : undefined;
    navigate(project ? href.project(project) : href.home(), true);
  }

  const accept = (e?: Event) => {
    e?.preventDefault();
    return run(async () => {
      await api.acceptInvite(code);
      await done();
    });
  };

  const register = (e: Event) => {
    e.preventDefault();
    return run(async () => {
      await api.registerInvite(code, username.trim(), password);
      password = "";
      await done();
    });
  };

  const passwordSignIn = (e: Event) => {
    e.preventDefault();
    return run(async () => {
      await api.passwordLogin(signIn.username, signIn.password);
      signIn.password = "";
      await signedIn(); // they are signed in here now, and accept like anybody who is
    });
  };

  const sso = () =>
    run(async () => {
      location.href = (await api.inviteSSO(code)).url;
    });
</script>

<div class="signin">
  <div class="box" data-testid="invite">
    <div class="mark"><Logo /><b>egzo hub</b></div>
    <h1 class="sr-only">Invite</h1>
    {#if gone}
      <div class="banner" role="alert" data-testid="invite-gone"><span class="tag">✕ Invite</span><div class="body">{gone}</div></div>
      <p><a class="btn" href={href.home()}>Go to the hub</a></p>
    {:else if preview}
      {@const i = preview.invite}
      {#if i.project}
        <p data-testid="invite-grants"><b>{i.createdBy}</b> invites you to <b>{i.projectName}</b> as <b>{i.role}</b>.</p>
        {#if i.role === "viewer"}<p class="muted">A viewer opens the previews of its agents from the links they are given, and sees nothing else of the project.</p>{/if}
      {:else}
        <p data-testid="invite-grants"><b>{i.createdBy}</b> invites you to this hub{#if i.operator}, as an <b>operator</b>{/if}{#if i.projectQuota}, to connect up to <b>{i.projectQuota}</b> {i.projectQuota === 1 ? "project" : "projects"}{/if}.</p>
      {/if}
      <p class="muted">Good once, until {stamp(i.expiresAt)}.</p>
      {#if failure}<div class="banner" role="alert"><span class="tag">✕ Refused</span><div class="body">{failure}</div></div>{/if}
      {#if app.user}
        <form class="signin-form" onsubmit={accept}>
          <p style="margin:0">Signed in as <b>{app.user.username}</b>.</p>
          <button class="btn" data-variant="primary" data-size="lg" type="submit" disabled={busy}>Accept</button>
        </form>
      {:else}
        {#if preview.sso}
          <p><button class="btn" data-variant="primary" data-size="lg" disabled={busy} onclick={sso}>Continue with {preview.sso}</button></p>
        {/if}
        {#if preview.local}
          <form class="signin-form" onsubmit={register} aria-label="Create an account">
            <h2 class="h-section" style="margin:0">Create an account</h2>
            <div class="field"><label for="i-user">Choose a username</label>
              <input id="i-user" class="input" bind:value={username} autocomplete="username" required maxlength="32" /></div>
            <div class="field"><label for="i-password">Choose a password</label>
              <input id="i-password" class="input" type="password" bind:value={password} autocomplete="new-password" required minlength="12" />
              <span class="help">At least 12 characters. There is no reset: an operator can only invite you again.</span></div>
            <button class="btn" data-variant={preview.sso ? undefined : "primary"} data-size="lg" type="submit" disabled={busy}>Create account</button>
          </form>
          <form class="signin-form" onsubmit={passwordSignIn} aria-label="Sign in">
            <h2 class="h-section" style="margin:0">Have an account with a password?</h2>
            <div class="field"><label for="i-have-user">Username</label>
              <input id="i-have-user" class="input" bind:value={signIn.username} autocomplete="username" required /></div>
            <div class="field"><label for="i-have-password">Password</label>
              <input id="i-have-password" class="input" type="password" bind:value={signIn.password} autocomplete="current-password" required /></div>
            <button class="btn" data-size="lg" type="submit" disabled={busy}>Sign in</button>
          </form>
        {/if}
        {#if !preview.sso && !preview.local}<p class="boot">There is no way to sign in: the hub has local login turned off and no single sign-on.</p>{/if}
      {/if}
    {:else}
      <p class="muted">Loading…</p>
    {/if}
  </div>
</div>
