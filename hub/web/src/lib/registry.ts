// Who can type into an open terminal, by its id: the leader's "send a literal Ctrl+]" needs it, and nothing else.
const senders = new Map<string, (data: string) => void>();

export function registerSender(id: string, send: (data: string) => void): () => void {
  senders.set(id, send);
  return () => senders.delete(id);
}

export function sendTo(id: string | null, data: string): void {
  if (id) senders.get(id)?.(data);
}
