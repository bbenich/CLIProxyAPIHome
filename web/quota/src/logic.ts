import type { QuotaAccount, QuotaWindow } from './api';

export function remainingPercent(window: QuotaWindow): number | null {
  const value =
    window.remaining_ratio ?? (window.used_ratio == null ? null : 1 - window.used_ratio);
  return value == null || !Number.isFinite(value) ? null : Math.max(0, Math.min(100, value * 100));
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
