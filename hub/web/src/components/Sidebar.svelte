<script lang="ts">
  import { app, projectName, seesLinks, toggleTheme } from "../lib/app.svelte";
  import { taskDoing } from "../lib/format";
  import { href, nav } from "../lib/router.svelte";
  import { closeTerm, scopedTerms, showPage, showTerm, workspace, type Term } from "../lib/workspace.svelte";
  import Avatar from "./Avatar.svelte";
  import Icon from "./Icon.svelte";
  import Logo from "./Logo.svelte";

  let { onsignout }: { onsignout: () => void } = $props();

  const onPage = $derived(workspace.active === null);
  const route = $derived(nav.route);

  function agentOf(t: Term) {
    return app.projects.find((p) => p.id === t.project)?.agents.find((a) => a.name === t.agent);
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
    return `${t.agent}, ${projectName(t.project)}, ${stateText[s]}${s === "connected" && a && a.activity !== "unknown" ? ", " + a.activity : ""}${t.unread ? ", new output" : ""}`;
  }

  // the nav belongs to one project: its pages and its open terminals; other projects are one click away on the project list
  const terms = $derived(scopedTerms());

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
    const p = app.projects.find((x) => x.id === workspace.scope);
    if (!p) return null;
    const admin = p.role === "admin" || p.role === "owner";
    return {
      project: p,
      list: [
        ["agents", "Agents", p.agents.length],
        ["templates", "Templates", p.templates.length],
        ...(p.role === "overseer" ? [] : [["activity", "Activity", null], ["network", "Network", null]]),
        ...(admin ? [["audit", "Audit log", null]] : []),
        ...(p.role === "owner" || app.user?.isOperator ? [["members", "Members", null]] : []),
        ["advanced", "Advanced", null],
      ] as [import("../lib/router.svelte").ProjectTab, string, number | null][],
    };
  });
</script>

<nav class="side" aria-label="Project">
  <div class="side-head">
    <a class="back" href={href.home()} aria-label="All projects" title="All projects" onclick={showPage}>
      <span class="arrow">←</span>
    </a>
    {#if sections}
      {@const p = sections.project}
      <a class="proj-title" href={href.project(p.id)} title="{p.name} on {p.host}" onclick={showPage}>
        <span class="ini" aria-hidden="true">{p.name.slice(0, 2)}</span>
        <span class="who"><b>{p.name}</b><small>{p.host}</small></span>
        {#if p.offlineSince}<span class="pill" data-s="unreachable" style="height:16px;font-size:10px">offline</span>{:else if !p.up}<span class="pill" data-s="warn" style="height:16px;font-size:10px">down</span>{/if}
      </a>
    {:else}
      <span class="proj-title"><span class="who"><b>{workspace.scope}</b></span></span>
    {/if}
    <button class="btn collapse" data-variant="quiet" data-size="sm" data-icon-only aria-label="{workspace.rail ? 'Expand' : 'Collapse'} sidebar" title="{workspace.rail ? 'Expand' : 'Collapse'} sidebar (Ctrl+] b)" onclick={() => (workspace.rail = !workspace.rail)}>
      <Icon name={workspace.rail ? "chev-r" : "single"} size="sm" />
    </button>
    <button class="btn close-nav" data-variant="quiet" data-size="sm" data-icon-only aria-label="Close menu" onclick={() => (workspace.navOpen = false)}><Icon name="x" /></button>
  </div>
  <button class="goto" aria-label="Go to a project or agent" onclick={() => (app.palette = true)}>
    <Icon name="search" size="sm" /><span class="t">Go to…</span><span class="kbd">:</span>
  </button>

  <div class="side-scroll">
    {#if sections}
      {@const p = sections.project}
      <div class="side-list" style="padding-top:var(--space-2)">
        {#each sections.list as [tab, name, count]}
          {@const ic = ({ agents: "users", templates: "file", activity: "pulse", network: "swap", spec: "list", audit: "shield", members: "key", advanced: "more" } as Record<string, string>)[tab] ?? "list"}
          <a class="nav-item" href={href.project(p.id, tab)} title={name} aria-current={onPage && ((route.name === "project" && (route.tab === tab || (route.tab === "spec" && tab === "advanced"))) || (route.name === "agent" && tab === "agents")) ? "page" : undefined} onclick={showPage}>
            <span class="ic"><Icon name={ic} size="sm" /></span><span class="nm">{name}</span><span class="end"><span class="n">{count ?? ""}</span></span>
          </a>
        {/each}
      </div>
    {/if}

    <div class="side-h"><span class="label">Terminals</span><span class="count">{terms.length || ""}</span></div>
    <div class="side-list" role="listbox" aria-label="Open terminals">
      {#each terms as t, i (t.id)}
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
          title="{projectName(t.project)}/{t.agent}"
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
          <span class="what" data-t={wt}>{w}</span>
        </div>
      {:else}
        <p class="side-empty">No terminal open in this project. Use <b>Attach</b> on an agent; it opens here and stays open while you browse.</p>
      {/each}
    </div>
  </div>

<div class="side-foot">
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
