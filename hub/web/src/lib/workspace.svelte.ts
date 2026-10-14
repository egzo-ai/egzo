// The workspace: the open terminals and how they are shown. It lives above the router, so a terminal survives every navigation;
// only closing one destroys it. The list of open terminals is kept in sessionStorage and reattached on a reload.
import type { TermState } from "./termsession";

export type Layout = 1 | 2 | 4;

export interface Term {
  id: string; // "engine:project/agent"
  engine: string;
  project: string;
  agent: string;
  state: TermState;
  holder: string;
  message: string;
  unread: boolean;
  /** Bumped to make the pane connect again. */
  epoch: number;
  /** The next connection takes the terminal over. */
  takeover: boolean;
}

export const MAX_TERMS = 12;
const STORE_KEY = "egzo.terminals";

export const termId = (engine: string, project: string, agent: string) => `${engine}:${project}/${agent}`;

/** Which terminal sits in each pane: keep the ones already shown, then fill the rest with open terminals that are not. */
export function fillPanes(ids: string[], panes: string[], layout: Layout): string[] {
  const out: string[] = panes.filter((id) => ids.includes(id)).slice(0, layout);
  for (const id of ids) {
    if (out.length >= layout) break;
    if (!out.includes(id)) out.push(id);
  }
  return out;
}

export const workspace = $state({
  terms: [] as Term[],
  /** The terminal in view, or null when a page is shown. */
  active: null as string | null,
  layout: 1 as Layout,
  panes: [] as string[],
  focusPane: 0,
  rail: false,
  fullscreen: false,
  navOpen: false,
});

function persist(): void {
  try {
    sessionStorage.setItem(STORE_KEY, JSON.stringify(workspace.terms.map((t) => [t.engine, t.project, t.agent])));
  } catch {
    /* storage may be unavailable */
  }
}

export function restore(): void {
  try {
    const saved = JSON.parse(sessionStorage.getItem(STORE_KEY) ?? "[]") as [string, string, string][];
    for (const [engine, project, agent] of saved.slice(0, MAX_TERMS)) add(engine, project, agent);
  } catch {
    /* nothing to restore */
  }
}

function add(engine: string, project: string, agent: string): Term {
  const id = termId(engine, project, agent);
  let term = workspace.terms.find((t) => t.id === id);
  if (!term) {
    term = { id, engine, project, agent, state: "connecting", holder: "", message: "", unread: false, epoch: 0, takeover: false };
    workspace.terms.push(term);
    term = workspace.terms[workspace.terms.length - 1]!; // the reactive proxy
    persist();
  }
  return term;
}

export const termById = (id: string | null) => workspace.terms.find((t) => t.id === id);

/** Open a terminal (or bring the open one forward) and show it. Returns false when too many are open. */
export function openTerm(engine: string, project: string, agent: string): boolean {
  const id = termId(engine, project, agent);
  if (!workspace.terms.some((t) => t.id === id) && workspace.terms.length >= MAX_TERMS) return false;
  add(engine, project, agent);
  showTerm(id);
  return true;
}

export function showTerm(id: string): void {
  if (!workspace.terms.some((t) => t.id === id)) return;
  workspace.active = id;
  workspace.navOpen = false;
  const at = workspace.panes.indexOf(id);
  if (at >= 0) {
    workspace.focusPane = at;
  } else {
    const panes = fillPanes(workspace.terms.map((t) => t.id), workspace.panes, workspace.layout);
    if (panes.length < workspace.layout) {
      panes.push(id);
    } else {
      panes[Math.min(workspace.focusPane, panes.length - 1)] = id;
    }
    workspace.panes = panes;
    workspace.focusPane = panes.indexOf(id);
  }
  const term = termById(id);
  if (term) term.unread = false;
}

/** Show the page again; the terminals stay open and mounted. */
export function showPage(): void {
  workspace.active = null;
  workspace.fullscreen = false;
}

export function closeTerm(id: string): void {
  const index = workspace.terms.findIndex((t) => t.id === id);
  if (index < 0) return;
  workspace.terms.splice(index, 1);
  persist();
  const ids = workspace.terms.map((t) => t.id);
  workspace.panes = fillPanes(ids, workspace.panes.filter((p) => p !== id), workspace.layout);
  if (workspace.active === id) {
    const next = workspace.panes[Math.min(workspace.focusPane, workspace.panes.length - 1)] ?? ids[0] ?? null;
    workspace.active = next ?? null;
  }
  if (workspace.active === null) workspace.fullscreen = false;
  workspace.focusPane = Math.max(0, workspace.panes.indexOf(workspace.active ?? ""));
}

/** The terminal after (or before) the one in view, going through the page as one more stop. */
export function stepTerm(delta: 1 | -1): void {
  const stops: (string | null)[] = [null, ...workspace.terms.map((t) => t.id)];
  const i = stops.indexOf(workspace.active);
  const next = stops[(i + delta + stops.length) % stops.length] ?? null;
  if (next === null) showPage();
  else showTerm(next);
}

export function goTerm(n: number): void {
  const term = workspace.terms[n - 1];
  if (term) showTerm(term.id);
}

export function setLayout(layout: Layout): void {
  workspace.layout = layout;
  const ids = workspace.terms.map((t) => t.id);
  if (workspace.active === null && ids.length) workspace.active = ids[0]!;
  workspace.panes = fillPanes(ids, workspace.panes, layout);
  workspace.focusPane = Math.max(0, workspace.panes.indexOf(workspace.active ?? ""));
}

export function cycleLayout(): void {
  setLayout(workspace.layout === 1 ? 2 : workspace.layout === 2 ? 4 : 1);
}

export function focusOtherPane(): void {
  if (workspace.panes.length < 2) return;
  workspace.focusPane = (workspace.focusPane + 1) % workspace.panes.length;
  workspace.active = workspace.panes[workspace.focusPane] ?? workspace.active;
  const term = termById(workspace.active);
  if (term) term.unread = false;
}

export function focusPane(index: number): void {
  const id = workspace.panes[index];
  if (!id) return;
  workspace.focusPane = index;
  workspace.active = id;
  const term = termById(id);
  if (term) term.unread = false;
}

/** What a pane tells the workspace. */
export function setTermState(id: string, state: TermState, info: { holder?: string; message?: string } = {}): void {
  const term = termById(id);
  if (!term) return;
  term.state = state;
  term.holder = info.holder ?? "";
  term.message = info.message ?? "";
}

export function noteOutput(id: string): void {
  const term = termById(id);
  if (!term) return;
  const shown = workspace.active !== null && workspace.panes.includes(id);
  if (!shown) term.unread = true;
}

/** Connect a terminal again; with takeover, ending the other person's. */
export function reconnect(id: string, takeover = false): void {
  const term = termById(id);
  if (!term) return;
  term.takeover = takeover;
  term.epoch++;
}

export function reset(): void {
  workspace.terms.splice(0);
  workspace.panes = [];
  workspace.active = null;
  workspace.layout = 1;
  workspace.focusPane = 0;
  workspace.fullscreen = false;
}
