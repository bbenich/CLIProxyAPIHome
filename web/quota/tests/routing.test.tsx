import { expect, test } from 'bun:test';
import { renderToStaticMarkup } from 'react-dom/server';
import { resolveGroupSelection, routingAccounts, routingValue } from '../src/routing';
import { QuotaAccounts } from '../src/QuotaAccounts';
import type { QuotaAccount, RoutingObservation } from '../src/api';
const accounts = ['a','b','c'].map((id)=>({credential_id:id,displayName:id,provider:'claude',credential_status:'enabled',freshness:'fresh',windows:[]} as unknown as QuotaAccount));
const routing: RoutingObservation = {strategy:'quota-reset',strategy_name:'Quota reset priority',order_kind:'rank',session_affinity:false,generated_at:'2026-10-08T12:00:00Z',accounts:[
 {credential_id:'a',rank:3,priority:0,weight:1,reason:'quota-unknown',model_dependent:false},
 {credential_id:'b',rank:1,priority:0,weight:1,reason:'untouched-five-hour',model_dependent:false},
 {credential_id:'c',rank:2,priority:0,weight:1,reason:'weekly-reset',weekly_reset_at:'2026-10-08T15:00:00Z',model_dependent:false},
]};
test('group filtering preserves global ranks and ordering in both layouts',()=>{
 const filtered=routingAccounts(accounts,routing,{groups:[{id:1,channel_name:'work',disabled:false}],bindings:[{channel_group_id:1,auth_id:'a'},{channel_group_id:1,auth_id:'c'}]},'1');
 expect(filtered.map((a)=>a.credential_id)).toEqual(['c','a']);
 expect(routingValue('c',routing)).toBe('#2 · Weekly reset in 3h');
 for (const view of ['cards','table'] as const) {
  const html=renderToStaticMarkup(<QuotaAccounts accounts={filtered} view={view} routing={routing}/>);
  expect(html).toContain('#2 · Weekly reset in 3h');expect(html).toContain('#3 · Unknown/stale quota');expect(html).not.toContain('#1');
 }
});
test('strategy changes update values without inventing a rotating queue',()=>{
 const roundRobin: RoutingObservation={...routing,strategy:'weighted-round-robin',order_kind:'tier',accounts:[{...routing.accounts[0],rank:1,reason:'weighted-rotation',priority:4,weight:7}]};
 expect(routingValue('a',roundRobin)).toBe('Tier #1 · Priority 4 · Weight 7');
 expect(routingValue('a',null)).toBe('Priority unavailable');
 expect(routingAccounts(accounts,null,{groups:[],bindings:[]},'missing')).toEqual([]);
});

test('healthy stale reset estimates retain their global priority in cards and table', () => {
 const estimated: RoutingObservation = {...routing, accounts: routing.accounts.map((item) => item.credential_id === 'c' ? {...item, estimated: true} : item)};
 expect(routingValue('c', estimated)).toBe('#2 · Weekly reset in 3h · Estimated');
 for (const view of ['cards', 'table'] as const) {
  const html = renderToStaticMarkup(<QuotaAccounts accounts={accounts} view={view} routing={estimated} />);
  expect(html).toContain('#2 · Weekly reset in 3h · Estimated');
 }
});

test('a deleted group falls back to all accounts while valid current selections survive refresh', () => {
 const refreshedGroups = {
  groups: [{id:2,channel_name:'personal',disabled:false}],
  bindings: [{channel_group_id:2,auth_id:'a'}],
 };
 const deletedSelection = resolveGroupSelection('1', refreshedGroups);
 expect(deletedSelection).toBe('all');
 expect(routingAccounts(accounts,routing,refreshedGroups,deletedSelection).map((account)=>account.credential_id)).toEqual(['b','c','a']);
 expect(resolveGroupSelection('all',refreshedGroups)).toBe('all');
 // Resolve against the latest selection, including a choice made while polling was in flight.
 const currentSelection = resolveGroupSelection('2',refreshedGroups);
 expect(currentSelection).toBe('2');
 expect(routingAccounts(accounts,routing,refreshedGroups,currentSelection).map((account)=>account.credential_id)).toEqual(['a']);
 expect(routingValue('a',routing)).toBe('#3 · Unknown/stale quota · fallback');
 expect(resolveGroupSelection('2',{groups:[],bindings:[]})).toBe('all');
});

import { filterUserAccounts, resolveUserSelection } from '../src/routing';
test('user scopes intersect group filtering and preserve global ranks', () => {
 const users=[{id:1,username:'fixture',key_count:2,credential_ids:['b','c']},{id:2,username:'no-keys',key_count:0,credential_ids:[]}];
 const groups={groups:[{id:1,channel_name:'work',disabled:false}],bindings:[{channel_group_id:1,auth_id:'a'},{channel_group_id:1,auth_id:'c'}]};
 const grouped=routingAccounts(accounts,routing,groups,'1');
 expect(filterUserAccounts(grouped,users,'1').map((account)=>account.credential_id)).toEqual(['c']);
 expect(routingValue('c',routing)).toBe('#2 · Weekly reset in 3h');
 expect(filterUserAccounts(accounts,users,'2')).toEqual([]);
 expect(filterUserAccounts(accounts,users,'missing')).toEqual([]);
 expect(filterUserAccounts(accounts,users,'all')).toEqual(accounts);
 expect(resolveUserSelection('missing',users)).toBe('all');
 expect(resolveUserSelection('1',users)).toBe('1');
});
