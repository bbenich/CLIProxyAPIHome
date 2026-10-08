export interface PickerOption { value: string; label: string }
export const PICKER_LIMIT = 100;
export function pickerPlacement(anchor: { left: number; right: number; top: number; bottom: number }, viewport: { width: number; height: number }, desiredHeight = 360) {
  const margin = 12, gap = 6;
  const width = Math.min(320, Math.max(0, viewport.width - margin * 2));
  const below = Math.max(0, viewport.height - anchor.bottom - gap - margin);
  const above = Math.max(0, anchor.top - gap - margin);
  const useAbove = below < Math.min(desiredHeight, 180) && above > below;
  const maxHeight = Math.min(desiredHeight, useAbove ? above : below);
  return {
    left: Math.max(margin, Math.min(anchor.right - width, viewport.width - width - margin)),
    top: Math.max(margin, useAbove ? anchor.top - gap - maxHeight : anchor.bottom + gap),
    width, maxHeight,
  };
}
const normalize = (text: string) => text.normalize('NFD').replace(/[\u0300-\u036f]/g, '').toLocaleLowerCase();
export function pickerResults(options: PickerOption[], query: string) {
  const terms = normalize(query).trim().split(/\s+/).filter(Boolean);
  const matches = options.filter((option) => terms.every((term) => normalize(option.label).includes(term)));
  return { items: matches.slice(0, PICKER_LIMIT), total: matches.length };
}
export function pickerIndex(key: string, index: number, length: number) {
  if (!length) return -1;
  if (key === 'Home') return 0;
  if (key === 'End') return length - 1;
  if (key === 'ArrowDown') return Math.min(index + 1, length - 1);
  if (key === 'ArrowUp') return Math.max(index - 1, 0);
  return index;
}
