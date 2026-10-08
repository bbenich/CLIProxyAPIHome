import type { HomeSession } from './session';

export interface QuotaWindow {
  id: string;
  label: string | null;
  scope: string;
  remaining_ratio: number | null;
  used_ratio: number | null;
  reset_at: string | null;
  window_seconds: number | null;
}
export interface QuotaAccount {
  credential_id: string;
  provider: string;
  label: string;
  credential_status: string;
  freshness: string;
  quota_status?: string;
  observed_at: string | null;
  collection_status: string;
  error: { message: string } | null;
  windows: QuotaWindow[];
  displayName: string;
}
export interface RoutingAccount {
  credential_id: string;
  rank: number | null;
  reason: string;
  priority: number;
  weight: number;
  weekly_reset_at?: string;
  model_dependent: boolean;
  estimated?: boolean;
}
export interface RoutingObservation {
  strategy: string;
  strategy_name: string;
  order_kind: 'rank' | 'tier' | 'unknown';
  session_affinity: boolean;
  generated_at: string;
  accounts: RoutingAccount[];
}
export interface QuotaUser { id: number; username: string; key_count: number; credential_ids: string[] }
export interface UsageWindow { tokens: number; requests: number }
export interface RecentAccountUsage {
  credential_id: string;
  five_minutes: UsageWindow;
  hour: UsageWindow;
  day: UsageWindow;
}
export interface RecentUsage { generated_at: string; accounts: RecentAccountUsage[] }
export interface AccountGroup { id: number; channel_name: string; disabled: boolean }
export interface GroupBinding { channel_group_id: number; auth_id: string }
export interface AccountGroups { groups: AccountGroup[]; bindings: GroupBinding[] }
export function createQuotaApi(session: HomeSession, request = fetch) {
  async function get<T>(path: string): Promise<T> {
    const response = await request(`${session.baseUrl}/v8/management${path}`, {
      method: 'GET', headers: { Authorization: `Bearer ${session.managementKey}`, 'Content-Type': 'application/json' },
      signal: AbortSignal.timeout(30000),
      redirect: 'error',
    });
    if (!response.ok) throw new Error(response.status === 401
      ? 'Home connection expired. Reconnect using Connection in Home Center.'
      : `Unable to load quotas (HTTP ${response.status}).`);
    return response.json() as Promise<T>;
  }
  return {
  async list(): Promise<QuotaAccount[]> {
    const accounts: QuotaAccount[] = [];
    const credentials = await get<{
      files: { id: string; email?: string; label?: string }[];
    }>('/credentials');
    const names = new Map(credentials.files.map((file) => [file.id, file.email || file.label]));
    let offset = 0;
    while (true) {
      const response = await get<{
        items: Omit<QuotaAccount, 'windows' | 'displayName'>[];
        total: number;
      }>(`/quota/credentials?limit=200&offset=${offset}&provider=claude,codex`);
      // Details include 5h windows that the Codex list summary deliberately omits.
      const details = await Promise.all(
        response.items.map(async (item) => {
          const detail = await get<{ windows: QuotaWindow[] }>(
            `/quota/credentials/${encodeURIComponent(item.credential_id)}`
          );
          return {
            ...item,
            windows: detail.windows,
            displayName: names.get(item.credential_id) || item.label,
          };
        })
      );
      accounts.push(...details);
      offset += response.items.length;
      if (offset >= response.total || response.items.length === 0) return accounts;
    }
  },
  routing: () => get<RoutingObservation>('/quota/routing'),
  users: async () => (await get<{ users: QuotaUser[] }>('/quota/users')).users,
  recentUsage: () => get<RecentUsage>('/quota/recent-usage'),
  groups: async (): Promise<AccountGroups> => {
    const [groups, bindings] = await Promise.all([
      get<{ channel_groups: AccountGroup[] }>('/channel-groups'),
      get<{ channel_group_details: GroupBinding[] }>('/channel-group-details'),
    ]);
    return { groups: groups.channel_groups, bindings: bindings.channel_group_details };
  },
};
}
export type QuotaApi = ReturnType<typeof createQuotaApi>;
