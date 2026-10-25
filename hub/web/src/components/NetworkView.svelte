<script lang="ts">
  import { api } from "../lib/api";
  import { errorText, toast } from "../lib/app.svelte";
  import { copyText } from "../lib/clipboard";
  import { ago, clock, stamp } from "../lib/format";
  import { poll } from "../lib/live";
  import { bytes, connections, duration, explain, sharedParents } from "../lib/network";
  import type { Denial, Network } from "../lib/types";

  // What the project's proxy recorded, or what it recorded for one agent: the hosts it denied, grouped, with why and how to fix
  // it, then the latest connections. The hub keeps the groups; the events are the latest it heard (roadmap/proxy-audit.md).
  let { engine, project, agent = "" }: { engine: string; project: string; agent?: string } = $props();

  let data = $state<Network | null>(null);
  let failure = $state("");
  $effect(() =>
    poll(async () => {
      try {
        data = await api.network(engine, project, agent);
        failure = "";
      } catch (e) {
        failure = errorText(e);
        throw e;
      }
    }, 3000),
  );

  let showFixed = $state(false);
  let expanded = $state<string | null>(null);
  let expandedConn = $state<string | null>(null);
  let deniedOnly = $state(false);
  let hostFilter = $state("");
  let agentFilter = $state("");

  const key = (d: Denial) => `${d.instance} ${d.protocol} ${d.host} ${d.code}`;
  const denials = $derived(data?.denials ?? []);
  const open = $derived(denials.filter((d) => !d.fixed));
  const fixed = $derived(denials.filter((d) => d.fixed));
  const shown = $derived(showFixed ? denials : open);
  const wildcards = $derived(sharedParents(open.filter((d) => d.code === "not-in-profile").map((d) => d.host)));
  const rows = $derived.by(() => {
    const needle = hostFilter.trim().toLowerCase();
    return connections(data?.events ?? []).filter(
      (c) =>
        (!deniedOnly || c.decision === "deny" || c.decision === "error") &&
        (!needle || c.host.toLowerCase().includes(needle) || c.server.toLowerCase().includes(needle)) &&
        (!agentFilter || c.agent === agentFilter),
    );
  });
  const agents = $derived([...new Set((data?.events ?? []).map((e) => e.agent).filter((a): a is string => !!a))].sort());

  const decisionKind = (d: string) => ({ allow: "done", deny: "error", error: "warn", challenge: "neutral" })[d] ?? "neutral";

  async function copy(text: string) {
    if (await copyText(text)) toast("Copied");
    else toast("Could not copy: select the text instead", "error");
  }
</script>

{#if data && data.feed.state !== "live"}
  <div class="banner" data-tone="info" role="status" data-testid="network-feed"><span class="tag">? Proxy log</span>
    <div class="body">{data.feed.state === "connecting" ? "Opening the proxy's log…" : data.feed.message} What the hub recorded before stays below.</div></div>
{/if}
{#if failure && !data}
  <div class="banner" data-tone="warn" role="alert"><span class="tag">! Network</span><div class="body">{failure}</div></div>
{/if}

{#if data}
  <section class="panel" aria-labelledby="denied-h">
    <header class="row">
      <h2 class="h-section" id="denied-h">Denied <span class="count">{open.length}</span></h2>
      <span class="grow"></span>
      {#if fixed.length}
        <label class="check"><input type="checkbox" bind:checked={showFixed} /> show fixed <span class="count">{fixed.length}</span></label>
      {/if}
    </header>
    {#each [...wildcards] as [parent, hosts]}
      <p class="hint" data-testid="wildcard">{hosts.length} hosts under <code>{parent}</code> were denied: <code>allow: ["*.{parent}"]</code> covers them all.</p>
    {/each}
    <div class="table-wrap">
      <table class="table" data-testid="denials">
        <thead><tr><th>Host</th>{#if !agent}<th>Agent</th>{/if}<th>Why</th><th>Times</th><th>Last</th><th></th></tr></thead>
        <tbody>
          {#each shown as d (key(d))}
            {@const e = explain(d)}
            <tr data-host={d.host} data-code={d.code} class:fixed={!!d.fixed}>
              <td><span class="name">{d.host}</span>{#if d.protocol === "ssh"} <span class="badge">ssh{#if d.server} {d.server}{/if}</span>{/if}</td>
              {#if !agent}<td>{d.agent || "–"}{#if !d.instance} <span class="muted" title="no credentials proved which agent it was">(unauthenticated)</span>{/if}</td>{/if}
              <td class="wrap"><span class="pill" data-s={d.decision === "deny" ? "error" : "warn"}>{e.title}</span>{#if d.fixed} <span class="pill" data-s="done" title={stamp(d.fixed)}>fixed</span>{/if}</td>
              <td>{d.count}</td>
              <td title={stamp(d.last)}>{ago(d.last) || clock(d.last)}</td>
              <td class="actions"><button class="btn" data-size="sm" aria-expanded={expanded === key(d)} onclick={() => (expanded = expanded === key(d) ? null : key(d))}>How to fix</button></td>
            </tr>
            {#if expanded === key(d)}
              <tr class="detail" data-testid="fix">
                <td colspan={agent ? 5 : 6}>
                  <p>{e.why}{#if d.profile} The agent's profile is <code>{d.profile}</code>.{/if}</p>
                  {#if d.text && d.text !== e.why}<p class="muted">The proxy said: {d.text}</p>{/if}
                  <p>{e.fix}</p>
                  {#if e.yaml}
                    <div class="snippet"><pre data-testid="fix-yaml">{e.yaml}</pre><button class="btn" data-size="sm" onclick={() => copy(e.yaml!)}>Copy</button></div>
                  {/if}
                  {#if e.command}
                    <div class="snippet"><pre>{e.command}</pre><button class="btn" data-size="sm" onclick={() => copy(e.command!)}>Copy</button></div>
                  {/if}
                  <p class="muted">First seen {stamp(d.first)}, last {stamp(d.last)}.</p>
                </td>
              </tr>
            {/if}
          {:else}
            <tr><td colspan={agent ? 5 : 6} class="muted">Nothing was denied{fixed.length ? " that is not fixed since" : ""}.</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  </section>

  <section class="panel" aria-labelledby="log-h">
    <header class="row">
      <h2 class="h-section" id="log-h">Connections</h2>
      <span class="grow"></span>
      <input class="input" data-size="sm" placeholder="Host" aria-label="Filter by host" bind:value={hostFilter} style="width:18ch" />
      {#if !agent && agents.length > 1}
        <select class="select" aria-label="Filter by agent" bind:value={agentFilter}>
          <option value="">every agent</option>
          {#each agents as a}<option value={a}>{a}</option>{/each}
        </select>
      {/if}
      <label class="check"><input type="checkbox" bind:checked={deniedOnly} /> denied and failed only</label>
    </header>
    <div class="table-wrap">
      <table class="table" data-testid="connections">
        <thead><tr><th>When</th>{#if !agent}<th>Agent</th>{/if}<th>Host</th><th>What</th><th>Decision</th><th>Traffic</th></tr></thead>
        <tbody>
          {#each rows as c (c.conn)}
            <tr data-conn={c.conn} data-host={c.host} onclick={() => (expandedConn = expandedConn === c.conn ? null : c.conn)} style="cursor:pointer">
              <td title={stamp(c.time)}>{clock(c.time)}</td>
              {#if !agent}<td>{#if c.agent}{c.agent}{:else if c.claimedAgent}<span class="muted" title="the name the client sent; its credentials were wrong">{c.claimedAgent}?</span>{:else}<span class="muted" title="no credentials">–</span>{/if}</td>{/if}
              <td><span class="name">{c.host}</span>{#if c.server} <span class="badge">ssh {c.server}</span>{/if}</td>
              <td class="wrap">{c.protocol}{#if c.mode} · {c.mode}{/if}{#if c.requests} · {c.requests} request{c.requests === 1 ? "" : "s"}{/if}{#if c.commands.length} · {c.commands.length} command{c.commands.length === 1 ? "" : "s"}{/if}</td>
              <td class="wrap">{#if c.decision}<span class="pill" data-s={decisionKind(c.decision)}>{c.decision}</span>{/if}{#if c.reason && c.decision !== "allow"} <span class="muted">{c.reason.text}</span>{/if}</td>
              <td>{#if c.durationMs !== null}{bytes(c.bytesUp)} ↑ {bytes(c.bytesDown)} ↓ · {duration(c.durationMs)}{:else}<span class="muted">open</span>{/if}</td>
            </tr>
            {#if expandedConn === c.conn}
              <tr class="detail">
                <td colspan={agent ? 5 : 6}>
                  <p class="from" data-testid="from">
                    {#if c.client}From <code>{c.client}</code>{/if}{#if c.userAgent}{c.client ? ", " : ""}<span title="what the client says it is, not proof">{c.userAgent}</span>{/if}
                    {#if !c.agent && c.claimedAgent} · claimed to be <b>{c.claimedAgent}</b>, not proved{:else if !c.agent} · no agent proved{/if}
                  </p>
                  <ol class="steps">
                    {#each c.steps as s}
                      <li><span class="muted">{clock(s.time)}</span> <b>{s.event}</b>{#if s.decision} {s.decision}{/if}{#if s.mode} ({s.mode}){/if}{#if s.method} {s.method} {s.path}{/if}{#if s.status} → {s.status}{/if}{#if s.command} <code>{s.command}</code>{/if}{#if s.reason} <span class="muted">[{s.reason.code}] {s.reason.text}</span>{/if}</li>
                    {/each}
                  </ol>
                </td>
              </tr>
            {/if}
          {:else}
            <tr><td colspan={agent ? 5 : 6} class="muted">No connection{deniedOnly || hostFilter || agentFilter ? " matches" : " heard yet"}.</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
    <p class="muted" style="font-size:var(--text-xs);margin:0">The latest connections the hub heard. Further back, <code>egzo proxy log</code> reads the proxy's own log, as far as the engine keeps it.</p>
  </section>
{:else if !failure}
  <p class="muted">Loading…</p>
{/if}

<style>
  header.row { display: flex; gap: var(--space-2); align-items: center; flex-wrap: wrap; padding: var(--space-3); }
  .check { display: inline-flex; gap: 6px; align-items: center; font-size: var(--text-sm); }
  .hint { margin: 0 var(--space-3) var(--space-2); font-size: var(--text-sm); }
  tr.fixed td { opacity: 0.65; }
  tr.detail td { white-space: normal; background: var(--surface-2); }
  tr.detail p { margin: 0 0 var(--space-2); }
  .snippet { display: flex; gap: var(--space-2); align-items: flex-start; margin-bottom: var(--space-2); }
  .snippet pre { margin: 0; padding: var(--space-2); background: var(--surface-1); border: 1px solid var(--border); border-radius: var(--radius-1); overflow-x: auto; }
  .from { margin: 0 0 var(--space-2); font-size: var(--text-sm); }
  .steps { margin: 0; padding-left: var(--space-4); font-size: var(--text-sm); }
  section.panel { margin-bottom: var(--space-4); }
</style>
