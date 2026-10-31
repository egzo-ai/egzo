// The keyboard of the whole app: the leader chord (inside a terminal and out), and single-key shortcuts on pages.
import { app, seesLinks, toast } from "./app.svelte";
import { Leader, type LeaderAction } from "./leader";
import { href, nav, navigate } from "./router.svelte";
import { sendTo } from "./registry";
import { closeTerm, cycleLayout, focusOtherPane, goTerm, showPage, stepTerm, termById, workspace } from "./workspace.svelte";

export const keys = $state({ armed: false });
const leader = new Leader();

function run(action: LeaderAction): void {
  switch (action.type) {
    case "go": goTerm(action.n); break;
    case "step": stepTerm(action.delta); break;
    case "back":
      showPage();
      if (nav.route.name === "login") navigate(href.home());
      break;
    case "sidebar": {
      workspace.rail = false;
      workspace.navOpen = true;
      queueMicrotask(() => document.querySelector<HTMLElement>('.side [tabindex="0"], .side a')?.focus());
      break;
    }
    case "rail": workspace.rail = !workspace.rail; break;
    case "layout": cycleLayout(); break;
    case "pane": focusOtherPane(); break;
    case "fullscreen": if (workspace.active) workspace.fullscreen = !workspace.fullscreen; break;
    case "detach": {
      const t = termById(workspace.active);
      if (t) {
        closeTerm(t.id);
        toast(`Detached from ${t.agent}. The agent keeps running.`);
      }
      break;
    }
    case "palette": app.palette = true; break;
    case "literal": sendTo(workspace.active, "\x1d"); break;
    case "help": app.keyref = true; break;
    case "cancel": break;
  }
}

function typing(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null;
  if (!el?.tagName) return false;
  return el.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(el.tagName);
}

let goPending = 0;

function pageKey(e: KeyboardEvent): boolean {
  if (e.key === "k" && e.ctrlKey && !e.metaKey && !e.altKey) { app.palette = true; return true; }
  if (e.ctrlKey || e.metaKey || e.altKey || !app.singleKeys || typing(e.target) || app.palette || app.keyref) return false;
  if (workspace.active !== null) return false; // a terminal is in view: its keys are the agent's
  const row = document.activeElement?.closest?.("[data-row]") as HTMLElement | null;
  if (goPending && Date.now() - goPending < 1500) {
    goPending = 0;
    if (e.key === "p") { navigate(href.home()); return true; }
    if (e.key === "l" && seesLinks()) { navigate(href.links()); return true; }
    if (e.key === "i" && app.user?.isOperator) { navigate(href.invites()); return true; }
    if (e.key === "u" && app.user?.isOperator) { navigate(href.users()); return true; }
    return false;
  }
  switch (e.key) {
    case ":": app.palette = true; return true;
    case "?": app.keyref = true; return true;
    case "/": {
      const f = document.querySelector<HTMLElement>("[data-filter-input]");
      if (!f) return false;
      f.focus();
      return true;
    }
    case "g": goPending = Date.now(); return true;
    case "j": case "k": {
      const rows = [...document.querySelectorAll<HTMLElement>("[data-row]")];
      if (!rows.length) return false;
      const i = row ? rows.indexOf(row) : -1;
      const next = rows[Math.max(0, Math.min(rows.length - 1, i + (e.key === "j" ? 1 : -1)))]!;
      next.focus();
      return true;
    }
    case "Enter": {
      if (!row) return false;
      row.querySelector<HTMLElement>("a.name")?.click();
      return true;
    }
    case "a": case "s": {
      const scope = e.key === "a" && row ? row : document;
      const button = scope.querySelector<HTMLElement>(`[data-key="${e.key}"]`);
      if (!button) return false;
      button.click();
      return true;
    }
  }
  return false;
}

/** Listen on the whole window, before the terminal does. Returns the function that stops it. */
export function startKeyboard(): () => void {
  const onKey = (e: KeyboardEvent) => {
    const r = leader.handle(e);
    keys.armed = r.armed;
    if (r.consumed) {
      e.preventDefault();
      e.stopPropagation();
      if (r.action) run(r.action);
      return;
    }
    if (!leader.armed && pageKey(e)) {
      e.preventDefault();
      e.stopPropagation();
    }
  };
  const onBlur = () => {
    leader.cancel();
    keys.armed = false;
  };
  window.addEventListener("keydown", onKey, true);
  window.addEventListener("blur", onBlur);
  return () => {
    window.removeEventListener("keydown", onKey, true);
    window.removeEventListener("blur", onBlur);
  };
}
