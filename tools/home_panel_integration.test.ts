import { expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { Script, runInNewContext } from 'node:vm';

const root = 'internal/managementasset/static/';
const html = readFileSync(root + 'management.html', 'utf8');
const entry = html.match(/src="\/(assets\/js\/management\.home-[a-f0-9]+\.js)"/)![1];
const bundle = readFileSync(root + entry, 'utf8');

test('extended pinned bundle is valid JavaScript and keeps the native sidebar navigation', () => {
  new Script(bundle);
  const icons = Object.fromEntries(['Q','J','X','Y','ee','et','ea','en','er','ei','es'].map(name => [name,{A:name}]));
  const expression = bundle.slice(bundle.indexOf('let eo=')+7,bundle.indexOf(',ed=eo.flatMap'));
  const groups = runInNewContext(expression, icons);
  const observe = groups.find((group: {id:string}) => group.id === 'observe');
  expect(observe.items.map((item: {to:string})=>item.to)).toEqual(['/admin/quota','/admin/usage','/admin/diagnostics','/admin/logs']);
  const all = groups.flatMap((group: {items:unknown[]})=>group.items);
  expect(all.filter((item: {to:string})=>item.to === '/admin/quota')).toHaveLength(1);
  expect(all.filter((item: {to:string})=>item.to === '/admin/api-console')).toHaveLength(1);
  expect(all.some((item: {to:string})=>item.to === '/admin/users')).toBe(true);
  expect(html).not.toContain('<iframe');
  expect(html).not.toContain('Interface navigation');
});

test('native quota route renders only content and reloads when Home connection changes', () => {
  const component = bundle.slice(bundle.indexOf('function homeQuotaPage(){'),bundle.indexOf('var eU=a(45234);'));
  const state: {connectedAt:string|null,managementKey:string} = {connectedAt:'first',managementKey:'fixture'};
  const context = {I:{B:(selector: (value: typeof state)=>unknown)=>selector(state)},n:{jsx:(tag:string,props:unknown)=>({tag,props})}};
  const render = () => runInNewContext(component+';homeQuotaPage()',context);
  expect(render()).toEqual({tag:'iframe',props:{key:'first:true',src:'/quota-panel.html',title:'Subscription quotas',style:{border:0,width:'100%',height:'100%',display:'block'}}});
  state.connectedAt = null; state.managementKey = '';
  expect(render().props.key).toBe('null:false');
  expect(bundle).toContain('path:"/admin/quota",component:homeQuotaPage');
});

test('API Console is a registered native route that leaves Home without nesting apps', () => {
  const route = bundle.slice(bundle.indexOf('homeConsoleRoute=(0,N.un)'),bundle.indexOf(',tu=e5.addChildren'));
  let destination = '';
  const context = {N:{un:(value:unknown)=>value},e5:{},r:{useEffect:(callback:()=>void)=>callback()},window:{location:{replace:(value:string)=>{destination=value}}}};
  const definition = runInNewContext('var '+route+';homeConsoleRoute',context);
  expect(definition.path).toBe('/admin/api-console');
  expect(definition.component()).toBeNull();
  expect(destination).toBe('/console.html#/');
  expect(bundle).toContain('tu=e5.addChildren([homeConsoleRoute,');
});

test('every shared Home strategy parser recognizes all selectable strategies', () => {
  for (const id of [1118,416,8063]) {
    const hash = bundle.match(new RegExp(`${id}:"([a-f0-9]+)"`))![1];
    const chunk = readFileSync(root + `assets/js/${id}.${hash}.js`,'utf8');
    new Script(chunk);
    const start = chunk.indexOf('function ', chunk.indexOf('26737('));
    const end = chunk.indexOf('default:return"unknown"}',start) + 'default:return"unknown"}}'.length;
    const parser = runInNewContext('('+chunk.slice(start,end)+')');
    for (const strategy of ['round-robin','fill-first','weighted-round-robin','quota-reset']) expect(parser({routing:{strategy}})).toBe(strategy);
    expect(parser({routing:{strategy:'rr'}})).toBe('round-robin');
    if (id===8063) {
      expect(chunk).toContain('options:["round-robin","fill-first","weighted-round-robin","quota-reset"]');
      expect(chunk).toContain('"Quota reset priority"');
      expect(chunk).toContain('case"routingStrategy":await t.put("/routing/strategy",{value:n})');
    }
  }
});
