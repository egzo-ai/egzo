<script lang="ts">
  import { app, seesLinks, toggleTheme } from "../lib/app.svelte";
  import { href, nav } from "../lib/router.svelte";
  import { workspace } from "../lib/workspace.svelte";
  import Avatar from "./Avatar.svelte";
  import Icon from "./Icon.svelte";
  import Logo from "./Logo.svelte";

  // hub pages have no left nav: what lives in its foot (settings, account, theme, sign out) sits up here
  let { onsignout }: { onsignout: () => void } = $props();

  const route = $derived(nav.route);
  const offline = $derived(app.projects.filter((p) => p.offlineSince).length);
  const totalWaiting = $derived(app.projects.reduce((n, p) => n + p.agents.filter((a) => a.waiting).length, 0));
</script>

<header class="hubbar">
  <a class="brand" href={href.home()} aria-label="egzo hub, all projects"><Logo /><b>egzo<span>/hub</span></b></a>
  <nav class="hubbar-links" aria-label="Hub">
    <a href={href.home()} aria-current={route.name === "home" ? "page" : undefined}>Projects{#if totalWaiting}&nbsp;<span class="qcount" title="{totalWaiting} waiting for an answer">{totalWaiting}?</span>{/if}</a>
    {#if seesLinks()}
      <a href={href.settings()} aria-current={route.name === "settings" && route.tab !== "account" ? "page" : undefined} aria-label={offline ? `Settings: ${offline} of ${app.projects.length} projects offline` : "Settings"}>
        Settings{#if offline}<span class="warn">&nbsp;⌁ {offline} offline</span>{/if}
      </a>
    {/if}
  </nav>
  <button class="goto" aria-label="Go to a project or agent" onclick={() => (app.palette = true)}>
    <Icon name="search" size="sm" /><span class="t">Go to…</span><span class="kbd">:</span>
  </button>
  <span class="grow"></span>
  {#if workspace.terms.length}<span class="muted hide-sm" title="Open terminals, across all projects">{workspace.terms.length} terminal{workspace.terms.length === 1 ? "" : "s"} open</span>{/if}
  <a class="me" data-testid="signed-in-as" href={href.account()} title="Your account" aria-current={route.name === "settings" && route.tab === "account" ? "page" : undefined}>
    {#if app.user}<Avatar user={app.user} me />{/if}<span class="t hide-sm">{app.user?.username}{#if app.user?.isOperator}&nbsp;· operator{/if}</span>
  </a>
  <button class="btn" data-variant="quiet" data-size="sm" data-icon-only aria-label="Switch theme" onclick={toggleTheme}><Icon name="sun" /></button>
  <button class="btn" data-variant="quiet" data-size="sm" data-icon-only aria-label="Sign out" title="Sign out" onclick={onsignout}><Icon name="signout" /></button>
</header>
