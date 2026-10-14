// A small router over the History API. The hub serves index.html for every address, so these paths survive a reload.

export type ProjectTab = "agents" | "services" | "timeline" | "spec" | "audit" | "members";
export const projectTabs: ProjectTab[] = ["agents", "services", "timeline", "spec", "audit", "members"];

export type Route =
  | { name: "home" }
  | { name: "login" }
  | { name: "engines" }
  | { name: "project"; engine: string; project: string; tab: ProjectTab }
  | { name: "agent"; engine: string; project: string; agent: string; tab: "overview" | "logs" }
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
  if (parts[0] === "engines" && parts.length === 1) return { name: "engines" };
  if (parts[0] === "e" && parts[2] === "p" && parts[1] && parts[3]) {
    const [, engine, , project, ...rest] = parts as [string, string, string, string, ...string[]];
    if (rest.length === 0) return { name: "project", engine, project, tab: "agents" };
    if (rest.length === 1 && (projectTabs as string[]).includes(rest[0]!) && rest[0] !== "agents") {
      return { name: "project", engine, project, tab: rest[0] as ProjectTab };
    }
    if (rest[0] === "a" && rest[1]) {
      if (rest.length === 2) return { name: "agent", engine, project, agent: rest[1], tab: "overview" };
      if (rest.length === 3 && rest[2] === "logs") return { name: "agent", engine, project, agent: rest[1], tab: "logs" };
    }
  }
  return { name: "notfound" };
}

const enc = encodeURIComponent;
export const href = {
  home: () => "/",
  engines: () => "/engines",
  project: (engine: string, project: string, tab: ProjectTab = "agents") =>
    `/e/${enc(engine)}/p/${enc(project)}${tab === "agents" ? "" : "/" + tab}`,
  agent: (engine: string, project: string, agent: string, tab: "overview" | "logs" = "overview") =>
    `/e/${enc(engine)}/p/${enc(project)}/a/${enc(agent)}${tab === "logs" ? "/logs" : ""}`,
};

export const nav = $state({ path: "/", route: { name: "home" } as Route, previous: "/" });

function sync() {
  nav.path = location.pathname + location.search;
  nav.route = parse(location.pathname);
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
