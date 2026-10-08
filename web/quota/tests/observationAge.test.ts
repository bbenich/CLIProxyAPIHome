import { expect, test } from 'bun:test';
import type { QuotaAccount, RoutingObservation } from '../src/api';
import { observationPresentation, OBSERVATION_WARNING_AGE } from '../src/observationAge';
import { connectionAccounts } from '../src/ConnectionStatus';

test('stale and Estimated warnings share a ten-minute age boundary without changing server ranks', () => {
 const now=Date.parse('2026-10-08T21:17:00Z');
 const account={credential_id:'a',observed_at:new Date(now-2*60000).toISOString(),freshness:'stale',quota_status:'healthy',windows:[{id:'five'}]} as QuotaAccount;
 const routing={strategy:'quota-reset',accounts:[{credential_id:'a',rank:3,reason:'weekly-reset',estimated:true}]} as RoutingObservation;
 for(const age of [2*60000,OBSERVATION_WARNING_AGE-1,OBSERVATION_WARNING_AGE,OBSERVATION_WARNING_AGE+1]) {
  const result=observationPresentation([{...account,observed_at:new Date(now-age).toISOString()}],routing,now);
  const stale=age>=OBSERVATION_WARNING_AGE;
  expect(result.accounts[0].freshness).toBe(stale?'stale':'fresh');
  expect(result.routing?.accounts[0].estimated).toBe(stale);
  expect(result.routing?.accounts[0].rank).toBe(3);
 }
 expect(account.freshness).toBe('stale');
 expect(routing.accounts[0].estimated).toBe(true);
 const fresh=observationPresentation([account],routing,now).accounts;
 expect(connectionAccounts(fresh,'Connection lost')[0].freshness).toBe('stale');
});

test('exact static and rotating priorities never become estimated due to quota age', () => {
 const now=Date.parse('2026-10-08T21:17:00Z');
 const account={credential_id:'a',observed_at:new Date(now-20*60000).toISOString(),quota_status:'healthy',windows:[{id:'five'}]} as QuotaAccount;
 for(const [strategy,reason] of [['fill-first','static-priority'],['round-robin','rotation'],['weighted-round-robin','weighted-rotation'],['quota-reset','disabled']]) {
  const routing={strategy,accounts:[{credential_id:'a',rank:3,reason,estimated:false}]} as RoutingObservation;
  const result=observationPresentation([account],routing,now);
  expect(result.accounts[0].freshness).toBe('stale');
  expect(result.routing?.accounts[0].estimated).toBe(false);
 }
});

test('missing, invalid, future and unusable observations cannot look current', () => {
 const now=Date.parse('2026-10-08T21:17:00Z');
 const base={credential_id:'a',freshness:'fresh',quota_status:'healthy',windows:[{id:'five'}]} as QuotaAccount;
 for(const observed_at of [null,'invalid',new Date(now+1).toISOString()]) {
  expect(observationPresentation([{...base,observed_at}],null,now).accounts[0].freshness).toBe('stale');
 }
 for(const patch of [{windows:[]},{quota_status:'unknown'},{quota_status:'error'}]) {
  expect(observationPresentation([{...base,observed_at:new Date(now).toISOString(),...patch}],null,now).accounts[0].freshness).toBe('stale');
 }
});
