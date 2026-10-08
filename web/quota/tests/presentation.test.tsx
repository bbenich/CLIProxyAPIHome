import { expect, test } from 'bun:test';
import { renderToStaticMarkup } from 'react-dom/server';
import { QuotaAccounts } from '../src/QuotaAccounts';
import type { QuotaAccount } from '../src/api';

const account: QuotaAccount = {
 credential_id:'fixture',provider:'claude',label:'fallback',displayName:'name@example.test',credential_status:'disabled',freshness:'stale',observed_at:'2026-10-08T12:00:00Z',collection_status:'idle',error:{message:'Provider unavailable'},
 windows:[
 {id:'five',scope:'account',label:null,window_seconds:18000,remaining_ratio:.99,used_ratio:null,reset_at:null},
 {id:'weekly',scope:'account',label:null,window_seconds:604800,remaining_ratio:null,used_ratio:null,reset_at:null},
 {id:'model-weekly',scope:'model',label:'Sonnet weekly',window_seconds:604800,remaining_ratio:.42,used_ratio:null,reset_at:null},
 ]
};

test('both layouts retain identity, quotas, model windows, freshness and errors', () => {
 for (const view of ['cards','table'] as const) {
  const html=renderToStaticMarkup(<QuotaAccounts accounts={[account]} view={view} />);
  for (const text of ['name@example.test','Claude','Disabled','99% remaining','Unknown','Sonnet weekly','42% remaining','Stale / not yet collected','Provider unavailable','2026-10-08T12:00:00Z']) expect(html).toContain(text);
  expect(html.includes('<table')).toBe(view==='table');
 }
});

test('table represents missing observations without claiming quota is full', () => {
 const html=renderToStaticMarkup(<QuotaAccounts accounts={[{...account,windows:[],error:null}]} view="table" />);
 expect(html).toContain('No quota observation available');
 expect(html).not.toContain('100%');
 expect(html).toContain('scope="row"');
 expect(html).toContain('scope="col"');
});

test('quota bars mark the even-pace reference only for running windows', () => {
 const now=Date.parse('2026-10-08T12:00:00Z');
 const running={...account,windows:[{...account.windows[0],remaining_ratio:.2,reset_at:'2026-10-08T13:00:00Z'},account.windows[2]]};
 for (const view of ['cards','table'] as const) {
  const html=renderToStaticMarkup(<QuotaAccounts accounts={[running]} view={view} now={now} />);
  expect(html.match(/left:20%/g)).toHaveLength(1);
  expect(html).toContain('title="Even pace: 20% remaining"');
  expect(html).toContain('aria-hidden="true"');
 }
 expect(renderToStaticMarkup(<QuotaAccounts accounts={[account]} view="cards" now={now} />)).not.toContain('Even pace');
});
