import { expect, test } from 'bun:test';
import { readPreferences, savePreferences, preferencesKey } from '../src/preferences';

test('view and size survive a fresh read for all six combinations', () => {
  let stored: string | null = null;
  const storage = {getItem:(key:string)=>{expect(key).toBe(preferencesKey);return stored},setItem:(key:string,value:string)=>{expect(key).toBe(preferencesKey);stored=value}};
  for (const view of ['cards','table'] as const) for (const size of ['large','medium','small'] as const) {
    savePreferences({view,size},storage);
    expect(readPreferences(storage)).toEqual({view,size});
  }
});

test('malformed, old and unavailable storage fall back safely', () => {
  for (const value of ['bad-json','null','[]','{"view":"grid","size":"huge"}']) {
    expect(readPreferences({getItem:()=>value})).toEqual({view:'cards',size:'medium'});
  }
  expect(readPreferences({getItem:()=>'{"view":"table"}'})).toEqual({view:'table',size:'medium'});
  expect(readPreferences({getItem:()=>{throw new Error('denied')}})).toEqual({view:'cards',size:'medium'});
  expect(()=>savePreferences({view:'table',size:'small'},{setItem:()=>{throw new Error('full')}})).not.toThrow();
});
