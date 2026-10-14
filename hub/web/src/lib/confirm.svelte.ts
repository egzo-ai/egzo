// Confirmation dialogs for what cannot be undone. Focus starts on Cancel.
export interface ConfirmRequest {
  title: string;
  lines: string[];
  confirm: string;
  tone?: "danger" | "normal";
  /** The person must type this to enable the confirm button. */
  typed?: string;
}

export const confirmation = $state<{ request: ConfirmRequest | null }>({ request: null });
let resolver: ((ok: boolean) => void) | null = null;

export function ask(request: ConfirmRequest): Promise<boolean> {
  resolver?.(false);
  confirmation.request = request;
  return new Promise((resolve) => {
    resolver = resolve;
  });
}

export function answer(ok: boolean): void {
  confirmation.request = null;
  resolver?.(ok);
  resolver = null;
}
