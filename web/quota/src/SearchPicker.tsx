import { useEffect, useLayoutEffect, useId, useRef, useState } from 'react';
import { pickerIndex, pickerResults, pickerPlacement, type PickerOption } from './picker';
import styles from './QuotaDashboard.module.css';

export function SearchPicker({ label, value, options, onChange }: {
  label: string; value: string; options: PickerOption[]; onChange: (value: string) => void;
}) {
  const id = useId();
  const root = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const list = useRef<HTMLDivElement>(null);
  const popup = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [active, setActive] = useState(0);
  const { items, total } = pickerResults(options, query);
  const index = items.length ? Math.min(active, items.length - 1) : -1;
  const selected = options.find((option) => option.value === value);
  const close = (restore = false) => {
    setOpen(false);
    if (restore) trigger.current?.focus();
  };
  const choose = (option: PickerOption) => { onChange(option.value); close(true); };
  useLayoutEffect(() => {
    if (!open || !popup.current || !trigger.current) return;
    const element = popup.current;
    const place = () => {
      if (!trigger.current) return;
      const placement = pickerPlacement(trigger.current.getBoundingClientRect(), { width: window.innerWidth, height: window.innerHeight });
      Object.assign(element.style, { left: `${placement.left}px`, top: `${placement.top}px`, width: `${placement.width}px`, maxHeight: `${placement.maxHeight}px` });
      element.style.setProperty('--picker-max-height', `${placement.maxHeight}px`);
    };
    place();
    // Native top layer escapes all page stacking contexts without a huge z-index.
    element.showPopover?.();
    window.addEventListener('resize', place);
    window.addEventListener('scroll', place, true);
    return () => {
      window.removeEventListener('resize', place);
      window.removeEventListener('scroll', place, true);
      if (element.matches(':popover-open')) element.hidePopover();
    };
  }, [open]);
  useEffect(() => {
    if (!open) return;
    input.current?.focus();
    const outside = (event: PointerEvent) => {
      if (event.target instanceof Node && !root.current?.contains(event.target)) setOpen(false);
    };
    document.addEventListener('pointerdown', outside);
    return () => document.removeEventListener('pointerdown', outside);
  }, [open]);
  useEffect(() => {
    if (open && index >= 0) list.current?.children[index]?.scrollIntoView({ block: 'nearest' });
  }, [open, index, query]);
  return <div ref={root} className={styles.picker} onBlur={(event) => {
    if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setOpen(false);
  }}>
    <span id={`${id}-label`} className={styles.optionLabel}>{label}</span>
    <button ref={trigger} type="button" className={styles.pickerTrigger} aria-haspopup="listbox"
      aria-expanded={open} aria-controls={open ? `${id}-list` : undefined} aria-labelledby={`${id}-label ${id}-value`}
      onClick={() => { setQuery(''); setActive(0); setOpen(!open); }}
      onKeyDown={(event) => {
        if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
          event.preventDefault(); setQuery(''); setActive(0); setOpen(true);
        } else if (event.key === 'Escape') close();
      }}>
      <span id={`${id}-value`}>{selected?.label ?? 'All'}</span>
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden="true"><path d="m6 9 6 6 6-6" /></svg>
    </button>
    {open && <div ref={popup} popover="manual" className={styles.pickerPopup}>
      <input ref={input} className={styles.pickerSearch} type="text" role="combobox" autoComplete="off"
        aria-label={`Search ${label.toLowerCase()}`} aria-expanded="true" aria-autocomplete="list"
        aria-controls={`${id}-list`} aria-describedby={`${id}-count`}
        aria-activedescendant={index >= 0 ? `${id}-option-${index}` : undefined}
        placeholder={`Search ${label.toLowerCase()}…`} value={query}
        onChange={(event) => { setQuery(event.target.value); setActive(0); }}
        onKeyDown={(event) => {
          if (event.nativeEvent.isComposing || event.nativeEvent.keyCode === 229) return;
          if (['ArrowDown', 'ArrowUp'].includes(event.key) || (event.ctrlKey && ['Home', 'End'].includes(event.key))) {
            event.preventDefault(); setActive(pickerIndex(event.key, index, items.length));
          } else if (event.key === 'Enter') {
            event.preventDefault(); if (index >= 0) choose(items[index]);
          } else if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); close(true); }
        }} />
      <div id={`${id}-count`} className={styles.pickerCount} role="status">
        {total === 0 ? 'No matches' : total > items.length ? `${items.length} of ${total} matches · type to narrow` : `${total} ${total === 1 ? 'option' : 'options'}`}
      </div>
      <div ref={list} id={`${id}-list`} className={styles.pickerList} role="listbox" aria-labelledby={`${id}-label`}>
        {items.map((option, position) => <div key={option.value} id={`${id}-option-${position}`}
          role="option" aria-selected={value === option.value} data-active={index === position}
          className={styles.pickerOption} onPointerMove={() => setActive(position)}
          onMouseDown={(event) => event.preventDefault()} onClick={() => choose(option)}>
          <span>{option.label}</span><span aria-hidden="true">{value === option.value ? '✓' : ''}</span>
        </div>)}
      </div>
    </div>}
  </div>;
}
