interface PollingEnvironment {
  visible: () => boolean;
  interval: (callback: () => void, milliseconds: number) => () => void;
  visibilityChanges: (callback: () => void) => () => void;
}

/** Poll visible snapshots, serializing requests and removing all listeners on cleanup. */
export function startQuotaPolling(load: () => Promise<void>, environment: PollingEnvironment) {
  let disposed = false;
  let pending = false;
  const refresh = async () => {
    if (disposed || pending || !environment.visible()) return;
    pending = true;
    try {
      await load();
    } catch {
      // The page reports request errors; keep later scheduled refreshes alive.
    } finally {
      pending = false;
    }
  };
  const tick = () => {
    void refresh();
  };
  const stopTimer = environment.interval(tick, 60000);
  const stopVisibility = environment.visibilityChanges(tick);
  tick();
  return () => {
    disposed = true;
    stopTimer();
    stopVisibility();
  };
}
