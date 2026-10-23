// A small router over the History API. The hub serves index.html for every address, so these paths survive a reload.

export type ProjectTab = "agents" | "services" | "timeline" | "network" | "spec" | "audit" | "members";
export const projectTabs: ProjectTab[] = ["agents", "services", "timeline", "network", "spec", "audit", "members"];

export type AgentTab = "overview" | "network" | "logs";

export type SettingsTab = "engines" | "users" | "account";
export const settingsTabs: SettingsTab[] = ["engines", "users", "account"];

export type Route =
  | { name: "home" }
  | { name: "login" }
  | { name: "settings"; tab: SettingsTab }
  | { name: "project"; engine: string; project: string; tab: ProjectTab }
  | { name: "agent"; engine: string; project: string; agent: string; tab: AgentTab }
  | { name: "notfound" };

const dec = (s: string) => {
  try {
    return decodeURIComponent(s);
  } catch {
    return s;
  }
};

export function parse(pathname: string): Route {
  const parts = pathname.split("/").filter(Boolean).map(dec);
  if (parts.length === 0) return { name: "home" };
  if (parts[0] === "login" && parts.length === 1) return { name: "login" };
  // /engines is where the engines were before Settings; it, and /settings alone, land on the first tab
  if (parts[0] === "engines" && parts.length === 1) return { name: "settings", tab: "engines" };
  if (parts[0] === "settings" && parts.length === 1) return { name: "settings", tab: "engines" };
  if (parts[0] === "settings" && parts.length === 2 && (settingsTabs as string[]).includes(parts[1]!)) return { name: "settings", tab: parts[1] as SettingsTab };
  if (parts[0] === "e" && parts[2] === "p" && parts[1] && parts[3]) {
    const [, engine, , project, ...rest] = parts as [string, string, string, string, ...string[]];
    if (rest.length === 0) return { name: "project", engine, project, tab: "agents" };
    if (rest.length === 1 && (projectTabs as string[]).includes(rest[0]!) && rest[0] !== "agents") {
      return { name: "project", engine, project, tab: rest[0] as ProjectTab };
    }
    if (rest[0] === "a" && rest[1]) {
      if (rest.length === 2) return { name: "agent", engine, project, agent: rest[1], tab: "overview" };
      if (rest.length === 3 && (rest[2] === "logs" || rest[2] === "network")) return { name: "agent", engine, project, agent: rest[1], tab: rest[2] };
    }
  }
  return { name: "notfound" };
}

const enc = encodeURIComponent;
export const href = {
  home: () => "/",
  settings: (tab: SettingsTab = "engines") => `/settings/${tab}`,
  engines: () => "/settings/engines",
  users: () => "/settings/users",
  account: () => "/settings/account",
  project: (engine: string, project: string, tab: ProjectTab = "agents") =>
    `/e/${enc(engine)}/p/${enc(project)}${tab === "agents" ? "" : "/" + tab}`,
  agent: (engine: string, project: string, agent: string, tab: AgentTab = "overview") =>
    `/e/${enc(engine)}/p/${enc(project)}/a/${enc(agent)}${tab === "overview" ? "" : "/" + tab}`,
};

export const nav = $state({ path: "/", route: { name: "home" } as Route, previous: "/" });

/** The address a route is known by, for the ones that have older or shorter ones. */
export function canonical(route: Route): string | null {
  return route.name === "settings" ? href.settings(route.tab) : null;
}

function sync() {
  const route = parse(location.pathname);
  const canon = canonical(route);
  if (canon && location.pathname !== canon) history.replaceState(null, "", canon + location.search);
  nav.path = location.pathname + location.search;
  nav.route = route;
}

/** Go to an address of the app. */
export function navigate(to: string, replace = false): void {
  if (to === nav.path) return;
  nav.previous = nav.path;
  if (replace) history.replaceState(null, "", to);
  else history.pushState(null, "", to);
  sync();
  window.dispatchEvent(new CustomEvent("egzo:navigate"));
}

/** Start following the address bar, and turn clicks on in-app links into navigation. */
export function startRouter(): void {
  sync();
  window.addEventListener("popstate", () => {
    nav.previous = nav.path;
    sync();
    window.dispatchEvent(new CustomEvent("egzo:navigate"));
  });
  document.addEventListener("click", (event) => {
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    const link = (event.target as Element | null)?.closest?.("a[href]") as HTMLAnchorElement | null;
    if (!link || link.target || link.hasAttribute("download") || link.dataset.external !== undefined) return;
    const url = new URL(link.href, location.href);
    if (url.origin !== location.origin) return;
    event.preventDefault();
    navigate(url.pathname + url.search);
  });
}
