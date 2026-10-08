import type { QuotaAccount, RoutingObservation, AccountGroups } from './api';

export function resolveGroupSelection(group: string, groups: AccountGroups): string {
  return group === 'all' || groups.groups.some((item) => String(item.id) === group)
    ? group : 'all';
}

// Sort globally first. Filtering never recalculates a rank or a strategy value.
export function routingAccounts(accounts: QuotaAccount[], routing: RoutingObservation | null, groups: AccountGroups, group: string) {
  const ranks = new Map(routing?.accounts.map((item) => [item.credential_id, item.rank]));
  const members = new Set(groups.bindings.filter((item) => String(item.channel_group_id) === group).map((item) => item.auth_id));
  return [...accounts].sort((a, b) => (ranks.get(a.credential_id) ?? Infinity) - (ranks.get(b.credential_id) ?? Infinity) || a.displayName.localeCompare(b.displayName))
    .filter((account) => group === 'all' || members.has(account.credential_id));
}

export function routingValue(id: string, routing?: RoutingObservation | null): string {
  const item = routing?.accounts.find((account) => account.credential_id === id);
  if (!item || !routing) return 'Priority unavailable';
  const rank = item.rank == null ? '' : `${routing.order_kind === 'tier' ? 'Tier ' : ''}#${item.rank} · `;
  let value: string;
  switch (item.reason) {
    case 'untouched-five-hour': value = 'Untouched 5h'; break;
    case 'weekly-reset': {
      const seconds = item.weekly_reset_at ? Math.max(0, (Date.parse(item.weekly_reset_at) - Date.parse(routing.generated_at)) / 1000) : NaN;
      value = Number.isFinite(seconds) ? `Weekly reset in ${seconds >= 3600 ? `${Math.ceil(seconds / 3600)}h` : `${Math.ceil(seconds / 60)}m`}` : 'Weekly reset unknown';
      break;
    }
    case 'quota-exhausted': value = 'Quota exhausted · fallback'; break;
    case 'quota-unknown': value = 'Unknown/stale quota · fallback'; break;
    case 'static-priority': value = `Priority ${item.priority}`; break;
    case 'rotation': value = `Priority ${item.priority} · Rotation`; break;
    case 'weighted-rotation': value = `Priority ${item.priority} · Weight ${item.weight}`; break;
    case 'disabled': value = 'Disabled'; break;
    case 'zero-weight': value = 'Excluded · Weight 0'; break;
    case 'unavailable': value = 'Unavailable'; break;
    default: value = 'Priority unavailable';
  }
  return rank + value + (item.estimated ? ' · Estimated' : '') + (item.model_dependent ? ' · Model cooldown' : '');
}

// Intersect with any group filter without changing global ranks.
export function filterUserAccounts(accounts: QuotaAccount[], users: import('./api').QuotaUser[], user: string) {
  if (user === 'all') return accounts;
  const scope = users.find((item) => String(item.id) === user);
  const allowed = new Set(scope?.credential_ids ?? []);
  return accounts.filter((account) => allowed.has(account.credential_id));
}
export function resolveUserSelection(user: string, users: import('./api').QuotaUser[]): string {
  return user === 'all' || users.some((item) => String(item.id) === user) ? user : 'all';
}
