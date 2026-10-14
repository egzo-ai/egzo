/** Run `load` now and every few seconds while the tab is visible; calls never overlap. Returns the function that stops it. */
export function poll(load: () => Promise<void>, ms = 3000): () => void {
  let stopped = false;
  let busy = false;
  const tick = async () => {
    if (stopped || busy) return;
    busy = true;
    try {
      await load();
    } catch {
      /* the page shows what the last good load said */
    } finally {
      busy = false;
    }
  };
  void tick();
  const timer = window.setInterval(() => {
    if (!document.hidden) void tick();
  }, ms);
  return () => {
    stopped = true;
    window.clearInterval(timer);
  };
}
