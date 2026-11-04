<script lang="ts">
  import { app, projectName } from "../lib/app.svelte";
  import { nav } from "../lib/router.svelte";
  import { termById, workspace } from "../lib/workspace.svelte";
  import Icon from "./Icon.svelte";

  const term = $derived(termById(workspace.active));
  const title = $derived.by(() => {
    const r = nav.route;
    switch (r.name) {
      case "home": return "Projects";
      case "settings": return "Settings";
      case "project": return projectName(r.project);
      case "agent": return `${projectName(r.project)} / ${r.agent}`;
      default: return "egzo hub";
    }
  });
</script>

<header class="mbar">
  <button class="btn" data-variant="quiet" data-icon-only aria-label="Open menu" onclick={() => (workspace.navOpen = true)}><Icon name="list" /></button>
  <span class="t">{#if term}<b>{term.agent}</b> <span class="muted">{projectName(term.project)}</span>{:else}{title}{/if}</span>
  <button class="btn" data-variant="quiet" data-icon-only aria-label="Go to" onclick={() => (app.palette = true)}><Icon name="search" /></button>
</header>
