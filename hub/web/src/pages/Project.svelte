<script lang="ts">
  import { api } from "../lib/api";
  import Previews from "../components/Previews.svelte";
  import { app, errorText, refreshProjects, toast } from "../lib/app.svelte";
  import { operate } from "../lib/agentops";
  import { ask } from "../lib/confirm.svelte";
  import { activityKind, activityLabel, ago, initials, stamp, taskDoing, taskFailedLabel } from "../lib/format";
  import { poll } from "../lib/live";
  import { suggestName } from "../lib/names";
  import { href, nav, navigate, type ProjectTab } from "../lib/router.svelte";
  import { followTask, onScreen } from "../lib/tasks";
  import type { Agent, AuditEntry, Invite, Members, Project, Task, Template } from "../lib/types";
  import { openTerm, roomMade, workspace } from "../lib/workspace.svelte";
  import AuditTable from "../components/AuditTable.svelte";
  import Icon from "../components/Icon.svelte";
  import MembersTable from "../components/MembersTable.svelte";
  import Modal from "../components/Modal.svelte";
  import NetworkView from "../components/NetworkView.svelte";
  import Pill from "../components/Pill.svelte";
  import SpecView from "../components/SpecView.svelte";
  import TaskTable from "../components/TaskTable.svelte";
  import ErrorPage from "./ErrorPage.svelte";

  let { id, tab }: { id: string; tab: ProjectTab } = $props();

  let project = $state<Project | null>(null);
  const offline = $derived(!!project?.offlineSince);
  let failure = $state<unknown>(null);
  let spec = $state<string | null>(null);
  let audit = $state<AuditEntry[] | null>(null);
  let members = $state<Members | null>(null);
  let invites = $state<Invite[]>([]);
  let activity = $state<Task[] | null>(null);
  let spawning = $state(false);
  let spawnForm = $state({ template: "", name: "", suggested: "" });
  let spawnError = $state("");
  let templates = $state<Template[] | null>(null);
  let templatesError = $state("");
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
  const readsSpec = $derived(tab === "spec" && !!project && project.role !== "overseer");
  $effect(() => {
    if (readsSpec) void api.spec(id).then((t) => (spec = t)).catch((e) => toast(errorText(e), "error"));
  });
  // what each template gives an agent comes from the project's spec snapshot: read again when the project publishes other templates
  const templateKey = $derived(project && project.role !== "overseer" && !offline ? project.templates.join(",") : null);
  $effect(() => {
    if (templateKey === null) return;
    api.templates(id).then((t) => { templates = t; templatesError = ""; }).catch((e) => (templatesError = errorText(e)));
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
  // the sections are in the left nav; the page only says which one it is
  const tabs = $derived.by(() => {
    const list: [ProjectTab, string, number | null][] = [["agents", "Agents", agents.length], ["templates", "Templates", project?.templates.length ?? null]];
    if (project?.role !== "overseer") list.push(["activity", "Activity", null], ["network", "Network", null]);
    if (project?.canAudit) list.push(["audit", "Audit log", null]);
    if (project?.canMembers) list.push(["members", "Members", null]);
    list.push(["advanced", "Advanced", null]);
    return list;
  });

  // the agents tab is by template: each published one, then the templates agents were made from that the project no longer has
  const groups = $derived.by(() => {
    const names = [...(project?.templates ?? [])];
    for (const a of agents) if (a.template && !names.includes(a.template)) names.push(a.template);
    const out = names.map((name) => ({
      name,
      published: project?.templates.includes(name) ?? false,
      detail: templates?.find((t) => t.name === name),
      agents: agents.filter((a) => a.template === name),
    }));
    const loose = agents.filter((a) => !a.template);
    if (loose.length) out.push({ name: "", published: false, detail: undefined, agents: loose });
    return out;
  });

  function suggest(template: string): string {
    return suggestName(template, agents.map((a) => a.name));
  }

  function openSpawn(chosen?: string) {
    const template = chosen ?? project?.templates[0] ?? "";
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
    roomMade(openTerm(id, agent));
  }
</script>

{#snippet row(a: Agent)}
  {@const open = workspace.terms.some((t) => t.project === id && t.agent === a.name)}
  {@const busy = a.task?.state === "running"}
  {@const made = a.state !== ""}
                <tr data-agent={a.name} aria-selected={open} aria-busy={busy}>
                  <td><a class="name" href={href.agent(id, a.name)}>{a.name}</a>{#if a.stale} <span class="pill" data-s="warn" title="the template changed after this agent was made">stale</span>{/if}{#if a.denied} <a class="pill" data-s="error" data-testid="denied" href={href.agent(id, a.name, "network")} title="hosts the proxy refused this agent in the last hour">{a.denied} denied</a>{/if}</td>
                  <td>{#if a.template}<a class="dim" href={href.project(id, "templates")}>{a.template}</a>{#if !project?.templates.includes(a.template)} <span class="pill" data-s="warn" title="the project no longer publishes this template">retired</span>{/if}{:else}<span class="muted">–</span>{/if}</td>
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
                      {#if made && a.canOperate && !offline}
                        <button class="btn" data-size="sm" aria-label="{open ? 'Open terminal of' : 'Attach'} {a.name}" disabled={!a.running || busy} onclick={() => attach(a.name)}><Icon name="attach" size="sm" />{open ? "Open terminal" : "Attach"}</button>
                        {#if a.running}<button class="btn" data-size="sm" data-icon-only aria-label="Stop {a.name}" title="Stop" disabled={busy} onclick={() => act(a.name, "stop")}><Icon name="stop" size="sm" /></button>
                        {:else}<button class="btn" data-size="sm" data-icon-only aria-label="Start {a.name}" title="Start" disabled={busy} onclick={() => act(a.name, "start")}><Icon name="play" size="sm" /></button>{/if}
                        <button class="btn" data-variant="danger" data-size="sm" data-icon-only aria-label="Remove {a.name}" title="Remove" disabled={busy} onclick={() => act(a.name, "rm")}><Icon name="trash" size="sm" /></button>
                      {/if}
                    </span>
                  </td>
                </tr>
{/snippet}

{#if failure}
  <ErrorPage error={failure} />
{:else if project}
  <div class="page">
    <nav class="crumbs" aria-label="Breadcrumb"><a href={href.home()}>Projects</a> / <a href={href.project(id)}>{project.name}</a> <span class="muted">on <span data-testid="host">{project.host}</span></span></nav>
    <div class="phead">
      <h1 class="h-page">{tabs.find(([t]) => t === (tab === "spec" ? "advanced" : tab))?.[1] ?? project.name}</h1>
      {#if project.ownerless}<span class="pill" data-s="warn" title="nobody who can sign in owns it">no owner</span>{/if}
      <span class="grow"></span>
      {#if project.canSpawn && (tab === "agents" || tab === "templates")}<button class="btn" data-variant="primary" onclick={() => openSpawn()} disabled={!project.up}>Spawn agent</button>{/if}
    </div>

    {#if project.offlineSince}
      <div class="banner" data-tone="unreachable" role="status" data-testid="offline"><span class="tag">⌁ Offline since <span title={stamp(project.offlineSince)}>{ago(project.offlineSince)}</span></span>
        <div class="body">The project's link is not connected, so this is what the hub last heard. Nothing can be done on it from here until <code>egzo project up</code> runs on {project.host || "its machine"}.</div></div>
    {/if}

    {#if tab === "agents" && (project.feed.state === "unavailable" || project.feed.state === "stale") && agents.some((a) => a.running)}
      <div class="banner" data-tone="info" role="status" data-testid="feed-banner" title={project.feed.message}><span class="tag">? Agent status unavailable</span>
        <div class="body">The hub can't see what the agents are doing right now, so the running ones just show as "running".</div></div>
    {/if}

    {#if !project.up && !project.offlineSince}
      <div class="banner" data-tone="warn" role="alert"><span class="tag">! Sidecars down</span>
        <div class="body">The project's control or proxy sidecar is not running, so agents cannot be spawned. Run <code>egzo project up</code>.</div></div>
    {/if}


    {#if tab === "agents"}
      {#if agents.length}
        <div class="table-wrap">
          <table class="table" data-testid="agents">
            <thead><tr><th>Agent</th><th>Template</th><th>Activity</th><th>Status</th><th>Terminal</th><th>Spawned by</th><th></th></tr></thead>
            <tbody>{#each agents as a (a.name)}{@render row(a)}{/each}</tbody>
          </table>
        </div>
        {#each agents.filter((a) => a.previews?.length) as a (a.name)}
          <h3 class="h-section">Previews of {a.name}</h3>
          <Previews agent={a.name} previews={a.previews} heading={false} />
        {/each}
      {:else}
        <div class="empty"><h2>No agent yet</h2><p>An agent is made from one of the project's <a href={href.project(id, "templates")}>templates</a>{#if project.templates.length === 0}. The project has published no template: run <code>egzo project up</code> on the project{/if}.</p></div>
      {/if}
    {:else if tab === "templates"}
      {#if groups.length}
        {#if templatesError}<p class="muted" style="margin:0">What the templates give an agent cannot be read: {templatesError}</p>{/if}
        <div class="tgroups">
          {#each groups as g (g.name)}
            {@const d = g.detail}
            {@const up = g.agents.filter((a) => a.running).length}
            <section class="tgroup" data-testid="template" data-template={g.name} data-retired={!g.published ? "" : undefined}>
              <header class="tg-head">
                <span class="tg-ini" aria-hidden="true">{(g.name || "?").slice(0, 2)}</span>
                <div class="tg-title">
                  <h2>{g.name || "No template"}{#if !g.published && g.name} <span class="pill" data-s="warn" title="the project no longer publishes this template: its agents run on what it was">retired</span>{/if}</h2>
                  {#if d}
                    <div class="tg-sub">
                      <span title="harness">{d.harness}</span>
                      {#if d.model}<span title="model">{d.model}</span>{/if}
                      {#if d.image}<span title="image">{d.image}</span>{:else if d.built}<span title="the image is built by egzo project up">built image</span>{/if}
                      {#if d.permissions && d.permissions !== "bypass"}<span title="permissions">asks before acting</span>{/if}
                    </div>
                  {:else if g.published && !templates && !templatesError && project.role !== "overseer" && !offline}
                    <div class="tg-sub muted">Reading the template…</div>
                  {/if}
                </div>
                {#if project.canSpawn && g.published}<button class="btn" data-size="sm" aria-label="Spawn an agent from {g.name}" onclick={() => openSpawn(g.name)} disabled={!project.up}><Icon name="plus" size="sm" />Spawn agent</button>{/if}
              </header>
              {#if d}
                <dl class="tg-facts">
                  <div><dt>workspaces</dt><dd>{#each d.workspaces as w}<span class="chip" title={w.mount}>{w.name}{#if w.mode === "ro"} <small>read-only</small>{/if}</span>{:else}<span class="muted">none</span>{/each}</dd></div>
                  <div><dt>network</dt><dd><span class="chip" title="egress profile">{d.egress.profile}</span> <span class="muted">{d.egress.allow.length} host{d.egress.allow.length === 1 ? "" : "s"}{#if d.egress.services.length}, {d.egress.services.length} service{d.egress.services.length === 1 ? "" : "s"}{/if}{#if d.egress.ssh.length}, {d.egress.ssh.length} ssh{/if}</span></dd></div>
                  <div><dt>ports</dt><dd>{#each d.ports as p}<span class="chip">{p.name} <small>{p.port}/{p.protocol}</small></span>{:else}<span class="muted">none</span>{/each}</dd></div>
                </dl>
              {/if}
              <div class="tg-inst">
                <span class="h-section">Agents</span>
                {#if g.agents.length}
                  <a class="counts" href={href.project(id)}>{#if up}<span><span class="dot" data-s="running"></span>{up} running</span>{/if}{#if g.agents.length - up}<span><span class="dot" data-s="stopped"></span>{g.agents.length - up} stopped</span>{/if}</a>
                  <span class="muted">{g.agents.map((a) => a.name).join(", ")}</span>
                {:else}
                  <span class="muted">none made from it yet</span>
                {/if}
              </div>
            </section>
          {/each}
        </div>
      {:else}
        <div class="empty"><h2>No template</h2><p>The project has published no template: run <code>egzo project up</code> on the project.</p></div>
      {/if}
    {:else if tab === "activity"}
      {#if activity}<TaskTable project={id} tasks={activity} />{:else}<p class="muted">Loading…</p>{/if}
    {:else if tab === "network"}
      <NetworkView project={id} />
    {:else if tab === "audit"}
      {#if audit}<AuditTable entries={audit} />{:else}<p class="muted">Loading…</p>{/if}
    {:else if tab === "advanced" || tab === "spec"}
      <!-- what is rarely needed: the project's health, and the spec it runs under -->
      {#if project.role !== "overseer"}
        <div class="tabs" role="tablist" aria-label="Advanced sections">
          <a class="tab" role="tab" href={href.project(id, "advanced")} aria-selected={tab === "advanced"}>Health</a>
          <a class="tab" role="tab" href={href.project(id, "spec")} aria-selected={tab === "spec"}>Spec</a>
        </div>
      {/if}
      {#if tab === "spec"}
        {#if project.role === "overseer"}<p class="muted">The spec takes a role on the project.</p>
        {:else if spec === null}<p class="muted">Loading…</p>{:else}<p class="muted" style="margin:0">The project file as the project last applied it, with references to secrets, never their values.</p><SpecView text={spec} />{/if}
      {:else}
      <section class="panel" aria-labelledby="health" data-testid="health">
        <header><h2 class="h-section" id="health">Health</h2></header>
        <div class="table-wrap" style="border:0;border-radius:0">
          <table class="table">
            <thead><tr><th>Part</th><th>State</th><th>Details</th></tr></thead>
            <tbody>
              <tr>
                <td>Link to this hub</td>
                <td>{#if project.offlineSince}<Pill kind="unreachable" label="offline" />{:else}<Pill kind="running" label="connected" />{/if}</td>
                <td class="muted wrap">{#if project.offlineSince}Not connected since {ago(project.offlineSince)} ({stamp(project.offlineSince)}). What the hub shows of the project may be out of date.{:else}The project is connected to the hub.{/if}</td>
              </tr>
              <tr>
                <td>Agent status</td>
                <td>{#if project.feed.state === "live"}<Pill kind="running" label="up to date" />{:else if project.feed.state === "connecting"}<Pill kind="starting" label="connecting" />{:else}<Pill kind="warn" label="unavailable" />{/if}</td>
                <td class="muted wrap">{#if project.feed.state === "live"}The hub knows what each agent is doing.{:else}{project.feed.message || "The hub can't see what the agents are doing right now."}{/if}</td>
              </tr>
              {#each project.services as svc (svc.name)}
                <tr data-testid="service" data-service={svc.name}>
                  <td>{svc.name} <span class="muted">{({ control: "· agent status and messages", proxy: "· what the agents may reach" } as Record<string, string>)[svc.name] ?? "· service"}</span></td>
                  <td><Pill kind={svc.state === "running" ? "running" : "stopped"} label={svc.state} /></td>
                  <td class="muted wrap">{svc.health ? `health check: ${svc.health}` : "–"}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
        {#if !project.up && !project.offlineSince}<p class="muted" style="margin:var(--space-2) var(--space-3)">A part is down: run <code>egzo project up</code> on {project.host || "the project's machine"} to start it again.</p>{/if}
      </section>
      {#if project.canUnlink}
        <section class="panel" aria-labelledby="unlink">
          <header><h2 class="h-section" id="unlink">Unlink this project</h2></header>
          <div class="pad" style="display:grid;gap:var(--space-3)">
            <p class="muted" style="margin:0">Removes the project from this hub: nobody can reach it from here any more, and its open terminals close. The project itself keeps running on {project.host || "its machine"}; to stop it, run <code>egzo project down</code> there.</p>
            <div class="btn-group"><button class="btn" data-variant="danger" onclick={unlink}>Unlink {project.name}</button></div>
          </div>
        </section>
      {/if}
      {/if}
    {:else if tab === "members"}
      {#if members}<MembersTable {project} data={members} invites={invites} reload={loadMembers} />{:else}<p class="muted">Loading…</p>{/if}
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
