import { api } from "./api";
import { errorText, refreshProjects, toast } from "./app.svelte";
import { ask } from "./confirm.svelte";
import { followTask } from "./tasks";
import type { Task } from "./types";
import { closeTerm, termId, workspace } from "./workspace.svelte";

/** Start, stop or remove an agent, asking first when it cannot be undone. The hub answers at once with the task, which the agent
 * shows while it runs; returns it, or null when nothing was started. `onEnd` is called when it has ended. */
export async function operate(engine: string, project: string, agent: string, verb: "start" | "stop" | "rm", onEnd?: (task: Task) => void): Promise<Task | null> {
  if (verb === "rm") {
    const ok = await ask({
      title: `Remove ${agent}?`,
      lines: ["The agent's container and its workspace are deleted.", "A terminal open on it is closed.", "This cannot be undone."],
      confirm: "Remove",
    });
    if (!ok) return null;
  }
  let task: Task;
  try {
    task = await api.operate(engine, project, agent, verb);
  } catch (e) {
    toast(errorText(e), "error");
    return null;
  }
  followTask(engine, project, task, (ended) => {
    const id = termId(engine, project, agent);
    if (verb === "rm" && ended.state === "done" && workspace.terms.some((t) => t.id === id)) closeTerm(id);
    onEnd?.(ended);
  });
  void refreshProjects();
  return task;
}
