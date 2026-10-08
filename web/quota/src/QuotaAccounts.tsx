import type { QuotaAccount, QuotaWindow, RoutingObservation, QuotaUser, RecentUsage, AccountGroups } from './api';
import { AccountUsage, AccountAccess } from './AccountMetrics';
import { routingValue } from './routing';
import type { QuotaView } from './preferences';
import { linearPacePercent, remainingPercent } from './logic';
import { t } from './messages';
import styles from './QuotaDashboard.module.css';

function windowLabel(window: QuotaWindow) {
  return window.scope === 'account' && window.window_seconds === 18000
    ? t('five_hour')
    : window.scope === 'account' && window.window_seconds === 604800
      ? t('weekly') : window.label || window.id;
}
function QuotaWindowView({ window, now, showLabel = true }: { window: QuotaWindow; now: number; showLabel?: boolean }) {
  const remaining = remainingPercent(window);
  const pace = linearPacePercent(window, now);
  const label = windowLabel(window);
  return <section className={styles.window} aria-label={label}>
    <div className={styles.windowHeading}>
      {showLabel && <h3>{label}</h3>}
      <strong>{remaining == null ? t('unknown') : t('remaining', { percent: Number(remaining.toFixed(1)) })}</strong>
    </div>
    {remaining != null && <div className={styles.bar} title={pace == null ? undefined : t('linear_pace', { percent: Math.round(pace) })}>
      <progress value={remaining} max={100} aria-label={`${label}: ${Number(remaining.toFixed(1))}%`} className={remaining <= 10 ? styles.low : undefined} />
      {pace != null && <span className={styles.pace} style={{ left: `${Number(pace.toFixed(2))}%` }} aria-hidden="true" />}
    </div>}
    <p>{t('reset', { time: window.reset_at ? new Date(window.reset_at).toLocaleString() : t('not_started') })}</p>
  </section>;
}
function Status({ account }: { account: QuotaAccount }) {
  return <span className={styles.badge}>{t(`status_${account.credential_status}`, { defaultValue: account.credential_status })}</span>;
}
function Observation({ account }: { account: QuotaAccount }) {
  return <div className={styles.observation}>
    <span className={account.freshness === 'fresh' ? '' : styles.warning}>{t(account.freshness === 'fresh' ? 'fresh' : 'stale')}</span>
    {account.observed_at && <time dateTime={account.observed_at}>{new Date(account.observed_at).toLocaleTimeString()}</time>}
    {account.error && <p className={styles.error}>{account.error.message}</p>}
  </div>;
}
function Windows({ windows, now, showLabel = true }: { windows: QuotaWindow[]; now: number; showLabel?: boolean }) {
  return <div className={styles.windows}>{[...windows]
    .sort((a, b) => (a.window_seconds ?? 0) - (b.window_seconds ?? 0))
    .map((window) => <QuotaWindowView key={window.id} window={window} now={now} showLabel={showLabel} />)}</div>;
}
const isFiveHour = (window: QuotaWindow) => window.scope === 'account' && window.window_seconds === 18000;
const isWeekly = (window: QuotaWindow) => window.scope === 'account' && window.window_seconds === 604800;

function RoutingValue({ account, routing }: { account: QuotaAccount; routing?: RoutingObservation | null }) {
  return <div className={styles.routingValue}>{routingValue(account.credential_id, routing)}</div>;
}

export function QuotaAccounts({ accounts, view, routing, users, usage, groups: accountGroups, now = Date.now() }: { accounts: QuotaAccount[]; view: QuotaView; routing?: RoutingObservation | null; users?: QuotaUser[]; usage?: RecentUsage | null; groups?: AccountGroups; now?: number }) {
  if (!accounts.length) return null;
  const usageByID = new Map(usage?.accounts.map((account) => [account.credential_id, account]));
  if (view === 'table') return <div className={styles.tableScroll} role="region" aria-label={t('table_label')} tabIndex={0}>
    <table className={styles.table}>
      <caption className={styles.srOnly}>{t('table_label')}</caption>
      <thead><tr>{['account', 'routing_priority', 'status', 'recent_usage', 'account_access', 'five_hour', 'weekly', 'other_quotas', 'observation'].map((key) => <th key={key} scope="col">{t(key)}</th>)}</tr></thead>
      <tbody>{accounts.map((account) => {
        const groups = [account.windows.filter(isFiveHour), account.windows.filter(isWeekly), account.windows.filter((window) => !isFiveHour(window) && !isWeekly(window))];
        return <tr key={account.credential_id}>
          <th scope="row" className={styles.accountCell}><strong>{account.displayName}</strong><span className={styles.provider}>{account.provider === 'claude' ? 'Claude' : 'Codex'}</span>{!account.windows.length && <p className={styles.noData}>{t('no_data')}</p>}</th>
          <td><RoutingValue account={account} routing={routing} /></td>
          <td><Status account={account} /></td>
          <td><AccountUsage usage={usageByID.get(account.credential_id)} generatedAt={usage?.generated_at} /></td>
          <td><AccountAccess credentialID={account.credential_id} users={users} groups={accountGroups} /></td>
          {groups.map((windows, index) => <td key={index}>{windows.length ? <Windows windows={windows} now={now} showLabel={index === 2} /> : <span className={styles.missing}>{t('unknown')}</span>}</td>)}
          <td><Observation account={account} /></td>
        </tr>;
      })}</tbody>
    </table>
  </div>;
  return <div className={styles.grid}>{accounts.map((account) => <article key={account.credential_id} className={styles.card}>
    <div className={styles.cardHeader}><span className={styles.provider}>{account.provider === 'claude' ? 'Claude' : 'Codex'}</span><Status account={account} /></div>
    <h2>{account.displayName}</h2>
    <RoutingValue account={account} routing={routing} />
    <div className={styles.metrics}>
      <AccountUsage usage={usageByID.get(account.credential_id)} generatedAt={usage?.generated_at} />
      <AccountAccess credentialID={account.credential_id} users={users} groups={accountGroups} />
    </div>
    <Windows windows={account.windows} now={now} />
    {!account.windows.length && <p className={styles.noData}>{t('no_data')}</p>}
    <footer className={styles.footer}><Observation account={account} /></footer>
  </article>)}</div>;
}
