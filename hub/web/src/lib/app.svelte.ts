// What the whole app shares: who is signed in, the projects the sidebar and the home page list, the toasts, and the preferences.
import { ApiError, api, setUnauthorizedHandler } from "./api";
import { navigate, nav } from "./router.svelte";
import type { EngineState, ProjectBrief, ProjectList, User } from "./types";

export const app = $state({
  ready: false,
  user: null as User | null,
  engines: [] as EngineState[],
  projects: [] as ProjectBrief[],
  unreachable: [] as EngineState[],
  projectsLoaded: false,
  offline: false,
  theme: "system" as "system" | "dark" | "light",
  singleKeys: true,
  renderer: "auto" as "auto" | "dom",
  keyref: false,
  palette: false,
});

export const toasts = $state<{ id: number; text: string; tone: "ok" | "error" }[]>([]);
let toastId = 0;

export function toast(text: string, tone: "ok" | "error" = "ok"): void {
  const id = ++toastId;
  toasts.push({ id, text, tone });
  if (tone === "ok") setTimeout(() => dismissToast(id), 5000);
}

export function dismissToast(id: number): void {
  const i = toasts.findIndex((t) => t.id === id);
  if (i >= 0) toasts.splice(i, 1);
}

export function errorText(e: unknown): string {
  return e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e);
}

/** True when more than one engine is configured: only then are engine badges and the filter shown. */
export const multiEngine = () => app.engines.length > 1;

function applyProjects(list: ProjectList): void {
  app.projects = list.projects;
  app.unreachable = list.unreachable;
  app.projectsLoaded = true;
}

let polling = 0;

export async function refreshProjects(): Promise<void> {
  if (!app.user) return;
  try {
    applyProjects(await api.projects());
    app.offline = false;
  } catch (e) {
    if (e instanceof ApiError && e.status === 0) app.offline = true;
  }
}

export async function refreshSession(): Promise<void> {
  const s = await api.session();
  app.user = s.user;
  app.engines = s.engines;
}

function applyTheme(): void {
  const root = document.documentElement;
  if (app.theme === "system") root.removeAttribute("data-theme");
  else root.dataset.theme = app.theme;
}

export function setTheme(theme: "system" | "dark" | "light"): void {
  app.theme = theme;
  applyTheme();
  try {
    localStorage.setItem("egzo.theme", theme);
  } catch {
    /* ignore */
  }
}

export function toggleTheme(): void {
  const dark = app.theme === "system" ? matchMedia("(prefers-color-scheme: dark)").matches : app.theme === "dark";
  setTheme(dark ? "light" : "dark");
}

export function setSingleKeys(on: boolean): void {
  app.singleKeys = on;
  try {
    localStorage.setItem("egzo.singlekeys", on ? "1" : "0");
  } catch {
    /* ignore */
  }
}

/** The terminal renderer: "auto" is WebGL where the browser has it, "dom" never uses it. */
export function setRenderer(r: "auto" | "dom"): void {
  app.renderer = r;
  try {
    localStorage.setItem("egzo.renderer", r);
  } catch {
    /* ignore */
  }
}

/** Where the login sends the browser back to. */
export function toLogin(): void {
  if (nav.route.name === "login") return;
  navigate("/login?next=" + encodeURIComponent(nav.path), true);
}

/** Load the session, start following the projects, and send an anonymous visitor to the sign-in screen. */
export async function boot(): Promise<void> {
  try {
    const t = localStorage.getItem("egzo.theme");
    if (t === "dark" || t === "light") app.theme = t;
    app.singleKeys = localStorage.getItem("egzo.singlekeys") !== "0";
    if (localStorage.getItem("egzo.renderer") === "dom") app.renderer = "dom";
  } catch {
    /* ignore */
  }
  applyTheme();
  setUnauthorizedHandler(() => {
    app.user = null;
    toLogin();
  });
  try {
    await refreshSession();
    await refreshProjects();
  } catch (e) {
    if (!(e instanceof ApiError && e.status === 401)) app.offline = true;
  }
  app.ready = true;
  if (!app.user) toLogin();
  if (!polling) {
    polling = window.setInterval(() => {
      if (document.hidden || !app.user) return;
      void refreshProjects();
      void refreshSession().catch(() => {});
    }, 3000);
  }
}

export async function signedIn(): Promise<void> {
  await refreshSession();
  await refreshProjects();
}

export async function signOut(): Promise<void> {
  try {
    await api.logout();
  } finally {
    app.user = null;
    app.projects = [];
    app.projectsLoaded = false;
    navigate("/login", true);
  }
}
