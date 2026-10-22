<script lang="ts">
  import { app, multiEngine } from "../lib/app.svelte";
  import { href, nav, navigate } from "../lib/router.svelte";
  import EngineBadge from "../components/EngineBadge.svelte";
  import Icon from "../components/Icon.svelte";

  const multi = $derived(multiEngine());
  const engineFilter = $derived(new URLSearchParams(nav.path.split("?")[1] ?? "").get("engine") ?? "all");
  let text = $state("");

  const rows = $derived(
    app.projects
      .filter((p) => (engineFilter === "all" || p.engine === engineFilter) && p.name.toLowerCase().includes(text.trim().toLowerCase()))
      .map((p) => {
        const running = p.agents.filter((a) => a.running).length;
        const waiting = p.agents.filter((a) => a.waiting).length;
        return { p, running, waiting, stopped: p.agents.length - running };
      }),
  );
  const engineNames = $derived([...new Set(app.projects.map((p) => p.engine))]);

  function setFilter(name: string) {
    navigate(name === "all" ? "/" : "/?engine=" + encodeURIComponent(name), true);
  }
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

  {#each app.unreachable as e (e.name)}
    <div class="banner" data-tone="unreachable" role="alert">
      <span class="tag">⌁ {e.name} unreachable</span>
      <div class="body">Engine <strong>{e.name}</strong> is unreachable: its projects are hidden until it answers again. <span class="muted">{e.err}</span></div>
    </div>
  {/each}

  {#if multi && app.projects.length}
    <div class="toolbar">
      <span class="label">Engine</span>
      <div class="chips" role="group" aria-label="Filter by engine">
        <button class="chip" aria-pressed={engineFilter === "all"} onclick={() => setFilter("all")}>All <span class="count">{app.projects.length}</span></button>
        {#each app.engines as e (e.name)}
          <button class="chip" aria-pressed={engineFilter === e.name} onclick={() => setFilter(e.name)} data-down={e.up ? undefined : ""} title={e.up ? "reachable" : "unreachable: " + (e.err ?? "")}>
            <EngineBadge name={e.name} /><span class="count">{app.projects.filter((p) => p.engine === e.name).length}</span>
          </button>
        {/each}
      </div>
    </div>
  {/if}

  {#if app.projects.length === 0 && app.projectsLoaded}
    <div class="empty">
      <pre class="art" aria-hidden="true">┌──────────────┐
│  0 projects  │
└──────────────┘</pre>
      <h2>Nothing here yet</h2>
      <p>You don't have a role on any project. Projects appear here when one of their owners adds you. Tell them your username: <code>{app.user?.username}</code>.</p>
      <p class="muted">Projects are created from the command line with <code>egzo project up</code>, not from the hub.</p>
    </div>
  {:else if rows.length === 0 && app.projects.length}
    <p class="muted">No project matches. <a href={href.home()}>Show all</a></p>
  {:else if rows.length}
    <div class="table-wrap">
      <table class="table" data-testid="projects">
        <thead><tr><th>Project</th>{#if multi}<th>Engine</th>{/if}<th>Agents</th><th>Questions</th><th>Your role</th></tr></thead>
        <tbody>
          {#each rows as { p, running, waiting, stopped } (p.engine + "/" + p.name)}
            <tr data-row tabindex="-1" data-project={p.name}>
              <td><a class="name" href={href.project(p.engine, p.name)}>{p.name}</a>{#if !p.up} <span class="pill" data-s="warn" title="the project's sidecars are not running">down</span>{/if}</td>
              {#if multi}<td><EngineBadge name={p.engine} /></td>{/if}
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
  {#if engineFilter !== "all"}<p><a href={href.home()}>Show all engines</a></p>{/if}
</div>
