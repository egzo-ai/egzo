// The shapes of the hub's JSON API (hub/internal/web/api.go; docs/api.md). Times are RFC 3339 strings.

export type Role = "user" | "administrator" | "owner";

export interface User {
  id: string;
  name: string;
  email: string;
  isOperator: boolean;
}

export interface EngineState {
  name: string;
  host?: string; // operators only
  up: boolean;
  err?: string;
}

export interface Session {
  user: User;
  engines: EngineState[];
}

/** One agent, as the project list and the sidebar know it (from the last scan). */
export interface AgentBrief {
  name: string;
  template: string;
  running: boolean;
  activity: string;
  status: string;
  waiting: boolean;
}

export interface ProjectBrief {
  engine: string;
  name: string;
  role: Role;
  /** Held as an operator: every project, with no grant. */
  operator: boolean;
  up: boolean;
  agents: AgentBrief[];
}

export interface ProjectList {
  projects: ProjectBrief[];
  unreachable: EngineState[];
}

export interface Agent {
  name: string;
  template: string;
  state: string;
  running: boolean;
  activity: string;
  status: string;
  waiting: boolean;
  stale: boolean;
  creator: string;
  createdAt: string;
  canOperate: boolean;
  holder: string;
}

export interface Service {
  name: string;
  state: string;
  health?: string;
}

export interface Project {
  engine: string;
  name: string;
  role: Role;
  /** Held as an operator: every project, with no grant. */
  operator: boolean;
  up: boolean;
  templates: string[];
  services: Service[];
  agents: Agent[] | null;
  canSpawn: boolean;
  canManage: boolean;
  canAudit: boolean;
}

export interface AgentAnswer {
  project: Project;
  agent: Agent;
}

export interface TimelineEvent {
  time: string;
  type: string;
  agent: string;
  actor: string;
  text: string;
}

export interface Member {
  user: User;
  role: Role;
  /** An operator holds the project without a role: the row cannot be changed here. */
  operator: boolean;
}

/** A person who has signed in, as the Users page lists them. */
export interface UserRow extends User {
  createdAt: string;
}

export interface Members {
  members: Member[];
  candidates: User[];
  roles: Role[];
  me: string;
}

export interface AuditEntry {
  at: string;
  actor: string;
  action: string;
  agent: string;
  detail: string;
  allowed: boolean;
}

export interface EngineInput {
  name: string;
  host: string;
  sshUser?: string;
  ca?: string;
  cert?: string;
  key?: string;
  sshKey?: string;
}

export interface PendingHostKey {
  name: string;
  host: string;
  fingerprint: string;
  token: string;
}

export type SaveEngineAnswer = { saved: string } | { pending: PendingHostKey };

/** The control frames of the terminal websocket (text frames; binary frames are the terminal's bytes). */
export type ControlFrame =
  | { type: "busy"; holder: string }
  | { type: "taken"; holder: string }
  | { type: "error"; message: string };
