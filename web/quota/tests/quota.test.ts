import { describe, expect, test } from 'bun:test';
import { linearPacePercent, remainingPercent } from '../src/logic';
import type { QuotaWindow } from '../src/api';
const window = (remaining: number | null, used: number | null): QuotaWindow => ({
  id: 'five',
  label: null,
  scope: 'account',
  window_seconds: 18000,
  remaining_ratio: remaining,
  used_ratio: used,
  reset_at: null,
});
describe('quota dashboard capacity', () => {
  test('unknown remains unknown', () => expect(remainingPercent(window(null, null))).toBeNull());
  test('converts utilization to remaining quota', () =>
    expect(remainingPercent(window(null, 0.89))).toBeCloseTo(11));
  test('prefers explicit remaining', () => expect(remainingPercent(window(0.99, 0.9))).toBe(99));
  test('rejects non-finite quota', () => expect(remainingPercent(window(NaN, null))).toBeNull());
});
describe('linear pace reference', () => {
  const now = Date.parse('2026-10-08T12:00:00Z');
  const resetIn = (hours: number | null, seconds: number | null = 18000): QuotaWindow => ({
    ...window(0.3, null),
    window_seconds: seconds,
    reset_at: hours == null ? null : new Date(now + hours * 3600_000).toISOString(),
  });
  test('halfway through a five-hour window is 50%', () => expect(linearPacePercent(resetIn(2.5), now)).toBe(50));
  test('tracks elapsed time, not usage', () => expect(linearPacePercent(resetIn(4), now)).toBeCloseTo(80));
  test('weekly windows use their own length', () => expect(linearPacePercent(resetIn(42, 604800), now)).toBe(25));
  test('unstarted, reset or unsized windows have no pace', () => {
    expect(linearPacePercent(resetIn(null), now)).toBeNull();
    expect(linearPacePercent(resetIn(0), now)).toBeNull();
    expect(linearPacePercent(resetIn(-1), now)).toBeNull();
    expect(linearPacePercent(resetIn(2, null), now)).toBeNull();
  });
  test('clock skew past the window length clamps to 100%', () => expect(linearPacePercent(resetIn(6), now)).toBe(100));
});

import { startQuotaPolling } from '../src/polling';

test('polling pauses while hidden, avoids overlaps, resumes and cleans up', async () => {
  let visible = true;
  let calls = 0;
  let tick = () => {};
  let visibility = () => {};
  let release = () => {};
  let timerStopped = false;
  let listenerStopped = false;
  const stop = startQuotaPolling(
    () => {
      calls += 1;
      return new Promise<void>((resolve) => {
        release = resolve;
      });
    },
    {
      visible: () => visible,
      interval: (callback, milliseconds) => {
        expect(milliseconds).toBe(60000);
        tick = callback;
        return () => {
          timerStopped = true;
        };
      },
      visibilityChanges: (callback) => {
        visibility = callback;
        return () => {
          listenerStopped = true;
        };
      },
    }
  );
  expect(calls).toBe(1);
  tick();
  visibility();
  expect(calls).toBe(1);
  release();
  await Promise.resolve();
  visible = false;
  tick();
  expect(calls).toBe(1);
  visible = true;
  visibility();
  expect(calls).toBe(2);
  stop();
  release();
  await Promise.resolve();
  tick();
  visibility();
  expect(calls).toBe(2);
  expect(timerStopped).toBe(true);
  expect(listenerStopped).toBe(true);
});
