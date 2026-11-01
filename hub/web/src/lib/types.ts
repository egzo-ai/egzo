// The shapes of the hub's JSON API (hub/internal/web/api.go; docs/api.md). Times are RFC 3339 strings.

/** A role that can be granted. */
export type Role = "viewer" | "user" | "admin" | "owner";
/** What a person holds on a project they are shown: a role, or "overseer" for an operator with none. */
export type Held = Role | "overseer";

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
  /** How many projects they may connect to the hub; an operator is not held to it. */
  projectQuota: number;
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

export interface Session {
  user: User;
  identities: Identity[];
  /** Signs in with a password (the starter account, while local login is on). */
  hasPassword: boolean;
  /** What single sign-on is called; "" without it. */
  sso: string;
  issuer: string;
  /** The address projects name in `hub:`. */
  hub: string;
  /** They may make a link code now: an operator, or somebody with room in their quota. */
  canLink: boolean;
  connectedProjects: number;
}

/** One agent, as the project list and the sidebar know it (from what its link pushed). */
export interface AgentBrief {
  name: string;
  template: string;
  running: boolean;
  activity: string;
  status: string;
  waiting: boolean;
  /** The verb of the task the hub runs on it. */
  task?: TaskVerb;
}

/** How far the agents' activity can be trusted: the status the control sidecar logs, followed by the hub. */
export interface Feed {
  state: "live" | "connecting" | "stale" | "unavailable";
  message?: string;
}

export interface ProjectBrief {
  id: string;
  name: string;
  host: string;
  role: Held;
  /** Nobody who can sign in owns it. Told to operators only. */
  ownerless?: boolean;
  /** When its link went away; null while it is connected. */
  offlineSince: string | null;
  feed: Feed;
  up: boolean;
  agents: AgentBrief[];
}

export interface ProjectList {
  projects: ProjectBrief[];
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
  /** What the hub does on it, or the failure of what it did that nobody dismissed. With no `state`, the project does not have the
   * agent: a spawn that runs or failed, or a removal that is ending. */
  task?: Task;
}

export type TaskVerb = "spawn" | "start" | "stop" | "rm";

/** One action of the hub on a project. `interrupted`: the hub stopped, or the link dropped, while it ran. */
export interface Task {
  id: string;
  verb: TaskVerb;
  agent: string;
  template?: string;
  /** Who asked. */
  creator: string;
  state: "running" | "done" | "failed" | "interrupted";
  /** What it does now, or did last. */
  step?: string;
  error?: string;
  startedAt: string;
  endedAt?: string;
  /** The user may take an ended one off its agent. */
  canDismiss: boolean;
}

export interface Service {
  name: string;
  state: string;
  health?: string;
}

export interface Project {
  id: string;
  name: string;
  host: string;
  role: Held;
  /** Nobody who can sign in owns it. Told to operators only. */
  ownerless?: boolean;
  /** When its link went away; null while it is connected. */
  offlineSince: string | null;
  feed: Feed;
  up: boolean;
  templates: string[];
  services: Service[];
  agents: Agent[] | null;
  canSpawn: boolean;
  /** May grant roles and invite. */
  canManage: boolean;
  canUnlink: boolean;
  /** May see who holds what: an owner, or an operator. */
  canMembers: boolean;
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
}

/** A person who has signed in, as the Users page lists them. */
export interface UserRow extends User {
  createdAt: string;
  lastLoginAt: string | null;
  hasPassword: boolean;
  identities: Identity[];
  /** The projects they connected, which their quota is measured against. */
  projects: number;
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
  client?: string;
}

/** An entry of the hub's own log, which spans the projects: `projectName` is empty for a project that is gone. */
export interface HubAuditEntry extends AuditEntry {
  id: number;
  project?: string;
  projectName?: string;
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
  /** Who, once the proxy checked the credentials (for ssh, the key's signature); never set otherwise. */
  agent?: string;
  instance?: string;
  profile?: string;
  /** The name the client sent in its proxy credentials, right or wrong (https only). */
  claimedAgent?: string;
  /** The address the connection came from. */
  client?: string;
  /** What the client says it is: a hint, never proof. */
  userAgent?: string;
  host?: string;
  /** The service of the profile that covers the host, on an allowed connect. */
  service?: string;
  server?: string;
  method?: string;
  /** Decoded, as the agent wrote it; rawPath is how it went on the wire, when its escapes make it differ. */
  path?: string;
  rawPath?: string;
  status?: number;
  /** On a request: the service whose credential the proxy set, and those whose placeholder it swapped for the secret. */
  injected?: string;
  substituted?: string[];
  command?: string;
  reason?: NetworkReason;
  bytesUp?: number;
  bytesDown?: number;
  durationMs?: number;
}

/** What the proxy refused, or failed at, for one instance, one host and one reason, however many times. */
export interface Denial {
  instance: string;
  /** Proved by the credentials; empty when the proxy could not tell. */
  agent: string;
  /** The name the client sent in its proxy credentials: part of the group. */
  claimedAgent?: string;
  /** The address of the latest one, and what its client said it was. */
  client?: string;
  userAgent?: string;
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

/** A registration: a project that presented a link code, and goes by its key from then on. */
export interface Link {
  id: string;
  name: string;
  host: string;
  release: string;
  templates: string[];
  state: "approved" | "revoked";
  /** Who connected it. */
  owner?: string;
  /** Who can answer for it now: none, on an approved project, is for the operators to deal with. */
  owners: string[];
  message?: string;
  createdAt: string;
  /** When its link went away; null while it is connected. */
  offlineSince: string | null;
}

/** A link code as it is listed: the code itself is shown once, when it is made. */
export interface LinkCode {
  id: string;
  createdBy: string;
  createdAt: string;
  expiresAt: string;
}

/** A link code that was just made. */
export interface NewLinkCode {
  id: string;
  code: string;
  expiresAt: string;
  /** The address the project names in `hub:`. */
  hub: string;
}

/** An invite as it is listed: its link is shown once, when it is made. With a project it grants a role, without one the flags on a user. */
export interface Invite {
  id: string;
  createdBy: string;
  createdAt: string;
  expiresAt: string;
  project?: string;
  projectName?: string;
  role?: Role;
  operator: boolean;
  projectQuota: number;
}

/** An invite that was just made. */
export interface NewInvite {
  id: string;
  url: string;
  expiresAt: string;
}

/** What a code grants and the ways to take it, for whoever holds the code. */
export interface InvitePreview {
  invite: Invite;
  local: boolean;
  sso: string;
}

/** The control frames of the terminal websocket (text frames; binary frames are the terminal's bytes). */
export type ControlFrame =
  | { type: "busy"; holder: string }
  | { type: "taken"; holder: string }
  | { type: "error"; message: string };
