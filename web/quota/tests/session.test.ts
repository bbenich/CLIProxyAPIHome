import { expect, test } from 'bun:test';
import { readHomeSession } from '../src/session';
const storage = (value: unknown) => ({ getItem: () => value == null ? null : JSON.stringify(value) });
const origin = 'http://localhost:8327';
test('reads temporary Home sessions and normalizes management endpoints', () => {
  expect(readHomeSession(storage(null), storage({baseUrl: origin + '/v8/management', managementKey: 'fixture'}), origin)).toEqual({baseUrl: origin, managementKey: 'fixture'});
});
test('does not forward keys for foreign servers or malformed sessions', () => {
  for (const baseUrl of ['https://other.example', origin + '/unrelated', origin + '?key=example']) {
    expect(readHomeSession(storage({baseUrl, managementKey:'fixture'}), storage(null), origin)).toBeNull();
  }
  expect(readHomeSession(storage({baseUrl:origin,managementKey:5}), storage(null), origin)).toBeNull();
});
test('absent or inaccessible storage means disconnected', () => {
  expect(readHomeSession(storage(null), storage(null), origin)).toBeNull();
  expect(readHomeSession({getItem:()=>{throw new Error('denied')}}, storage(null), origin)).toBeNull();
});

test('accepts the pinned Home panel relative v0 storage format while targeting v8 on this origin', () => {
  const fixture = {baseUrl:'/v0/management',managementKey:'fixture',rememberConnection:true};
  expect(readHomeSession(storage(fixture), storage(null), origin)).toEqual({baseUrl:origin,managementKey:'fixture'});
  expect(readHomeSession(storage(null), storage({...fixture,rememberConnection:false}), origin)).toEqual({baseUrl:origin,managementKey:'fixture'});
});
