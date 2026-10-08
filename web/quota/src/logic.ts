import type { QuotaAccount, QuotaWindow } from './api';

export function remainingPercent(window: QuotaWindow): number | null {
  const value =
    window.remaining_ratio ?? (window.used_ratio == null ? null : 1 - window.used_ratio);
  return value == null || !Number.isFinite(value) ? null : Math.max(0, Math.min(100, value * 100));
}
// Remaining percent an even burn rate would leave now: the share of the window
// still ahead of its reset. Unstarted or already-reset windows have no pace.
export function linearPacePercent(window: QuotaWindow, now: number): number | null {
  const reset = window.reset_at ? Date.parse(window.reset_at) : NaN;
  const seconds = window.window_seconds ?? 0;
  if (!Number.isFinite(reset) || seconds <= 0 || reset <= now) return null;
  return Math.min(1, (reset - now) / (seconds * 1000)) * 100;
}
export function sortAccounts(accounts: QuotaAccount[]): QuotaAccount[] {
  return [...accounts].sort((a, b) => {
    const reset = (account: QuotaAccount) => {
      const weekly = account.windows.find(
        (window) => window.scope === 'account' && window.window_seconds === 604800
      );
      const timestamp = weekly?.reset_at ? Date.parse(weekly.reset_at) : NaN;
      return Number.isFinite(timestamp) ? timestamp : Infinity;
    };
    return (
      a.provider.localeCompare(b.provider) ||
      reset(a) - reset(b) ||
      a.displayName.localeCompare(b.displayName)
    );
  });
}
