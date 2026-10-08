import type { QuotaAccount, RoutingObservation } from './api';
import { t } from './messages';
import styles from './QuotaDashboard.module.css';

export function connectionAccounts(accounts: QuotaAccount[], error: string): QuotaAccount[] {
  return error ? accounts.map((account) => ({ ...account, freshness: 'stale' })) : accounts;
}

export function ConnectionStatus({ error, checked, routing }: { error: string; checked: Date | null; routing?: RoutingObservation | null }) {
  return <>
    <div className={styles.status} role="status">
      {!error && checked && <span className={styles.live} />}
      {!error && <span>{t(checked ? 'auto_refresh' : 'connecting')}</span>}
      {checked && <span>{t('checked', { time: checked.toLocaleTimeString() })}</span>}
      {routing && <span className={styles.strategy} title={t('routing_scope_hint')}>
        {t('selected_strategy')}: {routing.strategy_name}
        {routing.session_affinity ? ` · ${t('session_affinity')}` : ''}
      </span>}
    </div>
    {error && <div role="alert" className={styles.error}>
      <strong>{t('connection_lost')}</strong>
      {error !== t('connection_lost') && <div>{error}</div>}
    </div>}
  </>;
}
