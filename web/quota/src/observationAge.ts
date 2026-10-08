import type { QuotaAccount, RoutingObservation } from './api';

export const OBSERVATION_WARNING_AGE = 10 * 60 * 1000;

/** Presentation age policy; backend probe scheduling and routing ranks are unchanged. */
export function observationPresentation(accounts: QuotaAccount[], routing: RoutingObservation | null, now: number) {
  const ages = new Map(accounts.map((account) => {
    const observed = account.observed_at ? Date.parse(account.observed_at) : NaN;
    const usable = Number.isFinite(observed) && observed <= now && account.windows.length > 0
      && account.quota_status !== 'unknown' && account.quota_status !== 'error';
    return [account.credential_id, { usable, stale: !usable || now - observed >= OBSERVATION_WARNING_AGE }] as const;
  }));
  return {
    accounts: accounts.map((account) => ({ ...account, freshness: ages.get(account.credential_id)?.stale ? 'stale' : 'fresh' })),
    routing: routing && { ...routing, accounts: routing.accounts.map((item) => {
      const age = ages.get(item.credential_id);
      const quotaDerived = routing.strategy === 'quota-reset'
        && ['weekly-reset', 'untouched-five-hour', 'quota-exhausted'].includes(item.reason);
      return quotaDerived && age?.usable ? { ...item, estimated: age.stale } : item;
    }) },
  };
}
