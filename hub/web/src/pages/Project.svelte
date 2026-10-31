<script lang="ts">
  import { api } from "../lib/api";
  import { app, errorText, refreshProjects, toast } from "../lib/app.svelte";
  import { operate } from "../lib/agentops";
  import { ask } from "../lib/confirm.svelte";
  import { activityKind, activityLabel, ago, initials, stamp, taskDoing, taskFailedLabel } from "../lib/format";
  import { poll } from "../lib/live";
  import { suggestName } from "../lib/names";
  import { href, nav, navigate, type ProjectTab } from "../lib/router.svelte";
  import { followTask, onScreen } from "../lib/tasks";
  import type { AuditEntry, Invite, Members, Project, Task, TimelineEvent } from "../lib/types";
  import { openTerm, workspace } from "../lib/workspace.svelte";
  import AuditTable from "../components/AuditTable.svelte";
  import Icon from "../components/Icon.svelte";
  import MembersTable from "../components/MembersTable.svelte";
  import Modal from "../components/Modal.svelte";
  import NetworkView from "../components/NetworkView.svelte";
  import Pill from "../components/Pill.svelte";
  import SpecView from "../components/SpecView.svelte";
  import TaskTable from "../components/TaskTable.svelte";
  import Timeline from "../components/Timeline.svelte";
  import ErrorPage from "./ErrorPage.svelte";

  let { id, tab }: { id: string; tab: ProjectTab } = $props();

  let project = $state<Project | null>(null);
  const offline = $derived(!!project?.offlineSince);
  let failure = $state<unknown>(null);
  let events = $state<TimelineEvent[] | null>(null);
  let spec = $state<string | null>(null);
  let audit = $state<AuditEntry[] | null>(null);
  let members = $state<Members | null>(null);
  let invites = $state<Invite[]>([]);
  let activity = $state<Task[] | null>(null);
  let spawning = $state(false);
  let spawnForm = $state({ template: "", name: "", suggested: "" });
  let spawnError = $state("");
  let submitting = $state(false);

  async function loadProject() {
    try {
      project = await api.project(id);
      failure = null;
    } catch (e) {
      if (!project) failure = e;
      else throw e;
    }
  }
  $effect(() => poll(loadProject));
  $effect(() => {
    // read from the control sidecar, which runs a command in it: slowly. A project that is offline has nobody to ask.
    if (tab !== "timeline" || offline) return;
    return poll(async () => {
      events = await api.events(id, "", 60);
    }, 15000);
  });
  $effect(() => {
    if (tab === "spec") void api.spec(id).then((t) => (spec = t)).catch((e) => toast(errorText(e), "error"));
  });
  async function loadAudit() {
    audit = await api.audit(id);
  }
  async function loadMembers() {
    [members, invites] = await Promise.all([api.members(id), project?.canManage ? api.projectInvites(id) : Promise.resolve([])]);
  }
  async function loadActivity() {
    activity = await api.tasks(id);
  }
  $effect(() => {
    if (tab === "activity") return poll(loadActivity, 5000);
    if (tab === "audit") return poll(loadAudit, 5000);
    if (tab === "members") return poll(loadMembers, 5000);
  });

  // an agent the project does not have (yet, or any more) is in the list while a task runs on it, and a spawn that failed until it is
  // dismissed: such an agent has a task and no state
  const agents = $derived(project?.agents ?? []);
  const running = $derived(agents.filter((a) => a.running).length);
  const waiting = $derived(agents.filter((a) => a.waiting).length);
  const tabs = $derived.by(() => {
    const list: [ProjectTab, string, number | null][] = [["agents", "Agents", agents.length], ["services", "Services", project?.services.length ?? null]];
    if (project?.role !== "overseer") list.push(["timeline", "Timeline", null], ["activity", "Activity", null], ["network", "Network", null], ["spec", "Spec", null]);
    if (project?.canAudit) list.push(["audit", "Audit log", null]);
    if (project?.canMembers) list.push(["members", "Members", null]);
    return list;
  });

  function suggest(template: string): string {
    return suggestName(template, agents.map((a) => a.name));
  }

  function openSpawn() {
    const template = project?.templates[0] ?? "";
    const suggested = suggest(template);
    spawnForm = { template, name: suggested, suggested };
    spawnError = "";
    spawning = true;
  }

  // a suggestion follows the template; a name the user typed stays
  function pickTemplate() {
    if (spawnForm.name !== spawnForm.suggested) return;
    spawnForm.suggested = spawnForm.name = suggest(spawnForm.template);
  }

  function anotherName() {
    spawnForm.suggested = spawnForm.name = suggest(spawnForm.template);
  }

  // The hub answers at once and runs the spawn as a task; the agent is in the project from then on. Its page shows the steps, and
  // opens its terminal when it is ready, if the user is still there.
  async function spawn(e: Event) {
    e.preventDefault();
    spawnError = "";
    submitting = true;
    try {
      const task = await api.spawn(id, spawnForm.template, spawnForm.name.trim());
      spawning = false;
      followTask(id, task, (ended) => {
        if (ended.state === "done" && onScreen(id, ended.agent) && nav.route.name === "agent") openTerm(id, ended.agent);
      });
      navigate(href.agent(id, task.agent));
    } catch (err) {
      spawnError = errorText(err);
    } finally {
      submitting = false;
    }
  }

  async function dismiss(task: Task) {
    try {
      await api.dismissTask(id, task.id);
      await loadProject();
    } catch (err) {
      toast(errorText(err), "error");
    }
  }

  async function act(agent: string, verb: "start" | "stop" | "rm") {
    if (await operate(id, agent, verb)) await loadProject();
  }

  async function unlink() {
    if (!project) return;
    const ok = await ask({
      title: `Unlink ${project.name}?`,
      lines: ["The hub forgets it, with everyone's role on it. A new link code brings it back as a new project.", `It keeps running on ${project.host || "its machine"}: only egzo project down, there, stops it.`],
      confirm: "Unlink", typed: project.name,
    });
    if (!ok) return;
    try {
      await api.unlinkProject(id);
      toast(`Unlinked ${project.name}`);
      await refreshProjects();
      navigate(href.home());
    } catch (e) {
      toast(errorText(e), "error");
    }
  }

  function attach(agent: string) {
    if (!openTerm(id, agent)) toast("Too many terminals are open. Close one first.", "error");
  }
</script>

{#if failure}
  <ErrorPage error={failure} />
{:else if project}
  <div class="page">
    <nav class="crumbs" aria-label="Breadcrumb"><a href={href.home()}>Projects</a> /</nav>
    <div class="phead">
      <h1 class="h-page">{project.name}</h1>
      <span class="muted" data-testid="host">{project.host}</span>
      {#if project.role === "overseer"}<span class="badge" title="You oversee it as an operator, with no role on it">operator</span>{:else}<span class="badge" title="your role">{project.role}</span>{/if}{#if project.ownerless} <span class="pill" data-s="warn" title="nobody who can sign in owns it">no owner</span>{/if}
      <span class="grow"></span>
      {#if project.canSpawn}<button class="btn" data-variant="primary" data-key="s" aria-label="Spawn agent" aria-keyshortcuts="s" onclick={openSpawn} disabled={!project.up}>Spawn agent <span class="kbd">s</span></button>{/if}
    </div>
    <div class="metaline">
      <span><span class="k">agents</span> {running} running{#if waiting}&nbsp;· {waiting} waiting{/if}&nbsp;· {agents.length - running} stopped</span>
      <span class="k">services</span>
      {#each project.services as s}<span><span class="dot" data-s={s.state === "running" ? "running" : "stopped"}></span> {s.name}</span>{/each}
      {#if project.templates.length}<span><span class="k">templates</span> {project.templates.join(", ")}</span>{/if}
    </div>

    {#if project.offlineSince}
      <div class="banner" data-tone="unreachable" role="status" data-testid="offline"><span class="tag">⌁ Offline since <span title={stamp(project.offlineSince)}>{ago(project.offlineSince)}</span></span>
        <div class="body">The project's link is not connected, so this is what the hub last heard. Nothing can be done on it from here until <code>egzo project up</code> runs on {project.host || "its machine"}.</div></div>
    {/if}

    {#if (project.feed.state === "unavailable" || project.feed.state === "stale") && agents.some((a) => a.running)}
      <div class="banner" data-tone="info" role="status" data-testid="feed-banner"><span class="tag">? Agent status unavailable</span>
        <div class="body">What the agents are doing cannot be read, so they show as running. {project.feed.message}</div></div>
    {/if}

    {#if !project.up && !project.offlineSince}
      <div class="banner" data-tone="warn" role="alert"><span class="tag">! Sidecars down</span>
        <div class="body">The project's control or proxy sidecar is not running, so agents cannot be spawned. Run <code>egzo project up</code>.</div></div>
    {/if}

    <div class="tabs" role="tablist" aria-label="Project sections">
      {#each tabs as [section, label, count]}
        <a class="tab" role="tab" href={href.project(id, section)} aria-selected={tab === section}>{label}{#if count !== null} <span class="count">{count}</span>{/if}</a>
      {/each}
    </div>

    {#if tab === "agents"}
      {#if agents.length}
        <div class="table-wrap">
          <table class="table" data-testid="agents">
            <thead><tr><th>Agent</th><th>Activity</th><th>Status</th><th>Terminal</th><th>Spawned by</th><th></th></tr></thead>
            <tbody>
              {#each agents as a (a.name)}
                {@const open = workspace.terms.some((t) => t.project === id && t.agent === a.name)}
                {@const busy = a.task?.state === "running"}
                {@const made = a.state !== ""}
                <tr data-row tabindex="-1" data-agent={a.name} aria-selected={open} aria-busy={busy}>
                  <td><a class="name" href={href.agent(id, a.name)}>{a.name}</a> <span class="muted">{a.template}</span>{#if a.stale} <span class="pill" data-s="warn" title="the template changed after this agent was made">stale</span>{/if}{#if a.denied} <a class="pill" data-s="error" data-testid="denied" href={href.agent(id, a.name, "network")} title="hosts the proxy refused this agent in the last hour">{a.denied} denied</a>{/if}</td>
                  <td>
                    {#if made}<Pill kind={activityKind(a.activity, a.waiting)} label={activityLabel(a.activity, a.waiting)} />{/if}
                    {#if a.task}{#if busy}<Pill kind="starting" label={taskDoing(a.task.verb)} testid="task" />{:else}<Pill kind="error" label={taskFailedLabel(a.task.verb, a.task.state)} testid="task" />{/if}{/if}
                  </td>
                  <td class="wrap" style="white-space:normal;min-width:18ch">
                    {#if a.task && busy}<span class="muted" data-testid="task-step">{a.task.step || "…"} · {a.task.creator}</span>
                    {:else if a.task}<span class="status" style:color="var(--status-error)" data-testid="task-error">{a.task.error}</span>
                    {:else}<span class="status" style:color={a.waiting ? "var(--status-waiting)" : undefined}>{a.status}</span>{/if}
                  </td>
                  <td>{#if !made}<span class="muted">—</span>{:else if a.holder}<span class="presence" data-live><span class="avatar" data-me={a.holder === app.user?.username ? "" : undefined}>{initials(a.holder)}</span>{a.holder === app.user?.username ? "you" : a.holder}</span>{:else}<span class="muted">free</span>{/if}</td>
                  <td>{a.creator}</td>
                  <td class="actions">
                    <span class="row-actions">
                      {#if a.task?.canDismiss}<button class="btn" data-size="sm" data-icon-only aria-label="Dismiss the failure on {a.name}" title="Dismiss" onclick={() => dismiss(a.task!)}><Icon name="x" size="sm" /></button>{/if}
                      {#if made && a.canOperate && !project.offlineSince}
                        <button class="btn" data-size="sm" data-key="a" aria-label="{open ? 'Open terminal of' : 'Attach'} {a.name}" disabled={!a.running || busy} onclick={() => attach(a.name)}><Icon name="attach" size="sm" />{open ? "Open terminal" : "Attach"}</button>
                        {#if a.running}<button class="btn" data-size="sm" data-icon-only aria-label="Stop {a.name}" title="Stop" disabled={busy} onclick={() => act(a.name, "stop")}><Icon name="stop" size="sm" /></button>
                        {:else}<button class="btn" data-size="sm" data-icon-only aria-label="Start {a.name}" title="Start" disabled={busy} onclick={() => act(a.name, "start")}><Icon name="play" size="sm" /></button>{/if}
                        <button class="btn" data-variant="danger" data-size="sm" data-icon-only aria-label="Remove {a.name}" title="Remove" disabled={busy} onclick={() => act(a.name, "rm")}><Icon name="trash" size="sm" /></button>
                      {/if}
                    </span>
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
      {#if offline && !events}<p class="muted" data-testid="timeline-offline">The timeline is read from the project, and it is offline.</p>{:else}<Timeline {events} />{/if}
    {:else if tab === "activity"}
      {#if activity}<TaskTable project={id} tasks={activity} />{:else}<p class="muted">Loading…</p>{/if}
    {:else if tab === "network"}
      <NetworkView project={id} />
    {:else if tab === "spec"}
      {#if spec === null}<p class="muted">Loading…</p>{:else}<SpecView text={spec} />{/if}
    {:else if tab === "audit"}
      {#if audit}<AuditTable entries={audit} />{:else}<p class="muted">Loading…</p>{/if}
    {:else if tab === "members"}
      {#if members}<MembersTable {project} data={members} invites={invites} reload={loadMembers} />{:else}<p class="muted">Loading…</p>{/if}
      {#if project.canUnlink}
        <section class="panel" aria-labelledby="unlink">
          <header><h2 class="h-section" id="unlink">Unlink</h2></header>
          <div class="pad" style="display:grid;gap:var(--space-3)">
            <p class="muted" style="margin:0">The hub forgets the project: everyone loses their role, and its terminals end. The project keeps running on {project.host || "its machine"}, until somebody runs <code>egzo project down</code> there.</p>
            <div class="btn-group"><button class="btn" data-variant="danger" onclick={unlink}>Unlink {project.name}</button></div>
          </div>
        </section>
      {/if}
    {/if}
  </div>

  {#if spawning}
    <Modal title="Spawn an agent" onclose={() => (spawning = false)}>
      <form id="spawn-form" onsubmit={spawn} style="display:grid;gap:var(--space-3)">
        {#if project.templates.length === 0}
          <p class="muted" style="margin:0">The project has published no template. Run <code>egzo project up</code> on the project.</p>
        {:else}
          <div class="field"><label for="sp-template">Template</label>
            <select id="sp-template" class="select" bind:value={spawnForm.template} onchange={pickTemplate}>{#each project.templates as t}<option value={t}>{t}</option>{/each}</select></div>
          <div class="field"><label for="sp-name">Name</label>
            <span style="display:flex;gap:var(--space-2)">
              <input id="sp-name" class="input" style="flex:1" bind:value={spawnForm.name} required pattern="[a-z0-9][a-z0-9_\-]*" maxlength="63" />
              <button type="button" class="btn" data-icon-only aria-label="Another suggestion" title="Another suggestion" onclick={anotherName}><Icon name="refresh" /></button>
            </span>
            <span class="help">Lowercase letters, digits, - and _. The name stays with the agent's branch, even after it is removed.</span></div>
        {/if}
        {#if spawnError}<div class="field-error" role="alert">{spawnError}</div>{/if}
      </form>
      {#snippet footer()}
        <button class="btn" onclick={() => (spawning = false)}>Cancel</button>
        <button class="btn" data-variant="primary" type="submit" form="spawn-form" disabled={!project?.templates.length || submitting} aria-busy={submitting}>Spawn</button>
      {/snippet}
    </Modal>
  {/if}
{:else}
  <div class="page"><p class="muted">Loading…</p></div>
{/if}
