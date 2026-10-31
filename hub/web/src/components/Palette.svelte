<script lang="ts">
  import { onMount } from "svelte";
  import { app, seesLinks, toggleTheme } from "../lib/app.svelte";
  import { href, navigate } from "../lib/router.svelte";
  import { cycleLayout, openTerm, showPage, workspace } from "../lib/workspace.svelte";
  import { toast } from "../lib/app.svelte";

  interface Item { group: string; label: string; hint?: string; run: () => void }

  let query = $state("");
  let selected = $state(0);
  let input: HTMLInputElement;

  const items = $derived.by(() => {
    const out: Item[] = [];
    out.push({ group: "Go to", label: "All projects", run: () => { showPage(); navigate(href.home()); } });
    if (seesLinks()) out.push({ group: "Go to", label: "Links", run: () => { showPage(); navigate(href.links()); } });
    if (app.user?.isOperator) {
      out.push({ group: "Go to", label: "Invites", run: () => { showPage(); navigate(href.invites()); } });
      out.push({ group: "Go to", label: "Users", run: () => { showPage(); navigate(href.users()); } });
    }
    out.push({ group: "Go to", label: "Your account", run: () => { showPage(); navigate(href.account()); } });
    for (const p of app.projects) {
      out.push({ group: "Projects", label: p.name, hint: p.host, run: () => { showPage(); navigate(href.project(p.id)); } });
      for (const a of p.agents) {
        out.push({
          group: "Agents", label: `${p.name}/${a.name}`, hint: p.host + " · " + (a.running ? "attach" : "stopped"),
          run: () => {
            if (a.running && openTerm(p.id, a.name)) return;
            if (a.running) toast("Too many terminals are open. Close one first.", "error");
            else { showPage(); navigate(href.agent(p.id, a.name)); }
          },
        });
      }
    }
    out.push({ group: "Actions", label: "Switch theme", run: toggleTheme });
    out.push({ group: "Actions", label: workspace.rail ? "Expand the sidebar" : "Collapse the sidebar", run: () => (workspace.rail = !workspace.rail) });
    out.push({ group: "Actions", label: "Change the terminal layout", run: cycleLayout });
    out.push({ group: "Actions", label: "Keyboard shortcuts", run: () => (app.keyref = true) });
    return out;
  });

  const shown = $derived.by(() => {
    const q = query.trim().toLowerCase();
    return q ? items.filter((i) => (i.label + " " + (i.hint ?? "")).toLowerCase().includes(q)) : items;
  });
  $effect(() => {
    void query;
    selected = 0;
  });

  function close() {
    app.palette = false;
  }
  function run(item: Item | undefined) {
    close();
    item?.run();
  }
  function keydown(e: KeyboardEvent) {
    if (e.key === "Escape") { e.preventDefault(); close(); }
    else if (e.key === "ArrowDown") { e.preventDefault(); selected = Math.min(shown.length - 1, selected + 1); }
    else if (e.key === "ArrowUp") { e.preventDefault(); selected = Math.max(0, selected - 1); }
    else if (e.key === "Enter") { e.preventDefault(); run(shown[selected]); }
  }
  onMount(() => {
    const before = document.activeElement as HTMLElement | null;
    input.focus();
    return () => before?.focus?.();
  });
</script>

<!-- svelte-ignore a11y_click_events_have_key_events -->
<div class="scrim" role="presentation" style="place-items:start center;padding-top:12vh" onmousedown={(e) => e.target === e.currentTarget && close()}>
  <div class="palette" style="position:static;transform:none" role="dialog" aria-modal="true" aria-label="Command menu">
    <div class="q"><span>:</span><input bind:this={input} bind:value={query} onkeydown={keydown} placeholder="Go to a project or agent, or run an action" aria-label="Command" role="combobox" aria-expanded="true" aria-controls="palette-list" /></div>
    <div class="list" id="palette-list" role="listbox">
      {#each shown as item, i}
        {#if i === 0 || shown[i - 1]!.group !== item.group}<div class="grp label">{item.group}</div>{/if}
        <div role="option" aria-selected={i === selected} class="menu-item" tabindex="-1" onmouseenter={() => (selected = i)} onclick={() => run(item)} onkeydown={() => {}}>
          {item.label}{#if item.hint}<small>{item.hint}</small>{/if}
        </div>
      {:else}
        <div class="menu-item muted">Nothing matches.</div>
      {/each}
    </div>
  </div>
</div>
