/** Put text on the clipboard. The async API needs a secure context (https or localhost); a hub served over plain http falls back to a copy command, which needs a user gesture. */
export async function copyText(text: string): Promise<boolean> {
  if (window.isSecureContext && navigator.clipboard) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      /* fall through */
    }
  }
  return copyTextSync(text);
}

function copyTextSync(text: string): boolean {
  const area = document.createElement("textarea");
  area.value = text;
  area.setAttribute("readonly", "");
  area.style.cssText = "position:fixed;top:0;left:0;opacity:0";
  const previous = document.activeElement as HTMLElement | null;
  document.body.appendChild(area);
  area.select();
  try {
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    area.remove();
    previous?.focus();
  }
}

/** The text of an OSC 52 "set clipboard" payload (`c;<base64>`), or null for a query or garbage. */
export function osc52Text(data: string): string | null {
  const i = data.indexOf(";");
  const b64 = i < 0 ? "" : data.slice(i + 1);
  if (b64 === "" || b64 === "?") return null;
  try {
    return new TextDecoder().decode(Uint8Array.from(atob(b64), (c) => c.charCodeAt(0)));
  } catch {
    return null;
  }
}
