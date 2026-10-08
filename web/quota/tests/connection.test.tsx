import { expect, test } from 'bun:test';
import { renderToStaticMarkup } from 'react-dom/server';
import { ConnectionStatus, connectionAccounts } from '../src/ConnectionStatus';
import type { QuotaAccount } from '../src/api';

test('lost dashboard connection replaces live status and marks all cached accounts stale until recovery', () => {
 const accounts = [{credential_id:'a',freshness:'fresh'},{credential_id:'b',freshness:'fresh'}] as QuotaAccount[];
 const checked = new Date('2026-10-08T20:00:00Z');
 const disconnected = renderToStaticMarkup(<ConnectionStatus error="Network unavailable" checked={checked} />);
 expect(disconnected).toContain('role="alert"');
 expect(disconnected).toContain('All displayed information is stale');
 expect(disconnected).not.toContain('Auto-refreshing');
 expect(connectionAccounts(accounts,'lost').map(item=>item.freshness)).toEqual(['stale','stale']);
 expect(accounts.map(item=>item.freshness)).toEqual(['fresh','fresh']);
 expect(connectionAccounts(accounts,'')).toBe(accounts);
 const recovered=renderToStaticMarkup(<ConnectionStatus error="" checked={checked} />);
 expect(recovered).toContain('Auto-refreshing');
 expect(recovered).not.toContain('role="alert"');
 expect(recovered).not.toContain('seconds');
});
