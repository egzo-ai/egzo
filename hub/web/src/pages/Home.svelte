<script lang="ts">
  import { app } from "../lib/app.svelte";
  import { ago, stamp } from "../lib/format";
  import { href } from "../lib/router.svelte";
  import Icon from "../components/Icon.svelte";

  let text = $state("");

  const rows = $derived(
    app.projects
      .filter((p) => (p.name + " " + p.host).toLowerCase().includes(text.trim().toLowerCase()))
      .map((p) => {
        const running = p.agents.filter((a) => a.running).length;
        const waiting = p.agents.filter((a) => a.waiting).length;
        return { p, running, waiting, stopped: p.agents.length - running };
      }),
  );
</script>

<div class="page">
  <div class="phead">
    <h1 class="h-page">Projects</h1><span class="muted">{app.projects.length}</span><span class="grow"></span>
    {#if app.projects.length}
      <div class="search">
        <label class="sr-only" for="q">Filter projects</label><Icon name="search" />
        <input id="q" class="input" placeholder="Filter projects" bind:value={text} data-filter-input /><span class="kbd">/</span>
      </div>
    {/if}
  </div>

  {#if app.projects.length === 0 && app.projectsLoaded}
    <div class="empty">
      <pre class="art" aria-hidden="true">┌──────────────┐
│  0 projects  │
└──────────────┘</pre>
      <h2>Nothing here yet</h2>
      {#if app.user?.isOperator}
        <p>No project has linked to this hub yet. Name it in a project file, <code>hub: {app.account.hub}</code>, run <code>egzo project up</code>, then <a href={href.settings("links")}>make a link code</a> for <code>egzo project register</code>.</p>
      {:else}
        <p>You don't have a role on any project. Projects appear here when one of their owners adds you. Tell them your username: <code>{app.user?.username}</code>.</p>
      {/if}
      <p class="muted">Projects are created from the command line with <code>egzo project register</code>, not from the hub.</p>
    </div>
  {:else if rows.length === 0 && app.projects.length}
    <p class="muted">No project matches. <a href={href.home()}>Show all</a></p>
  {:else if rows.length}
    <div class="table-wrap">
      <table class="table" data-testid="projects">
        <thead><tr><th>Project</th><th>Host</th><th>Agents</th><th>Questions</th><th>Your role</th></tr></thead>
        <tbody>
          {#each rows as { p, running, waiting, stopped } (p.id)}
            <tr data-row tabindex="-1" data-project={p.name}>
              <td><a class="name" href={href.project(p.id)}>{p.name}</a>{#if p.offlineSince} <span class="pill" data-s="unreachable" data-testid="offline" title="offline since {stamp(p.offlineSince)}">offline {ago(p.offlineSince)}</span>{:else if !p.up} <span class="pill" data-s="warn" title="the project's sidecars are not running">down</span>{/if}</td>
              <td class="muted">{p.host}</td>
              <td>
                <span class="agentsbar" aria-hidden="true">
                  {#if p.agents.length}<i class="r" style:width="{(running - waiting) / p.agents.length * 100}%"></i><i class="w" style:width="{waiting / p.agents.length * 100}%"></i><i class="s" style:width="{stopped / p.agents.length * 100}%"></i>{/if}
                </span>{running} running of {p.agents.length}
              </td>
              <td>{#if waiting}<span class="pill" data-s="waiting">{waiting} waiting</span>{:else}<span class="muted">–</span>{/if}</td>
              <td>{#if p.operator}<span class="badge" title="You hold every project as an operator">operator</span>{:else}{p.role}{/if}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>
