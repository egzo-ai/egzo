import { api } from "./api";
import { errorText, refreshProjects, toast } from "./app.svelte";
import { ask } from "./confirm.svelte";
import { closeTerm, termId, workspace } from "./workspace.svelte";

/** Start, stop or remove an agent, asking first when it cannot be undone. Returns true when it was done. */
export async function operate(engine: string, project: string, agent: string, verb: "start" | "stop" | "rm"): Promise<boolean> {
  if (verb === "rm") {
    const ok = await ask({
      title: `Remove ${agent}?`,
      lines: ["The agent's container and its workspace are deleted.", "A terminal open on it is closed.", "This cannot be undone."],
      confirm: "Remove",
    });
    if (!ok) return false;
  }
  try {
    await api.operate(engine, project, agent, verb);
  } catch (e) {
    toast(errorText(e), "error");
    return false;
  }
  toast({ start: "Started ", stop: "Stopped ", rm: "Removed " }[verb] + agent);
  if (verb === "rm" && workspace.terms.some((t) => t.id === termId(engine, project, agent))) closeTerm(termId(engine, project, agent));
  void refreshProjects();
  return true;
}
