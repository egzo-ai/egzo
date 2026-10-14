<script lang="ts">
  import { nav } from "../lib/router.svelte";
  import Logo from "../components/Logo.svelte";

  const params = $derived(new URLSearchParams(nav.path.split("?")[1] ?? ""));
  const next = $derived(params.get("next") ?? "/");
  const error = $derived(params.get("error"));
</script>

<div class="signin">
  <div class="box">
    <div class="mark"><Logo /><b>egzo hub</b></div>
    <h1 class="sr-only">Sign in</h1>
    <p class="boot">The hub signs you in through your organisation's login provider.<span class="cur" aria-hidden="true"></span></p>
    {#if error}
      <div class="banner" role="alert"><span class="tag">✕ Sign-in failed</span><div class="body">{error}</div></div>
    {/if}
    <p><a class="btn" data-variant="primary" data-size="lg" data-external href="/login/start?next={encodeURIComponent(next)}">Sign in</a></p>
  </div>
</div>
