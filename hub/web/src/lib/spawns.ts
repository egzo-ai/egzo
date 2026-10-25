import { api } from "./api";
import { toast } from "./app.svelte";
import type { Spawn } from "./types";

/** The spawns this tab started, followed until they end, wherever the user goes meanwhile: a spawn takes as long as the engine
 * needs, so the hub answers at once and the project lists the spawn. Ending says so in a toast. */
interface Followed {
  engine: string;
  project: string;
  id: string;
  label: string;
}

const followed: Followed[] = [];
let timer = 0;
let busy = false;

export function followSpawn(engine: string, project: string, spawn: Spawn): void {
  followed.push({ engine, project, id: spawn.id, label: spawn.name || spawn.template });
  if (!timer) timer = window.setInterval(() => void tick(), 2000);
}

async function tick(): Promise<void> {
  if (busy) return;
  busy = true;
  try {
    const projects = new Map<string, Followed[]>();
    for (const f of followed) {
      const key = `${f.engine}\u0000${f.project}`;
      projects.set(key, [...(projects.get(key) ?? []), f]);
    }
    for (const group of projects.values()) {
      let spawns: Spawn[];
      try {
        spawns = (await api.project(group[0].engine, group[0].project)).spawns ?? [];
      } catch {
        continue; // the next tick asks again
      }
      for (const f of group) {
        const s = spawns.find((x) => x.id === f.id);
        if (s?.state === "spawning") continue;
        followed.splice(followed.indexOf(f), 1);
        if (s?.state === "ready") toast(`Spawned ${s.agent}`);
        else if (s?.state === "failed") toast(`Could not spawn ${f.label}: ${s.error}`, "error");
        else toast(`The hub no longer knows the spawn of ${f.label}: look for it in ${f.project}`, "error"); // a hub that restarted
      }
    }
  } finally {
    busy = false;
    if (!followed.length && timer) {
      window.clearInterval(timer);
      timer = 0;
    }
  }
}
