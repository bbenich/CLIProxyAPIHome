import type { ReactNode } from 'react';
import styles from './QuotaDashboard.module.css';

interface Option<Value extends string> { value: Value; label: string; icon?: ReactNode }
export function OptionGroup<Value extends string>({ label, value, options, onChange }: {
  label: string; value: Value; options: Option<Value>[]; onChange: (value: Value) => void;
}) {
  return <div className={styles.optionGroup} role="group" aria-label={label}>
    <span className={styles.optionLabel} aria-hidden="true">{label}</span>
    <div className={styles.segments}>
      {options.map((option) => <button key={option.value} type="button" aria-pressed={value === option.value} className={styles.segment} onClick={() => onChange(option.value)}>
        {option.icon}<span>{option.label}</span>
      </button>)}
    </div>
  </div>;
}
export function ViewIcon({ table }: { table?: boolean }) {
  return <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    {table ? <><rect x="3" y="4" width="18" height="16" rx="2" /><path d="M3 10h18M3 15h18M10 10v10" /></> : <><rect x="3" y="3" width="7" height="7" rx="1.5" /><rect x="14" y="3" width="7" height="7" rx="1.5" /><rect x="3" y="14" width="7" height="7" rx="1.5" /><rect x="14" y="14" width="7" height="7" rx="1.5" /></>}
  </svg>;
}
