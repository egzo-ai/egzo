<script lang="ts">
  import { api } from "../lib/api";
  import { errorText, signedIn } from "../lib/app.svelte";
  import { nav } from "../lib/router.svelte";
  import type { SignInOptions } from "../lib/types";
  import Logo from "../components/Logo.svelte";

  const params = $derived(new URLSearchParams(nav.path.split("?")[1] ?? ""));
  const next = $derived(params.get("next") ?? "/");
  const error = $derived(params.get("error"));

  let options = $state<SignInOptions | null>(null);
  let username = $state("admin");
  let password = $state("");
  let failure = $state("");
  let busy = $state(false);

  $effect(() => {
    api.signInOptions().then((o) => (options = o), (e) => (failure = errorText(e)));
  });

  async function submit(e: Event) {
    e.preventDefault();
    busy = true;
    failure = "";
    try {
      await api.passwordLogin(username, password);
      password = "";
      await signedIn(); // the app then goes where the person was going
    } catch (e) {
      failure = errorText(e);
    } finally {
      busy = false;
    }
  }
</script>

<div class="signin">
  <div class="box">
    <div class="mark"><Logo /><b>egzo hub</b></div>
    <h1 class="sr-only">Sign in</h1>
    {#if error}
      <div class="banner" role="alert"><span class="tag">✕ Sign-in failed</span><div class="body">{error}</div></div>
    {/if}
    {#if failure}
      <div class="banner" role="alert"><span class="tag">✕ Sign-in failed</span><div class="body">{failure}</div></div>
    {/if}
    {#if options?.local}
      <form class="signin-form" onsubmit={submit} aria-label="Local account">
        <div class="field"><label for="s-user">Username</label>
          <input id="s-user" class="input" bind:value={username} autocomplete="username" required /></div>
        <div class="field"><label for="s-password">Password</label>
          <!-- svelte-ignore a11y_autofocus -->
          <input id="s-password" class="input" type="password" bind:value={password} autocomplete="current-password" required autofocus /></div>
        <button class="btn" data-variant={options.sso ? undefined : "primary"} data-size="lg" type="submit" disabled={busy}>Sign in</button>
      </form>
    {/if}
    {#if options?.sso}
      <p><a class="btn" data-variant="primary" data-size="lg" data-external href="/login/start?next={encodeURIComponent(next)}">Sign in with {options.sso}</a></p>
      {#if options.redirect}<p class="boot">The redirect address to register at the provider: <code>{options.redirect}</code></p>{/if}
    {/if}
    {#if options && !options.local && !options.sso}
      <p class="boot">There is no way to sign in: the hub has local login turned off and no single sign-on.</p>
    {/if}
  </div>
</div>
