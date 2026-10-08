export interface HomeSession { baseUrl: string; managementKey: string }
export const permanentSessionKey = 'home-management-center.managementSession';
export const temporarySessionKey = 'home-management-center.temporaryManagementSession';

// Read the pinned Home panel's connection format; never copy it into Console storage.
// A saved connection to a different server must not send its key to this instance.
export function readHomeSession(local: Pick<Storage, 'getItem'>, temporary: Pick<Storage, 'getItem'>, origin: string): HomeSession | null {
  for (const [storage, key] of [[local, permanentSessionKey], [temporary, temporarySessionKey]] as const) {
    try {
      const raw = storage.getItem(key);
      if (!raw) continue;
      const value: unknown = JSON.parse(raw);
      if (!value || typeof value !== 'object') return null;
      const session = value as Partial<HomeSession>;
      if (typeof session.baseUrl !== 'string' || typeof session.managementKey !== 'string' || !session.managementKey.trim()) return null;
      const url = new URL(session.baseUrl, origin);
      if (url.origin !== origin || url.username || url.password || url.search || url.hash || !['/', '/v0/management', '/v0/management/', '/v8/management', '/v8/management/'].includes(url.pathname)) return null;
      return { baseUrl: origin, managementKey: session.managementKey };
    } catch { return null; }
  }
  return null;
}
