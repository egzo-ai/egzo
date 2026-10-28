<script lang="ts">
  import { app, multiEngine, signOut, toggleTheme } from "../lib/app.svelte";
  import { taskDoing } from "../lib/format";
  import { href, nav } from "../lib/router.svelte";
  import { closeTerm, showPage, showTerm, workspace, type Term } from "../lib/workspace.svelte";
  import Avatar from "./Avatar.svelte";
  import EngineBadge from "./EngineBadge.svelte";
  import Icon from "./Icon.svelte";
  import Logo from "./Logo.svelte";

  let { onsignout }: { onsignout: () => void } = $props();

  const multi = $derived(multiEngine());
  const onPage = $derived(workspace.active === null);
  const route = $derived(nav.route);
  const down = $derived(app.engines.filter((e) => !e.up).length);

  function agentOf(t: Term) {
    return app.projects.find((p) => p.engine === t.engine && p.name === t.project)?.agents.find((a) => a.name === t.agent);
  }

  /** The state shown for a terminal: a connection that is gone because the agent stopped says so. */
  function shownState(t: Term): string {
    const a = agentOf(t);
    if (t.state !== "connected" && t.state !== "connecting" && a && !a.running) return "stopped";
    return t.state;
  }

  const stateText: Record<string, string> = {
    connecting: "connecting", connected: "connected", busy: "in use by someone else", taken: "taken over", disconnected: "disconnected", error: "error", stopped: "agent stopped",
  };

  function what(t: Term): [string, string] {
    const a = agentOf(t);
    switch (shownState(t)) {
      case "connecting": return ["", "Connecting…"];
      case "busy": return ["busy", `${t.holder || "Someone"} holds it`];
      case "taken": return ["busy", `Taken over by ${t.holder || "someone"}`];
      case "disconnected": return ["", "Disconnected"];
      case "error": return ["error", t.message || "Error"];
      case "stopped": return ["", "Agent stopped"];
    }
    if (a?.task) return ["", `${taskDoing(a.task)[0]!.toUpperCase()}${taskDoing(a.task).slice(1)}…`];
    if (a?.waiting) return ["waiting", "? " + (a.status || "Waiting for an answer")];
    return ["", a?.status || (a?.activity && a.activity !== "unknown" ? a.activity : "")];
  }

  function label(t: Term): string {
    const a = agentOf(t);
    const s = shownState(t);
    return `${t.agent}, ${t.project}${multi ? " on " + t.engine : ""}, ${stateText[s]}${s === "connected" && a && a.activity !== "unknown" ? ", " + a.activity : ""}${t.unread ? ", new output" : ""}`;
  }

  const waitingIn = (engine: string, project: string) => app.projects.find((p) => p.engine === engine && p.name === project)?.agents.filter((a) => a.waiting).length ?? 0;
  const totalWaiting = $derived(app.projects.reduce((n, p) => n + p.agents.filter((a) => a.waiting).length, 0));

  function termKey(e: KeyboardEvent, t: Term) {
    if (e.key === "Enter" || e.key === " ") { e.preventDefault(); showTerm(t.id); }
    else if (e.key === "Delete" || e.key === "Backspace") { e.preventDefault(); closeTerm(t.id); }
    else if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const items = [...(e.currentTarget as HTMLElement).parentElement!.querySelectorAll<HTMLElement>(".term-item")];
      const i = items.indexOf(e.currentTarget as HTMLElement);
      items[(i + (e.key === "ArrowDown" ? 1 : -1) + items.length) % items.length]?.focus();
    }
  }

  const sections = $derived.by(() => {
    if (route.name !== "project" && route.name !== "agent") return null;
    const p = app.projects.find((x) => x.engine === route.engine && x.name === route.project);
    if (!p) return null;
    const admin = p.role === "administrator" || p.role === "owner";
    return {
      project: p,
      list: [
        ["agents", "Agents", p.agents.length],
        ["services", "Services", null],
        ["timeline", "Timeline", null],
        ["network", "Network", null],
        ["spec", "Spec", null],
        ...(admin ? [["audit", "Audit log", null]] : []),
        ...(p.role === "owner" ? [["members", "Members", null]] : []),
      ] as [import("../lib/router.svelte").ProjectTab, string, number | null][],
    };
  });
</script>

<nav class="side" aria-label="Main">
  <div class="side-head">
    <a class="brand" href={href.home()} aria-label="egzo hub, all projects"><Logo /><b>egzo<span>/hub</span></b></a>
    <button class="btn collapse" data-variant="quiet" data-size="sm" data-icon-only aria-label="{workspace.rail ? 'Expand' : 'Collapse'} sidebar" title="{workspace.rail ? 'Expand' : 'Collapse'} sidebar (Ctrl+] b)" onclick={() => (workspace.rail = !workspace.rail)}>
      <Icon name={workspace.rail ? "chev-r" : "single"} size="sm" />
    </button>
    <button class="btn close-nav" data-variant="quiet" data-size="sm" data-icon-only aria-label="Close menu" onclick={() => (workspace.navOpen = false)}><Icon name="x" /></button>
  </div>
  <button class="goto" aria-label="Go to a project or agent" onclick={() => (app.palette = true)}>
    <Icon name="search" size="sm" /><span class="t">Go to…</span><span class="kbd">:</span>
  </button>

  <div class="side-scroll">
    <div class="side-h"><span class="label">Terminals</span><span class="count">{workspace.terms.length || ""}</span></div>
    <div class="side-list" role="listbox" aria-label="Open terminals">
      {#each workspace.terms as t, i (t.id)}
        {@const sel = workspace.active === t.id}
        {@const s = shownState(t)}
        {@const a = agentOf(t)}
        {@const [wt, w] = what(t)}
        <div
          role="option"
          tabindex={sel || (workspace.active === null && i === 0) ? 0 : -1}
          class="term-item"
          aria-selected={sel}
          aria-label={label(t)}
          title="{t.engine}:{t.project}/{t.agent}"
          data-tab={t.id}
          data-unread={t.unread && !sel ? "" : undefined}
          data-visible={workspace.active !== null && workspace.panes.includes(t.id) && !sel ? "" : undefined}
          onclick={() => showTerm(t.id)}
          onkeydown={(e) => termKey(e, t)}
        >
          <span class="glyph">
            {#if s === "connecting"}<span class="dot" data-s="starting"></span>
            {:else if s === "busy"}<Icon name="lock" />
            {:else if s === "taken"}<Icon name="swap" />
            {:else if s === "disconnected"}<Icon name="plug-off" />
            {:else if s === "error"}<span data-g="error">✕</span>
            {:else if s === "stopped"}<span class="dot" data-s="stopped"></span>
            {:else}<span class="dot" data-s={a?.waiting ? "waiting" : a?.activity && a.activity !== "unknown" ? a.activity : "running"}></span>{/if}
          </span>
          <span class="nm">{t.agent}</span>
          <span class="end">
            {#if a?.waiting && s === "connected" && !sel}<span class="qcount" title="waiting for an answer">?</span>{/if}
            {#if t.unread && !sel && !a?.waiting}<span class="unread" title="new output"></span>{/if}
            <span class="n">{i + 1}</span>
            <button class="x" aria-label="Close {t.agent}" onclick={(e) => { e.stopPropagation(); closeTerm(t.id); }}><Icon name="x" size="sm" /></button>
          </span>
          <span class="where">{t.project}{multi ? " · " + t.engine : ""}</span>
          <span class="what" data-t={wt}>{w}</span>
        </div>
      {:else}
        <p class="side-empty">No terminal open. Use <b>Attach</b> on an agent; it opens here and stays open while you browse.</p>
      {/each}
    </div>
    <div class="side-add">
      <button class="btn" data-variant="quiet" data-size="sm" style="width:100%;justify-content:flex-start" aria-label="Attach an agent" onclick={() => (app.palette = true)}>
        <Icon name="plus" size="sm" /><span class="t">Attach an agent</span>
      </button>
    </div>

    <div class="side-h"><span class="label">Projects</span><span class="count">{app.projects.length}</span></div>
    <div class="side-list">
      <a class="nav-item" href={href.home()} aria-current={onPage && route.name === "home" ? "page" : undefined} onclick={showPage}>
        <span class="ic"><Icon name="list" size="sm" /></span><span class="nm">All projects</span>
        <span class="end">{#if totalWaiting}<span class="qcount" title="{totalWaiting} waiting for an answer">{totalWaiting}?</span>{/if}</span>
      </a>
      {#each app.projects as p (p.engine + "/" + p.name)}
        {@const current = (route.name === "project" || route.name === "agent") && route.engine === p.engine && route.project === p.name}
        {@const q = waitingIn(p.engine, p.name)}
        <a class="nav-item proj" href={href.project(p.engine, p.name)} title="{p.engine}:{p.name}" onclick={showPage}>
          <span class="ic"><Icon name={current ? "chev-d" : "chev-r"} size="sm" /></span><span class="ini" aria-hidden="true">{p.name.slice(0, 2)}</span>
          <span class="nm">{p.name}</span>
          <span class="end">
            {#if q}<span class="qcount" title="{q} waiting for an answer">{q}?</span>{/if}
            {#if !p.up}<span class="pill" data-s="warn" style="height:16px;font-size:10px">down</span>{/if}
            {#if multi}<EngineBadge name={p.engine} />{/if}
          </span>
        </a>
        {#if current && sections}
          <div class="nav-sub">
            {#each sections.list as [tab, name, count]}
              <a class="nav-item" href={href.project(p.engine, p.name, tab)} aria-current={onPage && route.name === "project" && route.tab === tab ? "page" : undefined} onclick={showPage}>
                <span class="nm">{name}</span><span class="end"><span class="n">{count ?? ""}</span></span>
              </a>
            {/each}
            {#if route.name === "agent"}
              <a class="nav-item" href={href.agent(p.engine, p.name, route.agent)} aria-current={onPage ? "page" : undefined} onclick={showPage}><span class="nm">↳ {route.agent}</span></a>
            {/if}
          </div>
        {/if}
      {/each}
    </div>
  </div>

  <div class="side-foot">
    {#if app.user?.isOperator}
      <a class="health" href={href.settings()} data-down={down ? "" : undefined} aria-current={onPage && route.name === "settings" ? "page" : undefined} aria-label={down ? `Settings: ${down} of ${app.engines.length} engines unreachable` : "Settings"} onclick={showPage}>
        <Icon name="engine" size="sm" /><span class="t">Settings</span>
        {#if down}<span class="warn hide-rail">⌁ {down} down</span>{/if}
      </a>
    {:else if multi}
      <div class="health" data-down={down ? "" : undefined}>
        <Icon name="engine" size="sm" /><span class="t">Engines&nbsp;<span class="muted">{app.engines.length - down}/{app.engines.length}</span></span>
        {#if down}<span class="warn hide-rail">⌁ {down} down</span>{/if}
      </div>
    {/if}
    <div class="row">
      <a class="me" data-testid="signed-in-as" href={href.account()} title="Your account" onclick={showPage}>
        {#if app.user}<Avatar user={app.user} me />{/if}<span class="t">{app.user?.username}{#if app.user?.isOperator}&nbsp;· operator{/if}</span>
      </a>
      <span class="grow"></span>
      <button class="btn hide-rail" data-variant="quiet" data-size="sm" data-icon-only aria-label="Switch theme" onclick={toggleTheme}><Icon name="sun" /></button>
      <button class="btn hide-rail" data-variant="quiet" data-size="sm" data-icon-only aria-label="Sign out" title="Sign out" onclick={onsignout}><Icon name="signout" /></button>
    </div>
  </div>
</nav>
