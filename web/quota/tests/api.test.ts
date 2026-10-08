import { expect, test } from 'bun:test';
import { createQuotaApi } from '../src/api';

test('loads names and full quota windows across pages without collecting upstream', async () => {
  const calls: string[] = [];
  const request = (async (url: string, options: RequestInit) => {
    const path = new URL(url).pathname + new URL(url).search;
    calls.push(path);
    expect(options.method).toBe('GET');
    expect(options.headers).toEqual({Authorization:'Bearer fixture', 'Content-Type':'application/json'});
    expect(options.redirect).toBe('error');
    if (path.endsWith('/credentials')) return Response.json({files:[{id:'a',email:'account@example.test'}]});
    if (path.includes('?')) return Response.json({items:[{credential_id: path.includes('offset=0') ? 'a' : 'b', label:'Fallback'}], total:2});
    return Response.json({windows:[{id:'five',window_seconds:18000,remaining_ratio:0.99}]});
  }) as typeof fetch;
  const api = createQuotaApi({baseUrl:'http://localhost:8327',managementKey:'fixture'},request);
  const accounts = await api.list();
  expect(accounts).toHaveLength(2);
  expect(accounts[0].displayName).toBe('account@example.test');
  expect(accounts[1].displayName).toBe('Fallback');
  expect(accounts[0].windows[0].window_seconds).toBe(18000);
  expect(calls).toHaveLength(5);
});

test('authentication failure provides a Home reconnect instruction without exposing response data', async () => {
  const api = createQuotaApi({baseUrl:'http://localhost:8327',managementKey:'fixture'}, (async () => new Response('private upstream error',{status:401})) as typeof fetch);
  await expect(api.list()).rejects.toThrow('Home connection expired. Reconnect using Connection in Home Center.');
});

test('recent usage uses the authenticated batched summary with a server timestamp', async () => {
  const fixture = { generated_at: '2026-10-08T20:00:00Z', accounts: [{ credential_id: 'a', five_minutes: {tokens: 42, requests: 1}, hour: {tokens: 42, requests: 1}, day: {tokens: 42, requests: 1} }] };
  const api = createQuotaApi({baseUrl:'http://localhost:8327',managementKey:'fixture'}, (async (url, options) => {
    expect(String(url)).toBe('http://localhost:8327/v8/management/quota/recent-usage');
    expect(options?.method).toBe('GET');
    expect(options?.headers).toEqual({Authorization:'Bearer fixture', 'Content-Type':'application/json'});
    return Response.json(fixture);
  }) as typeof fetch);
  expect(await api.recentUsage()).toEqual(fixture);
});

test('multiple dashboard clients only read cached endpoints and expose no probe trigger', async () => {
 const calls: string[]=[];
 const request=(async (url,options) => {
  expect(options?.method).toBe('GET');
  const path=new URL(String(url)).pathname;
  calls.push(path);
  expect(path).not.toContain('/collect');
  if(path.endsWith('/credentials')) return Response.json(path.includes('/quota/')?{items:[],total:0}:{files:[]});
  if(path.endsWith('/channel-groups'))return Response.json({channel_groups:[]});
  if(path.endsWith('/channel-group-details'))return Response.json({channel_group_details:[]});
  if(path.endsWith('/users'))return Response.json({users:[]});
  return Response.json({});
 }) as typeof fetch;
 const clients=Array.from({length:3},()=>createQuotaApi({baseUrl:'http://localhost:8327',managementKey:'fixture'},request));
 for(const client of clients) {
  expect('collect' in client).toBe(false);
  await Promise.all([client.list(),client.routing(),client.users(),client.groups(),client.recentUsage()]);
 }
 expect(calls.length).toBe(21);
});
