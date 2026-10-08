export type QuotaView = 'cards' | 'table';
export type QuotaSize = 'large' | 'medium' | 'small';
export interface QuotaPreferences { view: QuotaView; size: QuotaSize }
export const preferencesKey = 'home-management-center.quota-preferences';
export const defaultPreferences: QuotaPreferences = { view: 'cards', size: 'medium' };

export function readPreferences(storage?: Pick<Storage, 'getItem'>): QuotaPreferences {
  try {
    const value = JSON.parse((storage ?? localStorage).getItem(preferencesKey) ?? 'null');
    return {
      view: value?.view === 'table' ? 'table' : 'cards',
      size: ['large', 'medium', 'small'].includes(value?.size) ? value.size : 'medium',
    };
  } catch { return { ...defaultPreferences }; }
}

export function savePreferences(value: QuotaPreferences, storage?: Pick<Storage, 'setItem'>) {
  try { (storage ?? localStorage).setItem(preferencesKey, JSON.stringify(value)); }
  catch { /* Continue supporting this tab when browser storage is unavailable. */ }
}
