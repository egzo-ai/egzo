<script lang="ts">
  import { app, seesLinks } from "../lib/app.svelte";
  import { ago, stamp } from "../lib/format";
  import { href, nav } from "../lib/router.svelte";
  import { inviteFailedText } from "../lib/signin";
  import { workspace } from "../lib/workspace.svelte";
  import Icon from "../components/Icon.svelte";

  let text = $state("");
  let view = $state<"tiles" | "list">((() => { try { return localStorage.getItem("egzo.projectView") === "list" ? "list" : "tiles"; } catch { return "tiles"; } })());
  $effect(() => { try { localStorage.setItem("egzo.projectView", view); } catch { /* ignore */ } });
  const openIn = (id: string) => workspace.terms.filter((t) => t.project === id).length;

  // the sign-in worked, and the invite it came with did not
  const inviteFailed = $derived(new URLSearchParams(nav.path.split("?")[1] ?? "").has("invite"));
  const ownerless = $derived(app.projects.filter((p) => p.ownerless));

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
      <div class="seg" role="group" aria-label="View">
        <button aria-pressed={view === "tiles"} onclick={() => (view = "tiles")} title="Tiles"><Icon name="grid" size="sm" /></button>
        <button aria-pressed={view === "list"} onclick={() => (view = "list")} title="List"><Icon name="list" size="sm" /></button>
      </div>
      <div class="search">
        <label class="sr-only" for="q">Filter projects</label><Icon name="search" />
        <input id="q" class="input" placeholder="Filter projects" bind:value={text} data-filter-input /><span class="kbd">/</span>
      </div>
    {/if}
  </div>

  {#if inviteFailed}<div class="banner" role="alert"><span class="tag">✕ Invite</span><div class="body">{inviteFailedText}</div></div>{/if}
  {#if ownerless.length}
    <div class="banner" data-tone="warn" role="status" data-testid="ownerless"><span class="tag">! No owner</span>
      <div class="body">{ownerless.map((p) => p.name).join(", ")} {ownerless.length === 1 ? "has" : "have"} nobody who can sign in as owner. Give {ownerless.length === 1 ? "it" : "them"} one on the Members tab, or revoke under <a href={href.links()}>Links</a>.</div></div>
  {/if}

  {#if app.projects.length === 0 && app.projectsLoaded}
    <div class="empty">
      <pre class="art" aria-hidden="true">┌──────────────┐
│  0 projects  │
└──────────────┘</pre>
      <h2>Nothing here yet</h2>
      {#if seesLinks()}
        <p>No project has linked to this hub yet. Name it in a project file, <code>hub: {app.account.hub}</code>, run <code>egzo project up</code>, then <a href={href.settings("links")}>make a link code</a> for <code>egzo project register</code>.</p>
      {:else}
        <p>You don't have a role on any project. Projects appear here when one of their owners invites you. Tell them your username: <code>{app.user?.username}</code>.</p>
      {/if}
      <p class="muted">Projects are created from the command line with <code>egzo project register</code>, not from the hub.</p>
    </div>
  {:else if rows.length === 0 && app.projects.length}
    <p class="muted">No project matches. <a href={href.home()}>Show all</a></p>
  {:else if rows.length && view === "tiles"}
    <div class="tiles" data-testid="projects">
      {#each rows as { p, running, waiting, stopped } (p.id)}
        {@const n = openIn(p.id)}
        <a class="tile" href={href.project(p.id)} data-project={p.name}>
          <header>
            <span class="ini" aria-hidden="true">{p.name.slice(0, 2)}</span>
            <span class="who"><b>{p.name}</b><small>{p.host}</small></span>
          </header>
          <div class="stats">
            <span class="muted">{p.templates.length} template{p.templates.length === 1 ? "" : "s"} ·</span>
            <span class="counts">{#if running}<span><span class="dot" data-s="running"></span>{running} running</span>{/if}{#if stopped}<span><span class="dot" data-s="stopped"></span>{stopped} stopped</span>{/if}{#if !p.agents.length}<span class="muted">no agents</span>{/if}</span>
            {#if waiting}<span class="pill" data-s="waiting">{waiting} waiting</span>{/if}
            {#if p.offlineSince}<span class="pill" data-s="unreachable" data-testid="offline" title="offline since {stamp(p.offlineSince)}">offline {ago(p.offlineSince)}</span>{:else if !p.up}<span class="pill" data-s="warn" title="the project's sidecars are not running">down</span>{/if}
            {#if p.ownerless}<span class="pill" data-s="warn" title="nobody who can sign in owns it">no owner</span>{/if}
          </div>
          <footer>
            <span>{#if p.role === "overseer"}operator{:else}{p.role}{/if}</span><span class="grow"></span>
            {#if n}<span title="open terminals"><Icon name="term" size="sm" /> {n} open</span>{/if}
          </footer>
        </a>
      {/each}
    </div>
  {:else if rows.length}
    <div class="table-wrap">
      <table class="table" data-testid="projects">
        <thead><tr><th>Project</th><th>Host</th><th>Templates</th><th>Agents</th><th>Questions</th><th>Your role</th></tr></thead>
        <tbody>
          {#each rows as { p, running, waiting, stopped } (p.id)}
            <tr data-row tabindex="-1" data-project={p.name}>
              <td><a class="name" href={href.project(p.id)}>{p.name}</a>{#if p.offlineSince} <span class="pill" data-s="unreachable" data-testid="offline" title="offline since {stamp(p.offlineSince)}">offline {ago(p.offlineSince)}</span>{:else if !p.up} <span class="pill" data-s="warn" title="the project's sidecars are not running">down</span>{/if}{#if p.ownerless} <span class="pill" data-s="warn" title="nobody who can sign in owns it">no owner</span>{/if}</td>
              <td class="muted">{p.host}</td>
              <td class="muted">{p.templates.join(", ") || "–"}</td>
              <td><span class="counts">{#if running}<span><span class="dot" data-s="running"></span>{running} running</span>{/if}{#if stopped}<span><span class="dot" data-s="stopped"></span>{stopped} stopped</span>{/if}{#if !p.agents.length}<span class="muted">no agents</span>{/if}</span></td>
              <td>{#if waiting}<span class="pill" data-s="waiting">{waiting} waiting</span>{:else}<span class="muted">–</span>{/if}</td>
              <td>{#if p.role === "overseer"}<span class="badge" title="You oversee it as an operator, with no role on it">operator</span>{:else}{p.role}{/if}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>
