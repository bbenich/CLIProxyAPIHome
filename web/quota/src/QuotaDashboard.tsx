import { useCallback, useEffect, useRef, useState } from 'react';
import { t } from './messages';
import { Button } from './Button';
import { type QuotaApi, type QuotaAccount, type RoutingObservation, type AccountGroups, type QuotaUser, type RecentUsage } from './api';
import { resolveGroupSelection, routingAccounts, filterUserAccounts, resolveUserSelection } from './routing';
import { QuotaAccounts } from './QuotaAccounts';
import { OptionGroup, ViewIcon } from './OptionGroup';
import { SearchPicker } from './SearchPicker';
import { readPreferences, savePreferences, type QuotaPreferences } from './preferences';
import { startQuotaPolling } from './polling';
import { ConnectionStatus, connectionAccounts } from './ConnectionStatus';
import { observationPresentation } from './observationAge';
import styles from './QuotaDashboard.module.css';

export function QuotaDashboard({ api }: { api: QuotaApi }) {
  const [preferences, setPreferences] = useState(() => readPreferences());
  const updatePreferences = (patch: Partial<QuotaPreferences>) => {
    const next = { ...preferences, ...patch };
    setPreferences(next);
    savePreferences(next);
  };
  const [accounts, setAccounts] = useState<QuotaAccount[]>([]);
  const [error, setError] = useState('');
  const [checked, setChecked] = useState<Date | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [routing, setRouting] = useState<RoutingObservation | null>(null);
  const [groups, setGroups] = useState<AccountGroups>({ groups: [], bindings: [] });
  const [group, setGroup] = useState('all');
  const [users, setUsers] = useState<QuotaUser[]>([]);
  const [user, setUser] = useState('all');
  const [usage, setUsage] = useState<RecentUsage | null>(null);
  const busy = useRef(false);
  const generation = useRef(0);
  const load = useCallback(
    async () => {
      if (busy.current) return;
      busy.current = true;
      const token = generation.current;
      const current = () => generation.current === token;
      setRefreshing(true);
      try {
        const [items, routing, groups, users, usage] = await Promise.all([
          api.list(),
          api.routing(),
          api.groups(),
          api.users(),
          api.recentUsage(),
        ]);
        if (!current()) return;
        if (!navigator.onLine) throw new Error(t('connection_lost'));
        setAccounts(items);
        setRouting(routing);
        setGroups(groups);
        setUsers(users);
        setUsage(usage);
        setUser((selected) => resolveUserSelection(selected, users));
        setGroup((selected) => resolveGroupSelection(selected, groups));
        setChecked(new Date());
        setError('');
      } catch (err) {
        if (current()) setError(err instanceof Error ? err.message : t('quota_dashboard.error'));
      } finally {
        if (current()) {
          busy.current = false;
          setRefreshing(false);
        }
      }
    },
    [api]
  );
  useEffect(() => {
    generation.current += 1;
    busy.current = false;
    setAccounts([]);
    setRouting(null);
    setGroups({ groups: [], bindings: [] });
    setGroup('all');
    setUsers([]);
    setUsage(null);
    setUser('all');
    setChecked(null);
    setError('');
    const stopPolling = startQuotaPolling(() => load(), {
      visible: () => document.visibilityState === 'visible',
      interval: (callback, milliseconds) => {
        const timer = window.setInterval(callback, milliseconds);
        return () => window.clearInterval(timer);
      },
      visibilityChanges: (callback) => {
        document.addEventListener('visibilitychange', callback);
        return () => document.removeEventListener('visibilitychange', callback);
      },
    });
    const offline = () => setError(t('connection_lost'));
    const online = () => void load();
    window.addEventListener('offline', offline);
    window.addEventListener('online', online);
    return () => {
      generation.current += 1;
      stopPolling();
      window.removeEventListener('offline', offline);
      window.removeEventListener('online', online);
    };
  }, [load]);
  const presentation = observationPresentation(accounts, routing, Date.now());
  const displayedAccounts = connectionAccounts(filterUserAccounts(routingAccounts(presentation.accounts, presentation.routing, groups, group), users, user), error);
  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <h1>{t('quota_dashboard.title')}</h1>
          <p>{t('quota_dashboard.subtitle')}</p>
        </div>
        <Button
          variant="secondary"
          disabled={refreshing}
          onClick={() => void load()}
        >
          {t(refreshing ? 'quota_dashboard.refreshing' : 'quota_dashboard.refresh')}
        </Button>
      </header>
      <div className={styles.preferences}>
        <OptionGroup label={t('view')} value={preferences.view} onChange={(view) => updatePreferences({ view })} options={[
          { value: 'cards', label: t('cards'), icon: <ViewIcon /> },
          { value: 'table', label: t('table'), icon: <ViewIcon table /> },
        ]} />
        <OptionGroup label={t('size')} value={preferences.size} onChange={(size) => updatePreferences({ size })} options={[
          { value: 'small', label: t('small') }, { value: 'medium', label: t('medium') }, { value: 'large', label: t('large') },
        ]} />
        <SearchPicker label={t('user')} value={user} onChange={setUser} options={[
          { value: 'all', label: t('all_users') },
          ...users.map((item) => ({ value: String(item.id), label: item.username })),
        ]} />
        <SearchPicker label={t('group')} value={group} onChange={setGroup} options={[
          { value: 'all', label: t('all_accounts') },
          ...groups.groups.map((item) => ({ value: String(item.id), label: item.channel_name + (item.disabled ? ' (disabled)' : '') })),
        ]} />
      </div>
      <ConnectionStatus error={error} checked={checked} routing={routing} />
      {!displayedAccounts.length && !error && (
        <p>{t(refreshing ? 'quota_dashboard.refreshing' : 'quota_dashboard.empty')}</p>
      )}
      <div className={styles.contents} data-size={preferences.size}>
        <QuotaAccounts accounts={displayedAccounts} view={preferences.view} routing={presentation.routing} users={users} usage={usage} groups={groups} />
      </div>
    </div>
  );
}
