export function clock(iso: string): string {
  const d = new Date(iso);
  return isNaN(+d) ? "" : d.toLocaleTimeString([], { hour12: false });
}

export function stamp(iso: string): string {
  const d = new Date(iso);
  if (isNaN(+d)) return "";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

export function ago(iso: string, now = Date.now()): string {
  const t = +new Date(iso);
  if (isNaN(t) || t <= 0) return "";
  const s = Math.max(0, (now - t) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 48 * 3600) return `${Math.floor(s / 3600)} h ago`;
  return `${Math.floor(s / 86400)} days ago`;
}

export function initials(name: string): string {
  const parts = name.replace(/@.*/, "").split(/[\s._-]+/).filter(Boolean);
  if (parts.length === 0) return "?";
  if (parts.length === 1) return parts[0]!.slice(0, 2).toUpperCase();
  return (parts[0]![0]! + parts[parts.length - 1]![0]!).toUpperCase();
}

/** The label of an agent's activity. "unknown" is what a running agent is when the hub cannot read its status (never "starting"). */
export function activityLabel(activity: string, waiting: boolean): string {
  if (waiting) return "waiting for you";
  return activity === "unknown" || !activity ? "running" : activity;
}

/** The status-pill kind of an agent (the design's data-s). */
export function activityKind(activity: string, waiting: boolean): string {
  if (waiting) return "waiting";
  switch (activity) {
    case "working": case "idle": case "starting": case "stopped": case "error": case "done": case "running":
      return activity;
    case "unknown":
      return "running"; // its container runs; what it is doing is not known
  }
  return "neutral";
}

const taskWords: Record<string, [string, string, string]> = {
  // what it does, what it did, what it could not do
  spawn: ["spawning", "Spawned", "spawn"],
  start: ["starting", "Started", "start"],
  stop: ["stopping", "Stopped", "stop"],
  rm: ["removing", "Removed", "remove"],
};

/** What a task does, for its pill: "stopping". */
export function taskDoing(verb: string): string {
  return taskWords[verb]?.[0] ?? verb;
}

/** How a task ended, for whoever asked: "Stopped agent-3", "Could not stop agent-3: why". */
export function taskEnded(verb: string, agent: string, state: string, error = ""): string {
  const words = taskWords[verb] ?? [verb, verb, verb];
  if (state === "done") return `${words[1]} ${agent}`;
  if (state === "interrupted") return `The hub stopped while ${words[0]} ${agent}: look at the agent`;
  return `Could not ${words[2]} ${agent}${error ? ": " + error : ""}`;
}

/** The pill of a task that ended badly: "stop failed". */
export function taskFailedLabel(verb: string, state: string): string {
  return state === "interrupted" ? "interrupted" : `${taskWords[verb]?.[2] ?? verb} failed`;
}

/** How long something took: "4 s", "2 min 5 s". */
export function duration(fromIso: string, toIso?: string, now = Date.now()): string {
  const from = +new Date(fromIso);
  const to = toIso ? +new Date(toIso) : now;
  if (isNaN(from) || isNaN(to)) return "";
  const s = Math.max(0, Math.round((to - from) / 1000));
  if (s < 60) return `${s} s`;
  if (s < 3600) return `${Math.floor(s / 60)} min${s % 60 ? ` ${s % 60} s` : ""}`;
  return `${Math.floor(s / 3600)} h ${Math.floor((s % 3600) / 60)} min`;
}
