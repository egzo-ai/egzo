import { api, ApiError } from "./api";
import { refreshProjects, toast, updateToast } from "./app.svelte";
import { taskDoing, taskEnded } from "./format";
import { href, nav } from "./router.svelte";
import type { Task } from "./types";
import { workspace } from "./workspace.svelte";

/** The tasks this tab asked for, followed until they end, wherever the user goes meanwhile: a task takes as long as the project needs,
 * so the hub answers at once, and the task is shown on its agent. A toast says what it does only while its agent is not on the
 * screen, and how it ended when that was not seen there, or when it failed. */
interface Followed {
  project: string;
  task: Task;
  toast: number | null;
  onEnd?: (task: Task) => void;
}

const followed: Followed[] = [];
let timer = 0;
let busy = false;

export function followTask(project: string, task: Task, onEnd?: (task: Task) => void): void {
  followed.push({ project, task, toast: null, onEnd });
  if (!timer) timer = window.setInterval(() => void tick(), 1500);
}

/** Whether the agent is on the screen: its project's list of agents, or its own page, not hidden behind a terminal. */
export function onScreen(project: string, agent: string): boolean {
  const r = nav.route;
  if (workspace.active !== null) return false;
  if (r.name === "project") return r.project === project && r.tab === "agents";
  return r.name === "agent" && r.project === project && r.agent === agent;
}

function report(f: Followed): void {
  const t = f.task;
  const link = href.agent(f.project, t.agent);
  const seen = onScreen(f.project, t.agent);
  if (t.state === "running") {
    const text = `${taskDoing(t.verb)[0]!.toUpperCase()}${taskDoing(t.verb).slice(1)} ${t.agent}${t.step ? " · " + t.step : ""}`;
    if (f.toast !== null) f.toast = updateToast(f.toast, text, "busy", link);
    else if (!seen) f.toast = toast(text, "busy", link);
    return;
  }
  const text = taskEnded(t.verb, t.agent, t.state, t.error);
  const tone = t.state === "done" ? "ok" : "error";
  if (f.toast !== null) updateToast(f.toast, text, tone, link);
  else if (tone === "error" || !seen) toast(text, tone, link);
}

async function tick(): Promise<void> {
  if (busy) return;
  busy = true;
  try {
    for (const f of [...followed]) {
      try {
        f.task = await api.task(f.project, f.task.id);
      } catch (e) {
        if (!(e instanceof ApiError) || e.status !== 404) continue; // the next tick asks again
        followed.splice(followed.indexOf(f), 1);
        if (f.toast !== null) updateToast(f.toast, `${f.task.agent} can no longer be followed: look for it in ${f.project}`, "error");
        continue;
      }
      report(f);
      if (f.task.state === "running") continue;
      followed.splice(followed.indexOf(f), 1);
      f.onEnd?.(f.task);
      void refreshProjects();
    }
  } finally {
    busy = false;
    if (!followed.length && timer) {
      window.clearInterval(timer);
      timer = 0;
    }
  }
}
