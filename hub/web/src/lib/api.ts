import type {
  AgentAnswer, AuditEntry, EngineInput, EngineState, Members, Project, ProjectList, SaveEngineAnswer, Session, TimelineEvent,
} from "./types";

/** An error answer of the hub: {code, message} with a status. */
export class ApiError extends Error {
  constructor(public status: number, public code: string, message: string) {
    super(message);
  }
}

let onUnauthorized: () => void = () => {};
/** The app registers what happens when the session has ended (any call answered 401). */
export function setUnauthorizedHandler(f: () => void): void {
  onUnauthorized = f;
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const init: RequestInit = { method, credentials: "same-origin", headers: { Accept: "application/json" } };
  if (method !== "GET") {
    (init.headers as Record<string, string>)["Content-Type"] = "application/json";
    init.body = JSON.stringify(body ?? {});
  }
  let response: Response;
  try {
    response = await fetch("/api/v1" + path, init);
  } catch {
    throw new ApiError(0, "network", "The hub cannot be reached. Check your connection.");
  }
  if (response.status === 401) onUnauthorized();
  const text = await response.text();
  let data: unknown = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    /* not JSON: a proxy's page */
  }
  if (!response.ok) {
    const e = (data ?? {}) as { code?: string; message?: string };
    throw new ApiError(response.status, e.code ?? "error", e.message ?? `The hub answered ${response.status}.`);
  }
  return data as T;
}

const enc = encodeURIComponent;
const project = (engine: string, name: string) => `/e/${enc(engine)}/p/${enc(name)}`;
const agent = (engine: string, p: string, a: string) => `${project(engine, p)}/a/${enc(a)}`;

export const api = {
  session: () => request<Session>("GET", "/session"),
  logout: () => request<{ ok: true }>("POST", "/logout"),
  projects: () => request<ProjectList>("GET", "/projects"),

  engines: () => request<{ engines: EngineState[] }>("GET", "/engines").then((r) => r.engines),
  saveEngine: (input: EngineInput) => request<SaveEngineAnswer>("POST", "/engines", input),
  confirmEngine: (token: string) => request<SaveEngineAnswer>("POST", "/engines/confirm", { token }),
  removeEngine: (name: string) => request<{ removed: string }>("DELETE", `/engines/${enc(name)}`),

  project: (engine: string, name: string) => request<Project>("GET", project(engine, name)),
  events: (engine: string, name: string, agentName = "", limit = 40) =>
    request<{ events: TimelineEvent[] }>("GET", `${project(engine, name)}/events?limit=${limit}${agentName ? "&agent=" + enc(agentName) : ""}`).then((r) => r.events),
  spawn: (engine: string, name: string, template: string, agentName: string) =>
    request<{ agent: string }>("POST", `${project(engine, name)}/spawn`, { template, name: agentName }),
  spec: (engine: string, name: string) => request<{ text: string }>("GET", `${project(engine, name)}/spec`).then((r) => r.text),
  members: (engine: string, name: string) => request<Members>("GET", `${project(engine, name)}/members`),
  setRole: (engine: string, name: string, user: string, role: string) =>
    request<{ ok: true }>("POST", `${project(engine, name)}/members`, { user, role }),
  audit: (engine: string, name: string) => request<{ entries: AuditEntry[] }>("GET", `${project(engine, name)}/audit`).then((r) => r.entries),

  agent: (engine: string, p: string, a: string) => request<AgentAnswer>("GET", agent(engine, p, a)),
  operate: (engine: string, p: string, a: string, verb: "start" | "stop" | "rm") => request<{ agent: string }>("POST", `${agent(engine, p, a)}/${verb}`),
  logs: (engine: string, p: string, a: string) => request<{ text: string }>("GET", `${agent(engine, p, a)}/logs`).then((r) => r.text),
};

/** The websocket address of an agent's terminal. */
export function terminalURL(engine: string, p: string, a: string, takeover: boolean): string {
  const scheme = location.protocol === "https:" ? "wss://" : "ws://";
  return `${scheme}${location.host}/api/v1${agent(engine, p, a)}/terminal${takeover ? "?takeover=1" : ""}`;
}
