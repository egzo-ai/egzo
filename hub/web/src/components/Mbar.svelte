<script lang="ts">
  import { app } from "../lib/app.svelte";
  import { nav } from "../lib/router.svelte";
  import { termById, workspace } from "../lib/workspace.svelte";
  import Icon from "./Icon.svelte";

  const term = $derived(termById(workspace.active));
  const waiting = $derived(app.projects.some((p) => p.agents.some((a) => a.waiting)));
  const title = $derived.by(() => {
    const r = nav.route;
    switch (r.name) {
      case "home": return "Projects";
      case "engines": return "Engines";
      case "project": return r.project;
      case "agent": return `${r.project} / ${r.agent}`;
      default: return "egzo hub";
    }
  });
</script>

<header class="mbar">
  <button class="btn" data-variant="quiet" data-icon-only aria-label="Open menu" onclick={() => (workspace.navOpen = true)}><Icon name="list" /></button>
  <span class="t">{#if term}<b>{term.agent}</b> <span class="muted">{term.project}</span>{:else}{title}{/if}</span>
  {#if waiting}<span class="qcount" title="an agent is waiting for an answer">?</span>{/if}
  <button class="btn" data-variant="quiet" data-icon-only aria-label="Go to" onclick={() => (app.palette = true)}><Icon name="search" /></button>
</header>
