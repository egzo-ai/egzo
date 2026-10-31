<script lang="ts">
  import { ago, duration, stamp, taskDoing, taskFailedLabel } from "../lib/format";
  import { href } from "../lib/router.svelte";
  import type { Task } from "../lib/types";
  import Pill from "./Pill.svelte";

  let { project, tasks, agentColumn = true }: { project: string; tasks: Task[]; agentColumn?: boolean } = $props();

  const verbs: Record<string, string> = { spawn: "spawn", start: "start", stop: "stop", rm: "remove" };
</script>

<div class="table-wrap">
  <table class="table" data-testid="activity">
    <thead><tr><th>When</th><th>Who</th><th>What</th>{#if agentColumn}<th>Agent</th>{/if}<th>Result</th><th>Took</th><th>Detail</th></tr></thead>
    <tbody>
      {#each tasks as t (t.id)}
        <tr data-task={t.id}>
          <td title={stamp(t.startedAt)}>{ago(t.startedAt)}</td>
          <td>{t.creator}</td>
          <td>{verbs[t.verb] ?? t.verb}{#if t.template} <span class="muted">{t.template}</span>{/if}</td>
          {#if agentColumn}<td><a href={href.agent(project, t.agent)}>{t.agent}</a></td>{/if}
          <td>
            {#if t.state === "running"}<Pill kind="starting" label={taskDoing(t.verb)} />
            {:else if t.state === "done"}<Pill kind="done" label="done" />
            {:else}<Pill kind="error" label={taskFailedLabel(t.verb, t.state)} />{/if}
          </td>
          <td>{duration(t.startedAt, t.endedAt)}</td>
          <td class="wrap">{#if t.error}<span style:color="var(--status-error)">{t.error}</span>{:else if t.state === "running"}<span class="muted">{t.step}</span>{/if}</td>
        </tr>
      {:else}
        <tr><td colspan={agentColumn ? 7 : 6} class="muted">Nothing done from the hub yet.</td></tr>
      {/each}
    </tbody>
  </table>
</div>
