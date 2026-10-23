// What the Network view says about the proxy's audit trail: why a denial happened and how to fix it, by its reason code
// (roadmap/proxy-audit.md), and the events of one connection as one row.

import type { Denial, NetworkEvent, NetworkReason } from "./types";

export interface Explanation {
  /** A few words, for the table. */
  title: string;
  /** What happened, in a sentence. */
  why: string;
  /** What to do, in a sentence. */
  fix: string;
  /** The egzo.yaml to add, when the fix is a change of the profile. */
  yaml?: string;
  /** The command that applies it. */
  command?: string;
}

const up = "egzo project up";

/** Why a denial happened and how to fix it. */
export function explain(d: Pick<Denial, "code" | "host" | "profile" | "protocol" | "server" | "text">): Explanation {
  const profile = d.profile || "default";
  switch (d.code) {
    case "not-in-profile":
      return {
        title: "not in the profile",
        why: `${d.host} is not in the egress profile ${profile}.`,
        fix: `Add it to the profile's allow list, or to a service, then apply the change.`,
        yaml: `egress:\n  ${profile}:\n    allow: [${d.host}]`,
        command: up,
      };
    case "port":
      return { title: "not port 443", why: "The proxy lets agents out on port 443 only (HTTPS, and ssh to the servers of the profile).", fix: "Use the service over HTTPS. Other ports cannot be opened." };
    case "private-address":
      return { title: "private address", why: `${d.host} is a private, loopback or link-local address.`, fix: "Agents never reach private addresses, by design. Expose the service on a public HTTPS address, or run it in the agent." };
    case "credentials":
      return { title: "wrong proxy credentials", why: "The client sent proxy credentials that match no running agent.", fix: "Usually a tool kept the credentials of an agent that was removed. Restart the tool, or the agent." };
    case "no-policy":
      return { title: "the proxy has no policy", why: "The proxy was restarted and lost its policy, which lives in memory.", fix: "Load it again.", command: "egzo restart proxy" };
    case "limit":
      return { title: "too many connections", why: d.text || "The agent holds as many connections as the proxy allows.", fix: "Wait for connections to close; a tool that opens many at once may need to be told to open fewer." };
    case "client-hello":
    case "sni-mismatch":
      return { title: "TLS name mismatch", why: d.text || "The TLS server name differs from the host the agent asked for.", fix: "An allowed name cannot be used to reach another server. Allow the host the client really asks for." };
    case "host-header":
    case "host-mismatch":
      return { title: "request for another host", why: d.text || "A request named another host than its connection.", fix: "Allow that host too: each host is checked on its own." };
    case "placeholder":
      return { title: "placeholder not swapped", why: d.text || "A placeholder could not be swapped in the request.", fix: "Send the request uncompressed and under 1 MiB, so the proxy can swap the placeholder." };
    case "dial":
      return { title: "unreachable", why: d.text || `${d.host} did not answer.`, fix: "Check the host name; the host may be down, or not reachable from the engine's network." };
    case "tls-handshake":
      return { title: "CA not trusted", why: "The client's TLS handshake with the proxy failed: it does not trust the project CA.", fix: "Make the tool trust the project CA (SSL_CERT_FILE, NODE_EXTRA_CA_CERTS; Chromium needs certutil), or it pins its certificate. See docs/troubleshooting.md." };
    case "upstream":
      return { title: "upstream error", why: d.text || "A request to the host failed.", fix: "The host answered with an error or closed the connection: see the text." };
    case "identity":
      return { title: "unknown ssh identity", why: "The ssh client signed in with a key that is not an agent's, or asked for a server its profile does not name.", fix: `Use the server's name from the profile (ssh ${d.server || "NAME"}), from the agent itself.` };
    case "channel":
    case "forwarding":
    case "request":
      return { title: "not relayed", why: d.text || "The proxy relays ssh sessions only: no port forwarding, X11 or agent forwarding.", fix: "Run the command on the server instead of forwarding to it." };
    case "host-key":
      return { title: "host key mismatch", why: d.text || "The server presented another host key than the pinned one.", fix: "If the server's key changed on purpose, pin the new one in the profile and apply the change.", command: up };
    case "key":
      return { title: "unusable key", why: "The key of the profile for this server cannot be used.", fix: "Check the secret the profile names for the server's key.", command: up };
    case "auth":
      return { title: "key refused", why: "The server refused the profile's key.", fix: "Add the public half of the key to the server's authorized_keys, for the profile's user." };
    case "session":
      return { title: "no session", why: d.text || "The server did not open a session.", fix: "Check the server's sshd configuration and logs." };
  }
  return { title: d.code || "refused", why: d.text, fix: "" };
}

/** Parent domains that several denied hosts share, with the hosts: a wildcard may be what the profile needs. */
export function sharedParents(hosts: string[]): Map<string, string[]> {
  const byParent = new Map<string, Set<string>>();
  for (const host of new Set(hosts)) {
    const labels = host.split(".");
    if (labels.length < 3 || /^[\d.]+$/.test(host) || host.includes(":")) continue;
    const parent = labels.slice(1).join(".");
    if (!byParent.has(parent)) byParent.set(parent, new Set());
    byParent.get(parent)!.add(host);
  }
  const shared = new Map<string, string[]>();
  for (const [parent, set] of byParent) if (set.size >= 2) shared.set(parent, [...set].sort());
  return shared;
}

/** One connection: its events, oldest first, and what they add up to. */
export interface Connection {
  conn: string;
  time: string;
  protocol: "https" | "ssh";
  agent: string;
  host: string;
  server: string;
  decision: "allow" | "deny" | "error" | "challenge" | "";
  mode: string;
  reason?: NetworkReason;
  requests: number;
  bytesUp: number;
  bytesDown: number;
  durationMs: number | null;
  /** The commands an ssh connection ran. */
  commands: string[];
  steps: NetworkEvent[];
}

const worse = { "": 0, allow: 1, challenge: 2, error: 3, deny: 4 } as const;

/** The events as connections, the one with the newest event first. events is newest first, as the API answers it. */
export function connections(events: NetworkEvent[]): Connection[] {
  const byConn = new Map<string, Connection>();
  for (const e of [...events].reverse()) {
    let c = byConn.get(e.conn);
    if (!c) {
      c = { conn: e.conn, time: e.time, protocol: e.protocol, agent: e.agent ?? "", host: e.host ?? "", server: e.server ?? "", decision: "", mode: "",
        requests: 0, bytesUp: 0, bytesDown: 0, durationMs: null, commands: [], steps: [] };
      byConn.set(e.conn, c);
    }
    c.steps.push(e);
    c.time = e.time;
    if (e.agent) c.agent = e.agent;
    if (e.host) c.host = e.host;
    if (e.server) c.server = e.server;
    if (e.mode) c.mode = e.mode;
    if (e.event === "request") c.requests++;
    if (e.command) c.commands.push(e.command);
    if (e.event === "close") [c.bytesUp, c.bytesDown, c.durationMs] = [e.bytesUp ?? 0, e.bytesDown ?? 0, e.durationMs ?? 0];
    const d = e.decision;
    if (d) {
      // a client that left before its handshake is not a failure worth the row's colour
      const aborted = d === "error" && e.reason?.code === "aborted";
      if (!c.decision || (!aborted && worse[d] > worse[c.decision])) {
        c.decision = d;
        c.reason = e.reason ?? c.reason;
      }
    }
  }
  return [...byConn.values()].sort((a, b) => (a.time < b.time ? 1 : a.time > b.time ? -1 : 0));
}

/** A size for people. */
export function bytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(1)} GB`;
}

/** A duration for people. */
export function duration(ms: number): string {
  if (ms < 1000) return `${ms} ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(s < 10 ? 1 : 0)} s`;
  if (s < 3600) return `${Math.floor(s / 60)} min ${Math.round(s % 60)} s`;
  return `${Math.floor(s / 3600)} h ${Math.floor((s % 3600) / 60)} min`;
}
