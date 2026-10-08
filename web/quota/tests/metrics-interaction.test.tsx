import { afterAll, afterEach, beforeAll, expect, test, jest } from 'bun:test';
import { Window } from 'happy-dom';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { AccountAccess } from '../src/AccountMetrics';

// Exercise React's real handlers with DOM events. Native popover visibility is
// simulated here; placement/top-layer rendering still needs a browser check.
const dom = new Window();
const names = ['window', 'document', 'Node', 'HTMLElement', 'Event', 'MouseEvent', 'KeyboardEvent', 'FocusEvent', 'PointerEvent', 'IS_REACT_ACT_ENVIRONMENT'] as const;
const previous = new Map(names.map((name) => [name, Object.getOwnPropertyDescriptor(globalThis, name)]));
let root: Root;
let host: HTMLDivElement;

beforeAll(() => {
  for (const name of names) Object.defineProperty(globalThis, name, { configurable: true, writable: true, value: name === 'IS_REACT_ACT_ENVIRONMENT' ? true : dom[name as keyof Window] });
  Object.defineProperties(dom.HTMLElement.prototype, {
    showPopover: { configurable: true, value(this: HTMLElement) { this.setAttribute('data-visible', 'true'); } },
    hidePopover: { configurable: true, value(this: HTMLElement) { this.removeAttribute('data-visible'); } },
  });
});
afterEach(async () => {
  await act(() => root?.unmount());
  host?.remove();
  jest.useRealTimers();
});
afterAll(() => {
  for (const name of names) {
    const descriptor = previous.get(name);
    if (descriptor) Object.defineProperty(globalThis, name, descriptor);
    else Reflect.deleteProperty(globalThis, name);
  }
  dom.happyDOM.abort();
});

async function mount() {
  host = document.createElement('div');
  document.body.appendChild(host);
  root = createRoot(host);
  await act(() => root.render(<AccountAccess credentialID="a" users={[{id: 1, username: 'Fixture', key_count: 2, credential_ids: ['a']}]} />));
  return { button: host.querySelector('button')!, popup: host.querySelector('[popover]')! };
}
async function event(target: EventTarget, event: Event) { await act(() => { target.dispatchEvent(event); }); }
const mouse = (type: string, relatedTarget?: EventTarget) => new MouseEvent(type, {bubbles: true, relatedTarget});

test('hover opens; scrolling the detail stays open; parent scroll and window resize dismiss', async () => {
  const {button, popup} = await mount();
  await event(button, mouse('mouseover'));
  expect(popup.hasAttribute('data-visible')).toBe(true);
  await event(popup, new Event('scroll'));
  expect(popup.hasAttribute('data-visible')).toBe(true);
  await event(host, new Event('scroll'));
  expect(popup.hasAttribute('data-visible')).toBe(false);
  await event(button, mouse('mouseover'));
  expect(popup.hasAttribute('data-visible')).toBe(true);
  await event(window, new Event('resize'));
  expect(popup.hasAttribute('data-visible')).toBe(false);
});

test('keyboard focus and tap open details, Escape, outside pointer and blur dismiss', async () => {
  const {button, popup} = await mount();
  await act(() => button.focus());
  expect(popup.hasAttribute('data-visible')).toBe(true);
  await event(button, new KeyboardEvent('keydown', {bubbles: true, key: 'Escape'}));
  expect(popup.hasAttribute('data-visible')).toBe(false);
  await event(button, mouse('click'));
  expect(popup.hasAttribute('data-visible')).toBe(true);
  await event(document.body, new PointerEvent('pointerdown', {bubbles: true}));
  expect(popup.hasAttribute('data-visible')).toBe(false);
  await event(button, mouse('click'));
  await act(() => button.blur());
  expect(popup.hasAttribute('data-visible')).toBe(false);
});

test('hover exit allows crossing the gap and closes after leaving, with cleanup on unmount', async () => {
  jest.useFakeTimers();
  const {button, popup} = await mount();
  await event(button, mouse('mouseover'));
  await event(button, mouse('mouseout', document.body));
  expect(popup.hasAttribute('data-visible')).toBe(true);
  await event(popup, mouse('mouseover', document.body));
  await act(() => jest.advanceTimersByTime(200));
  expect(popup.hasAttribute('data-visible')).toBe(true);
  await event(popup, mouse('mouseout', document.body));
  await act(() => jest.advanceTimersByTime(200));
  expect(popup.hasAttribute('data-visible')).toBe(false);
  await event(button, mouse('mouseover'));
  await event(button, mouse('mouseout', document.body));
  await act(() => root.unmount());
  await act(() => jest.advanceTimersByTime(200));
});
