<script lang="ts">
  import { api } from "../lib/api";
  import { app, errorText, multiEngine, toast } from "../lib/app.svelte";
  import { operate } from "../lib/agentops";
  import { activityKind, activityLabel, ago, initials, stamp } from "../lib/format";
  import { poll } from "../lib/live";
  import { href, navigate } from "../lib/router.svelte";
  import type { AgentAnswer, TimelineEvent } from "../lib/types";
  import { openTerm, termId, workspace } from "../lib/workspace.svelte";
  import EngineBadge from "../components/EngineBadge.svelte";
  import Icon from "../components/Icon.svelte";
  import Pill from "../components/Pill.svelte";
  import Timeline from "../components/Timeline.svelte";
  import ErrorPage from "./ErrorPage.svelte";

  let { engine, project, agent, tab }: { engine: string; project: string; agent: string; tab: "overview" | "logs" } = $props();

  let data = $state<AgentAnswer | null>(null);
  let failure = $state<unknown>(null);
  let events = $state<TimelineEvent[] | null>(null);
  let logs = $state<string | null>(null);

  const multi = $derived(multiEngine());
  const a = $derived(data?.agent);
  const isOpen = $derived(workspace.terms.some((t) => t.id === termId(engine, project, agent)));

  // The agent itself comes from what the hub already holds, so it is read often. The timeline is read from the control sidecar, which
  // runs a command in it, so it is read slowly and only when it is on the screen.
  $effect(() => poll(async () => {
    try {
      data = await api.agent(engine, project, agent);
      failure = null;
    } catch (e) {
      if (!data) failure = e;
      else throw e;
    }
  }));
  $effect(() => {
    if (tab !== "overview") return;
    return poll(async () => {
      events = await api.events(engine, project, agent, 40);
    }, 15000);
  });
  $effect(() => {
    if (tab === "logs") void api.logs(engine, project, agent).then((t) => (logs = t)).catch((e) => toast(errorText(e), "error"));
  });

  async function act(verb: "start" | "stop" | "rm") {
    if (!(await operate(engine, project, agent, verb))) return;
    if (verb === "rm") navigate(href.project(engine, project));
    else data = await api.agent(engine, project, agent);
  }

  function attach() {
    if (!openTerm(engine, project, agent)) toast("Too many terminals are open. Close one first.", "error");
  }
</script>

{#if failure}
  <ErrorPage error={failure} />
{:else if data && a}
  <div class="page">
    <nav class="crumbs" aria-label="Breadcrumb"><a href={href.home()}>Projects</a> / <a href={href.project(engine, project)}>{project}</a> /{#if multi} <EngineBadge name={engine} />{/if}</nav>
    <div class="phead">
      <h1 class="h-page">{a.name}</h1>
      <Pill kind={activityKind(a.activity, a.waiting)} label={activityLabel(a.activity, a.waiting)} testid="activity" />
      {#if a.waiting}<span class="pill" data-s="waiting" data-testid="waiting">waiting for an answer</span>{/if}
      {#if a.stale}<span class="pill" data-s="warn" title="the template changed after this agent was made">stale</span>{/if}
      <span class="grow"></span>
      <span class="btn-group">
        {#if a.canOperate && a.running}<button class="btn" data-variant="primary" data-key="a" aria-label={isOpen ? "Go to terminal" : "Attach"} aria-keyshortcuts="a" onclick={attach}><Icon name="attach" />{isOpen ? "Go to terminal" : "Attach"} <span class="kbd">a</span></button>{/if}
        {#if a.canOperate}
          {#if a.running}<button class="btn" onclick={() => act("stop")}><Icon name="stop" />Stop</button>
          {:else}<button class="btn" onclick={() => act("start")}><Icon name="play" />Start</button>{/if}
          <button class="btn" data-variant="danger" onclick={() => act("rm")}><Icon name="trash" />Remove</button>
        {/if}
      </span>
    </div>

    {#if a.status}
      <div class="statusline"><span data-testid="status" class:q={a.waiting}>{a.status}</span></div>
    {/if}
    {#if a.waiting}
      <div class="banner" data-tone="info" role="status"><span class="tag">? Waiting for an answer</span><div class="body">The agent asked a question and nobody has answered it. Answer it in the terminal, or from a messaging client.</div></div>
    {/if}

    <section class="termcard" aria-label="Terminal">
      <div class="grow">
        <b>Terminal</b><br />
        {#if a.canOperate && a.running}
          <span class="muted">One person holds the terminal at a time; opening it while somebody else does offers to take it over. Detach with <span class="kbd">Ctrl</span> <span class="kbd">]</span> then <span class="kbd">d</span>.</span>
        {:else if a.canOperate}
          <span class="muted">The agent is {a.state}. Start it to open its terminal.</span>
        {:else}
          <span class="muted">Only the people who may operate this agent can open its terminal.</span>
        {/if}
      </div>
      {#if a.holder}
        <span class="presence" data-live data-testid="presence"><span class="avatar" data-me={a.holder === app.user?.username ? "" : undefined}>{initials(a.holder)}</span>{a.holder === app.user?.username ? "you" : a.holder}</span>
      {/if}
    </section>

    <div class="tabs" role="tablist">
      <a class="tab" role="tab" href={href.agent(engine, project, agent)} aria-selected={tab === "overview"}>Timeline</a>
      <a class="tab" role="tab" href={href.agent(engine, project, agent, "logs")} aria-selected={tab === "logs"}>Logs</a>
    </div>

    {#if tab === "overview"}
      <div class="twocol">
        <Timeline {events} />
        <section class="panel">
          <header><h2 class="h-section">Details</h2></header>
          <div class="pad">
            <dl class="kv">
              <dt>template</dt><dd>{a.template}</dd>
              <dt>spawned by</dt><dd>{a.creator}</dd>
              <dt>created</dt><dd title={stamp(a.createdAt)}>{ago(a.createdAt)}</dd>
              <dt>state</dt><dd>{a.state}</dd>
            </dl>
          </div>
        </section>
      </div>
    {:else if logs === null}
      <p class="muted">Loading…</p>
    {:else}
      <pre class="log" data-testid="text">{logs}</pre>
    {/if}
  </div>
{:else}
  <div class="page"><p class="muted">Loading…</p></div>
{/if}
