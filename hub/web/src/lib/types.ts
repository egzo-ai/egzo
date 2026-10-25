// The shapes of the hub's JSON API (hub/internal/web/api.go; docs/api.md). Times are RFC 3339 strings.

export type Role = "user" | "administrator" | "owner";

/** A person: the username the hub chose, then what their login provider says (display only, from their latest sign-in). */
export interface User {
  id: string;
  username: string;
  name: string;
  email: string;
  emailVerified: boolean;
  /** An https address the browser fetches, or "". */
  picture: string;
  isOperator: boolean;
  disabled: boolean;
}

/** One account at a login provider. `current` is false for an account of a former provider: it cannot sign in. */
export interface Identity {
  id: string;
  issuer: string;
  email: string;
  name: string;
  current: boolean;
  lastLoginAt: string;
}

/** The ways in the sign-in page offers. */
export interface SignInOptions {
  local: boolean;
  /** What the single sign-on button says; "" without single sign-on. */
  sso: string;
  redirect?: string;
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
  identities: Identity[];
  /** Signs in with a password (the starter account, while local login is on). */
  hasPassword: boolean;
  /** What single sign-on is called; "" without it. */
  sso: string;
  issuer: string;
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

/** How far the agents' activity can be trusted: the status the control sidecar logs, followed by the hub. */
export interface Feed {
  state: "live" | "connecting" | "stale" | "unavailable";
  message?: string;
}

export interface ProjectBrief {
  engine: string;
  name: string;
  role: Role;
  /** Held as an operator: every project, with no grant. */
  operator: boolean;
  feed: Feed;
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
  /** Hosts the proxy refused the agent in the last hour, and that it has not reached since. */
  denied: number;
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
  feed: Feed;
  up: boolean;
  templates: string[];
  services: Service[];
  agents: Agent[] | null;
  /** Spawns started from the hub that are running, or ended a short while ago. */
  spawns: Spawn[];
  canSpawn: boolean;
  canManage: boolean;
  canAudit: boolean;
}

/** A spawn the hub runs in the background: `agent` once it is ready, `error` when it failed. */
export interface Spawn {
  id: string;
  template: string;
  /** As asked: empty when the engine chooses. */
  name: string;
  creator: string;
  state: "spawning" | "ready" | "failed";
  agent?: string;
  error?: string;
  startedAt: string;
  endedAt?: string;
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
  lastLoginAt: string | null;
  hasPassword: boolean;
  identities: Identity[];
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

/** Why the proxy decided what it did: a code from a fixed list (roadmap/proxy-audit.md), and a text for people. */
export interface NetworkReason {
  code: string;
  text: string;
}

/** One event of a proxy's audit trail: a step of one connection. */
export interface NetworkEvent {
  time: string;
  conn: string;
  protocol: "https" | "ssh";
  event: string;
  decision?: "allow" | "deny" | "error" | "challenge";
  mode?: "tunnel" | "inject" | "inspect" | "intercept";
  agent?: string;
  instance?: string;
  profile?: string;
  host?: string;
  server?: string;
  method?: string;
  path?: string;
  status?: number;
  command?: string;
  reason?: NetworkReason;
  bytesUp?: number;
  bytesDown?: number;
  durationMs?: number;
}

/** What the proxy refused, or failed at, for one instance, one host and one reason, however many times. */
export interface Denial {
  instance: string;
  agent: string;
  creator: string;
  protocol: "https" | "ssh";
  host: string;
  server?: string;
  code: string;
  event: string;
  decision: "deny" | "error";
  profile: string;
  text: string;
  count: number;
  first: string;
  last: string;
  /** When the same instance reached the host afterwards; null while it has not. */
  fixed: string | null;
}

export interface Network {
  feed: Feed;
  /** The most recently seen first. */
  denials: Denial[];
  /** The latest events the hub heard, newest first. */
  events: NetworkEvent[];
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
