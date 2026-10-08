import { expect, test } from 'bun:test';
import { renderToStaticMarkup } from 'react-dom/server';
import { AccountAccess, AccountUsage } from '../src/AccountMetrics';
import { QuotaAccounts } from '../src/QuotaAccounts';
import type { QuotaAccount, QuotaUser, RecentUsage } from '../src/api';

const users: QuotaUser[] = [
  { id: 1, username: 'Work user', key_count: 3, credential_ids: ['a', 'a'] },
  { id: 2, username: 'Personal user', key_count: 2, credential_ids: ['a', 'b'] },
  { id: 3, username: 'Other user', key_count: 1, credential_ids: ['b'] },
];
const usage: RecentUsage = { generated_at: '2026-10-08T20:00:00Z', accounts: [
  { credential_id: 'a', five_minutes: { tokens: 12345, requests: 3 }, hour: { tokens: 90000, requests: 21 }, day: { tokens: 500000, requests: 111 } },
] };
const account: QuotaAccount = { credential_id: 'a', provider: 'claude', label: 'a', displayName: 'Account A', credential_status: 'enabled', freshness: 'fresh', observed_at: null, collection_status: 'idle', error: null, windows: [] };

test('cards and table show compact five-minute tokens, request counts and global access', () => {
  for (const view of ['cards', 'table'] as const) {
    const html = renderToStaticMarkup(<QuotaAccounts accounts={[account]} view={view} usage={usage} users={users} />);
    for (const value of ['12.3K tokens', '3 requests', '/ 5m', '12.3K tokens', '90K tokens', '500K tokens', 'Last hour', 'Last 24 hours', '2</strong> users with access', 'Work user', 'Personal user']) expect(html).toContain(value);
    expect(html).not.toContain('Other user');
    expect(html.match(/role="tooltip"/g)).toHaveLength(2);
    expect(html.match(/aria-describedby=/g)).toHaveLength(2);
    expect(html.match(/popover="manual"/g)).toHaveLength(2);
    expect(html).toContain('type="button"');
    expect(html).toContain('2026-10-08T20:00:00Z');
  }
});

test('zero traffic is distinct from unavailable usage, and one user is counted once', () => {
  const zero = { ...usage.accounts[0], five_minutes: { tokens: 0, requests: 0 }, hour: { tokens: 0, requests: 0 }, day: { tokens: 0, requests: 0 } };
  const html = renderToStaticMarkup(<AccountUsage usage={zero} />);
  expect(html).toContain('0 tokens');
  expect(html).toContain('0 requests');
  expect(html).not.toContain('unavailable');
  expect(renderToStaticMarkup(<AccountUsage />)).toContain('Usage unavailable');
  expect(renderToStaticMarkup(<AccountAccess credentialID="a" users={[users[0], users[0]]} />)).toContain('1</strong> user with access');
  expect(renderToStaticMarkup(<AccountAccess credentialID="c" users={users} />)).toContain('No assigned Home users');
  expect(renderToStaticMarkup(<AccountAccess credentialID="a" />)).toContain('Access unavailable');
});

test('user names are escaped and unknown credentials do not inherit another account usage', () => {
  const html = renderToStaticMarkup(<QuotaAccounts accounts={[{ ...account, credential_id: 'missing' }]} view="cards" usage={usage} users={[{ ...users[0], username: '<img onerror=bad>', credential_ids: ['missing'] }]} />);
  expect(html).toContain('Usage unavailable');
  expect(html).not.toContain('12.3K');
  expect(html).toContain('&lt;img onerror=bad&gt;');
  expect(html).not.toContain('<img');
});


test('usage details abbreviate thousands, millions and billions of tokens and keep the explanation short', () => {
 const usage = { credential_id: 'a', five_minutes: {tokens: 28082508, requests:85}, hour:{tokens:192512252, requests:780}, day:{tokens:2066090664,requests:6979} };
 const html=renderToStaticMarkup(<AccountUsage usage={usage} />);
 for(const text of ['28.1M tokens','192.5M tokens','2.1B tokens','6,979 requests','Completed traffic recorded by this proxy.']) expect(html).toContain(text);
 expect(html).not.toContain('28,082,508');
 expect(html).not.toContain('subscription quota percentages');
});

test('cards and table count distinct enabled credential scopes globally and list their names', () => {
 const groups={groups:[
  {id:1,channel_name:'Personal',disabled:false},{id:2,channel_name:'Work',disabled:false},{id:3,channel_name:'Shared',disabled:false},
  {id:4,channel_name:'Disabled scope',disabled:true},{id:5,channel_name:'Another account',disabled:false},
 ],bindings:[
  {channel_group_id:1,auth_id:'a'},{channel_group_id:1,auth_id:'a'},{channel_group_id:2,auth_id:'a'},{channel_group_id:3,auth_id:'a'},
  {channel_group_id:4,auth_id:'a'},{channel_group_id:999,auth_id:'a'},{channel_group_id:5,auth_id:'b'},
 ]};
 for(const view of ['cards','table'] as const) {
  const html=renderToStaticMarkup(<QuotaAccounts accounts={[account]} view={view} users={[users[0],users[0]]} groups={groups} />);
  expect(html).toContain('1</strong> user, <strong>3</strong> scopes with access');
  for(const name of ['Personal','Work','Shared'])expect(html).toContain(name);
  expect(html).not.toContain('Disabled scope');expect(html).not.toContain('Another account');
  expect(html).toContain('Configured access through assigned client keys and scopes.');
  expect(html).not.toContain('Temporary limits and cooldowns');
 }
 const empty=renderToStaticMarkup(<AccountAccess credentialID="missing" users={users} groups={groups} />);
 expect(empty).toContain('0</strong> users, <strong>0</strong> scopes with access');
 const single=renderToStaticMarkup(<AccountAccess credentialID="b" users={users} groups={groups} />);
 expect(single).toContain('1</strong> scope with access');
});
