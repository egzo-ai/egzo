<script lang="ts">
  import { api } from "../lib/api";
  import { app, errorText, multiEngine, toast } from "../lib/app.svelte";
  import { operate } from "../lib/agentops";
  import { activityKind, activityLabel, initials } from "../lib/format";
  import { poll } from "../lib/live";
  import { href, navigate, type ProjectTab } from "../lib/router.svelte";
  import type { AuditEntry, Members, Project, TimelineEvent } from "../lib/types";
  import { openTerm, workspace } from "../lib/workspace.svelte";
  import AuditTable from "../components/AuditTable.svelte";
  import EngineBadge from "../components/EngineBadge.svelte";
  import Icon from "../components/Icon.svelte";
  import MembersTable from "../components/MembersTable.svelte";
  import Modal from "../components/Modal.svelte";
  import Pill from "../components/Pill.svelte";
  import SpecView from "../components/SpecView.svelte";
  import Timeline from "../components/Timeline.svelte";
  import ErrorPage from "./ErrorPage.svelte";

  let { engine, name, tab }: { engine: string; name: string; tab: ProjectTab } = $props();

  let project = $state<Project | null>(null);
  let failure = $state<unknown>(null);
  let events = $state<TimelineEvent[] | null>(null);
  let spec = $state<string | null>(null);
  let audit = $state<AuditEntry[] | null>(null);
  let members = $state<Members | null>(null);
  let spawning = $state(false);
  let spawnForm = $state({ template: "", name: "" });
  let spawnError = $state("");

  const multi = $derived(multiEngine());

  async function loadProject() {
    try {
      project = await api.project(engine, name);
      failure = null;
    } catch (e) {
      if (!project) failure = e;
      else throw e;
    }
  }
  $effect(() => poll(loadProject));
  $effect(() => {
    if (tab !== "timeline") return;
    return poll(async () => {
      events = await api.events(engine, name, "", 60);
    });
  });
  $effect(() => {
    if (tab === "spec") void api.spec(engine, name).then((t) => (spec = t)).catch((e) => toast(errorText(e), "error"));
  });
  async function loadAudit() {
    audit = await api.audit(engine, name);
  }
  async function loadMembers() {
    members = await api.members(engine, name);
  }
  $effect(() => {
    if (tab === "audit") return poll(loadAudit, 5000);
    if (tab === "members") return poll(loadMembers, 5000);
  });

  const agents = $derived(project?.agents ?? []);
  const running = $derived(agents.filter((a) => a.running).length);
  const waiting = $derived(agents.filter((a) => a.waiting).length);
  const tabs = $derived.by(() => {
    const list: [ProjectTab, string, number | null][] = [["agents", "Agents", agents.length], ["services", "Services", project?.services.length ?? null], ["timeline", "Timeline", null], ["spec", "Spec", null]];
    if (project?.canAudit) list.push(["audit", "Audit log", null]);
    if (project?.canManage) list.push(["members", "Members", null]);
    return list;
  });

  function openSpawn() {
    spawnForm = { template: project?.templates[0] ?? "", name: "" };
    spawnError = "";
    spawning = true;
  }

  async function spawn(e: Event) {
    e.preventDefault();
    spawnError = "";
    try {
      const { agent } = await api.spawn(engine, name, spawnForm.template, spawnForm.name);
      spawning = false;
      toast(`Spawned ${agent}`);
      navigate(href.agent(engine, name, agent));
    } catch (err) {
      spawnError = errorText(err);
    }
  }

  async function act(agent: string, verb: "start" | "stop" | "rm") {
    if (await operate(engine, name, agent, verb)) await loadProject();
  }

  function attach(agent: string) {
    if (!openTerm(engine, name, agent)) toast("Too many terminals are open. Close one first.", "error");
  }
</script>

{#if failure}
  <ErrorPage error={failure} />
{:else if project}
  <div class="page">
    <nav class="crumbs" aria-label="Breadcrumb"><a href={href.home()}>Projects</a> /</nav>
    <div class="phead">
      <h1 class="h-page">{project.name}</h1>
      {#if multi}<EngineBadge name={project.engine} />{/if}
      <span class="badge" title="your role">{project.role}</span>
      <span class="grow"></span>
      {#if project.canSpawn}<button class="btn" data-variant="primary" data-key="s" aria-label="Spawn agent" aria-keyshortcuts="s" onclick={openSpawn} disabled={!project.up}>Spawn agent <span class="kbd">s</span></button>{/if}
    </div>
    <div class="metaline">
      <span><span class="k">agents</span> {running} running{#if waiting}&nbsp;· {waiting} waiting{/if}&nbsp;· {agents.length - running} stopped</span>
      <span class="k">services</span>
      {#each project.services as s}<span><span class="dot" data-s={s.state === "running" ? "running" : "stopped"}></span> {s.name}</span>{/each}
      {#if project.templates.length}<span><span class="k">templates</span> {project.templates.join(", ")}</span>{/if}
    </div>

    {#if !project.up}
      <div class="banner" data-tone="warn" role="alert"><span class="tag">! Sidecars down</span>
        <div class="body">The project's control or proxy sidecar is not running, so agents cannot be spawned. Run <code>egzo project up</code>.</div></div>
    {/if}

    <div class="tabs" role="tablist" aria-label="Project sections">
      {#each tabs as [id, label, count]}
        <a class="tab" role="tab" href={href.project(engine, name, id)} aria-selected={tab === id}>{label}{#if count !== null} <span class="count">{count}</span>{/if}</a>
      {/each}
    </div>

    {#if tab === "agents"}
      {#if agents.length}
        <div class="table-wrap">
          <table class="table" data-testid="agents">
            <thead><tr><th>Agent</th><th>Activity</th><th>Status</th><th>Terminal</th><th>Spawned by</th><th></th></tr></thead>
            <tbody>
              {#each agents as a (a.name)}
                {@const open = workspace.terms.some((t) => t.engine === engine && t.project === name && t.agent === a.name)}
                <tr data-row tabindex="-1" data-agent={a.name} aria-selected={open}>
                  <td><a class="name" href={href.agent(engine, name, a.name)}>{a.name}</a> <span class="muted">{a.template}</span>{#if a.stale} <span class="pill" data-s="warn" title="the template changed after this agent was made">stale</span>{/if}</td>
                  <td><Pill kind={activityKind(a.activity, a.waiting)} label={activityLabel(a.activity, a.waiting)} /></td>
                  <td class="wrap" style="white-space:normal;min-width:18ch"><span class="status" style:color={a.waiting ? "var(--status-waiting)" : undefined}>{a.status}</span></td>
                  <td>{#if a.holder}<span class="presence" data-live><span class="avatar" data-me={a.holder === app.user?.name ? "" : undefined}>{initials(a.holder)}</span>{a.holder === app.user?.name ? "you" : a.holder}</span>{:else}<span class="muted">free</span>{/if}</td>
                  <td>{a.creator}</td>
                  <td class="actions">
                    {#if a.canOperate}
                      <span class="row-actions">
                        <button class="btn" data-size="sm" data-key="a" aria-label="{open ? 'Open terminal of' : 'Attach'} {a.name}" disabled={!a.running} onclick={() => attach(a.name)}><Icon name="attach" size="sm" />{open ? "Open terminal" : "Attach"}</button>
                        {#if a.running}<button class="btn" data-size="sm" data-icon-only aria-label="Stop {a.name}" title="Stop" onclick={() => act(a.name, "stop")}><Icon name="stop" size="sm" /></button>
                        {:else}<button class="btn" data-size="sm" data-icon-only aria-label="Start {a.name}" title="Start" onclick={() => act(a.name, "start")}><Icon name="play" size="sm" /></button>{/if}
                        <button class="btn" data-variant="danger" data-size="sm" data-icon-only aria-label="Remove {a.name}" title="Remove" onclick={() => act(a.name, "rm")}><Icon name="trash" size="sm" /></button>
                      </span>
                    {/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
        <p class="muted" style="font-size:var(--text-xs);margin:0"><span class="kbd">j</span> <span class="kbd">k</span> move · <span class="kbd">Enter</span> open · <span class="kbd">a</span> attach · <span class="kbd">s</span> spawn</p>
      {:else}
        <div class="empty"><h2>No agent yet</h2><p>Spawn one from a template{#if project.templates.length === 0}. The project has published no template: run <code>egzo project up</code> on the project{/if}.</p></div>
      {/if}
    {:else if tab === "services"}
      <div class="table-wrap">
        <table class="table" data-testid="services">
          <thead><tr><th>Service</th><th>State</th><th>Health</th></tr></thead>
          <tbody>
            {#each project.services as s}
              <tr><td>{s.name}</td><td><Pill kind={s.state === "running" ? "running" : "stopped"} label="{s.name}: {s.state}" /></td><td>{s.health || "–"}</td></tr>
            {/each}
          </tbody>
        </table>
      </div>
    {:else if tab === "timeline"}
      <Timeline {events} />
    {:else if tab === "spec"}
      {#if spec === null}<p class="muted">Loading…</p>{:else}<SpecView text={spec} />{/if}
    {:else if tab === "audit"}
      {#if audit}<AuditTable entries={audit} />{:else}<p class="muted">Loading…</p>{/if}
    {:else if tab === "members"}
      {#if members}<MembersTable {engine} project={name} data={members} reload={loadMembers} />{:else}<p class="muted">Loading…</p>{/if}
    {/if}
  </div>

  {#if spawning}
    <Modal title="Spawn an agent" onclose={() => (spawning = false)}>
      <form id="spawn-form" onsubmit={spawn} style="display:grid;gap:var(--space-3)">
        {#if project.templates.length === 0}
          <p class="muted" style="margin:0">The project has published no template. Run <code>egzo project up</code> on the project.</p>
        {:else}
          <div class="field"><label for="sp-template">Template</label>
            <select id="sp-template" class="select" bind:value={spawnForm.template}>{#each project.templates as t}<option value={t}>{t}</option>{/each}</select></div>
          <div class="field"><label for="sp-name">Name</label>
            <input id="sp-name" class="input" bind:value={spawnForm.name} placeholder="(automatic)" pattern="[A-Za-z0-9][A-Za-z0-9._\-]*" maxlength="63" />
            <span class="help">Optional. Letters, digits, . - and _.</span></div>
        {/if}
        {#if spawnError}<div class="field-error" role="alert">{spawnError}</div>{/if}
      </form>
      {#snippet footer()}
        <button class="btn" onclick={() => (spawning = false)}>Cancel</button>
        <button class="btn" data-variant="primary" type="submit" form="spawn-form" disabled={!project?.templates.length}>Spawn</button>
      {/snippet}
    </Modal>
  {/if}
{:else}
  <div class="page"><p class="muted">Loading…</p></div>
{/if}
