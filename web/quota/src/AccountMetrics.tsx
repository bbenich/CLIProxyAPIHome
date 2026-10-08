import { useEffect, useId, useRef, useState, type ReactNode } from 'react';
import type { QuotaUser, RecentAccountUsage, AccountGroups } from './api';
import styles from './QuotaDashboard.module.css';

// Native popovers escape the scrolling table and keep the density/theme variables.
function MetricDetail({ label, children }: { label: ReactNode; children: ReactNode }) {
  const id = useId();
  const container = useRef<HTMLSpanElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  const popup = useRef<HTMLDivElement>(null);
  const closeTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [visible, setVisible] = useState(false);
  const cancelClose = () => { if (closeTimer.current !== null) clearTimeout(closeTimer.current); closeTimer.current = null; };
  const close = () => { cancelClose(); popup.current?.hidePopover(); setVisible(false); };
  const open = () => {
    cancelClose();
    const anchor = button.current;
    const detail = popup.current;
    if (!anchor || !detail) return;
    detail.showPopover();
    const rect = anchor.getBoundingClientRect();
    const bounds = detail.getBoundingClientRect();
    const left = Math.max(8, Math.min(rect.left, window.innerWidth - bounds.width - 8));
    const top = rect.bottom + bounds.height + 8 <= window.innerHeight
      ? rect.bottom + 4 : Math.max(8, rect.top - bounds.height - 4);
    detail.style.left = `${left}px`;
    detail.style.top = `${top}px`;
    setVisible(true);
  };
  useEffect(() => () => { cancelClose(); }, []);
  useEffect(() => {
    if (!visible) return;
    const dismiss = (event: KeyboardEvent) => { if (event.key === 'Escape') close(); };
    const resize = () => close();
    const scroll = (event: Event) => {
      if (!(event.target instanceof Node) || !popup.current?.contains(event.target)) close();
    };
    const outside = (event: PointerEvent) => {
      if (!container.current?.contains(event.target as Node)) close();
    };
    document.addEventListener('keydown', dismiss);
    document.addEventListener('pointerdown', outside);
    window.addEventListener('resize', resize);
    // Capture parent scroll, including the horizontally scrolling table.
    window.addEventListener('scroll', scroll, true);
    return () => {
      document.removeEventListener('keydown', dismiss);
      document.removeEventListener('pointerdown', outside);
      window.removeEventListener('resize', resize);
      window.removeEventListener('scroll', scroll, true);
    };
  }, [visible]);
  return <span ref={container} className={styles.metric}
    onMouseEnter={open}
    onMouseLeave={() => { if (!container.current?.contains(document.activeElement)) closeTimer.current = setTimeout(close, 150); }}
    onBlur={(event) => { if (!event.currentTarget.contains(event.relatedTarget)) close(); }}>
    <button ref={button} type="button" className={styles.metricButton} aria-describedby={id}
      onFocus={open} onClick={open}>{label}</button>
    <div ref={popup} id={id} role="tooltip" popover="manual" className={styles.metricDetail}>{children}</div>
  </span>;
}

const compact = (value: number) => new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 }).format(value);
const exact = (value: number) => value.toLocaleString();

export function AccountUsage({ usage, generatedAt }: { usage?: RecentAccountUsage; generatedAt?: string }) {
  if (!usage) return <span className={styles.missing}>Usage unavailable</span>;
  return <MetricDetail label={<><strong>{compact(usage.five_minutes.tokens)} tokens</strong> · {compact(usage.five_minutes.requests)} requests <span className={styles.metricPeriod}>/ 5m</span></>}>
    <strong>Recent proxy usage</strong>
    <dl className={styles.metricWindows}>
      {([['Last 5 minutes', usage.five_minutes], ['Last hour', usage.hour], ['Last 24 hours', usage.day]] as const).map(([label, window]) => <div key={label}>
        <dt>{label}</dt><dd>{compact(window.tokens)} tokens · {exact(window.requests)} requests</dd>
      </div>)}
    </dl>
    <p>Completed traffic recorded by this proxy.</p>
    {generatedAt && <time dateTime={generatedAt}>As of {new Date(generatedAt).toLocaleTimeString()}</time>}
  </MetricDetail>;
}

export function AccountAccess({ credentialID, users, groups }: { credentialID: string; users?: QuotaUser[]; groups?: AccountGroups }) {
  if (!users) return <span className={styles.missing}>Access unavailable</span>;
  const unique = new Map(users.filter((user) => user.credential_ids.includes(credentialID)).map((user) => [user.id, user]));
  const allowed = [...unique.values()].sort((a, b) => a.username.localeCompare(b.username));
  const scopeIDs = new Set(groups?.bindings.filter((binding) => binding.auth_id === credentialID).map((binding) => binding.channel_group_id));
  const scopes = [...new Map(groups?.groups.filter((group) => !group.disabled && scopeIDs.has(group.id)).map((group) => [group.id, group])).values()]
    .sort((a, b) => a.channel_name.localeCompare(b.channel_name));
  return <MetricDetail label={<><strong>{allowed.length}</strong> {allowed.length === 1 ? 'user' : 'users'}{groups && <>, <strong>{scopes.length}</strong> {scopes.length === 1 ? 'scope' : 'scopes'}</>} with access</>}>
    <strong>Users with configured access</strong>
    {allowed.length ? <ul className={styles.metricUsers}>{allowed.map((user) => <li key={user.id}>{user.username}</li>)}</ul> : <p>No assigned Home users have access.</p>}
    {groups && <><strong>Credential scopes with access</strong>
      {scopes.length ? <ul className={styles.metricUsers}>{scopes.map((scope) => <li key={scope.id}>{scope.channel_name}</li>)}</ul> : <p>No enabled credential scopes include this account.</p>}
    </>}
    <p>Configured access through assigned client keys and scopes.</p>
  </MetricDetail>;
}
