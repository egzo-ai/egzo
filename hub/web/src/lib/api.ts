import type {
  AgentAnswer, AuditEntry, Link, Members, Network, Project, ProjectList, Session, SignInOptions, Task, TimelineEvent, UserRow,
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

async function request<T>(method: string, path: string, body?: unknown, prefix = "/api/v1"): Promise<T> {
  const init: RequestInit = { method, credentials: "same-origin", headers: { Accept: "application/json" } };
  if (method !== "GET") {
    (init.headers as Record<string, string>)["Content-Type"] = "application/json";
    init.body = JSON.stringify(body ?? {});
  }
  let response: Response;
  try {
    response = await fetch(prefix + path, init);
  } catch {
    throw new ApiError(0, "network", "The hub cannot be reached. Check your connection.");
  }
  if (response.status === 401 && prefix === "/api/v1") onUnauthorized();
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
const project = (id: string) => `/p/${enc(id)}`;
const agent = (id: string, a: string) => `${project(id)}/a/${enc(a)}`;

export const api = {
  session: () => request<Session>("GET", "/session"),
  logout: () => request<{ ok: true }>("POST", "/logout"),
  signInOptions: () => request<SignInOptions>("GET", "/signin"),
  passwordLogin: (username: string, password: string) => request<{ ok: true }>("POST", "/login", { username, password }, "/auth"),
  /** Starts linking an account at the login provider; the answer is where to send the browser. */
  link: () => request<{ url: string }>("POST", "/me/link"),
  unlink: (identity: string) => request<{ ok: true }>("DELETE", `/me/identities/${enc(identity)}`),
  projects: () => request<ProjectList>("GET", "/projects"),

  /** Every registration, whatever its state: operators. */
  links: () => request<{ links: Link[] }>("GET", "/links").then((r) => r.links),
  revokeLink: (id: string, message: string) => request<{ ok: true }>("POST", `/links/${enc(id)}/revoke`, { message }),
  forgetLink: (id: string) => request<{ ok: true }>("DELETE", `/links/${enc(id)}`),
  /** The project that waits under a device code: operators. */
  pendingLink: (code: string) => request<{ link: Link }>("GET", `/register/${enc(code)}`).then((r) => r.link),
  approveLink: (code: string) => request<{ project: string }>("POST", `/register/${enc(code)}`, { approve: true }).then((r) => r.project),
  refuseLink: (code: string, message: string) => request<{ ok: true }>("POST", `/register/${enc(code)}`, { approve: false, message }),

  users: () => request<{ users: UserRow[]; me: string }>("GET", "/users"),
  setOperator: (id: string, isOperator: boolean) => request<{ ok: true }>("PUT", `/users/${enc(id)}`, { isOperator }),
  setDisabled: (id: string, disabled: boolean) => request<{ ok: true }>("PUT", `/users/${enc(id)}`, { disabled }),
  removeIdentity: (user: string, identity: string) => request<{ ok: true }>("DELETE", `/users/${enc(user)}/identities/${enc(identity)}`),

  project: (id: string) => request<Project>("GET", project(id)),
  events: (id: string, agentName = "", limit = 40) =>
    request<{ events: TimelineEvent[] }>("GET", `${project(id)}/events?limit=${limit}${agentName ? "&agent=" + enc(agentName) : ""}`).then((r) => r.events),
  /** Answered at once: the hub runs the spawn as a task, and the agent is in the project from then on. */
  spawn: (id: string, template: string, agentName: string) =>
    request<{ task: Task }>("POST", `${project(id)}/spawn`, { template, name: agentName }).then((r) => r.task),
  /** The project's activity, or one agent's: the latest tasks, newest first. */
  tasks: (id: string, agentName = "") =>
    request<{ tasks: Task[] }>("GET", `${project(id)}/tasks${agentName ? "?agent=" + enc(agentName) : ""}`).then((r) => r.tasks),
  task: (id: string, task: string) => request<{ task: Task }>("GET", `${project(id)}/tasks/${enc(task)}`).then((r) => r.task),
  /** Takes an ended task off its agent. */
  dismissTask: (id: string, task: string) => request<{ ok: true }>("DELETE", `${project(id)}/tasks/${enc(task)}`),
  spec: (id: string) => request<{ text: string }>("GET", `${project(id)}/spec`).then((r) => r.text),
  members: (id: string) => request<Members>("GET", `${project(id)}/members`),
  setRole: (id: string, user: string, role: string) =>
    request<{ ok: true }>("POST", `${project(id)}/members`, { user, role }),
  audit: (id: string) => request<{ entries: AuditEntry[] }>("GET", `${project(id)}/audit`).then((r) => r.entries),
  network: (id: string, agentName = "") =>
    request<Network>("GET", `${project(id)}/network${agentName ? "?agent=" + enc(agentName) : ""}`),

  agent: (id: string, a: string) => request<AgentAnswer>("GET", agent(id, a)),
  /** Answered at once: the hub runs it as a task, shown on the agent. */
  operate: (id: string, a: string, verb: "start" | "stop" | "rm") =>
    request<{ task: Task }>("POST", `${agent(id, a)}/${verb}`).then((r) => r.task),
  logs: (id: string, a: string) => request<{ text: string }>("GET", `${agent(id, a)}/logs`).then((r) => r.text),
};

/** The websocket address of an agent's terminal. */
export function terminalURL(id: string, a: string, takeover: boolean): string {
  const scheme = location.protocol === "https:" ? "wss://" : "ws://";
  return `${scheme}${location.host}/api/v1${agent(id, a)}/terminal${takeover ? "?takeover=1" : ""}`;
}
