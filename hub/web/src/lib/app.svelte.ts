// What the whole app shares: who is signed in, the projects the sidebar and the home page list, the toasts, and the preferences.
import { ApiError, api, setUnauthorizedHandler } from "./api";
import { navigate, nav } from "./router.svelte";
import type { Identity, ProjectBrief, ProjectList, User } from "./types";

export const app = $state({
  ready: false,
  user: null as User | null,
  /** How the signed-in user signs in: their accounts at login providers, a password, and what single sign-on is called. */
  account: { identities: [] as Identity[], hasPassword: false, sso: "", issuer: "" },
  projects: [] as ProjectBrief[],
  projectsLoaded: false,
  offline: false,
  theme: "system" as "system" | "dark" | "light",
  singleKeys: true,
  renderer: "auto" as "auto" | "dom",
  keyref: false,
  palette: false,
});

/** A toast: "busy" while something it follows runs (it stays), "ok" goes away by itself, "error" stays until it is closed. */
export interface Toast {
  id: number;
  text: string;
  tone: "ok" | "error" | "busy";
  /** Where the toast leads: the agent a task was for. */
  link?: string;
}

export const toasts = $state<Toast[]>([]);
let toastId = 0;

export function toast(text: string, tone: Toast["tone"] = "ok", link?: string): number {
  const id = ++toastId;
  toasts.push({ id, text, tone, link });
  if (tone === "ok") setTimeout(() => dismissToast(id), 5000);
  return id;
}

/** Changes what a toast says, as a task it follows goes on; one that is gone is shown again. */
export function updateToast(id: number, text: string, tone: Toast["tone"], link?: string): number {
  const t = toasts.find((x) => x.id === id);
  if (!t) return toast(text, tone, link);
  const settles = t.tone !== "ok" && tone === "ok";
  Object.assign(t, { text, tone, link });
  if (settles) setTimeout(() => dismissToast(id), 5000);
  return id;
}

export function dismissToast(id: number): void {
  const i = toasts.findIndex((t) => t.id === id);
  if (i >= 0) toasts.splice(i, 1);
}

export function errorText(e: unknown): string {
  return e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e);
}

/** The name of a project, for what holds only its id. */
export const projectName = (id: string) => app.projects.find((p) => p.id === id)?.name ?? id;

function applyProjects(list: ProjectList): void {
  app.projects = list.projects;
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
  app.account = { identities: s.identities, hasPassword: s.hasPassword, sso: s.sso, issuer: s.issuer };
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
