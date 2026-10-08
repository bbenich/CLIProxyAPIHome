import { describe, expect, test } from 'bun:test';
import { renderToStaticMarkup } from 'react-dom/server';
import { SearchPicker } from '../src/SearchPicker';
import { pickerIndex, pickerResults, pickerPlacement, PICKER_LIMIT } from '../src/picker';

describe('searchable scope pickers', () => {
  const options = Array.from({ length: 2000 }, (_, id) => ({ value: String(id), label: `User ${id} engineering` }));
  test('bounds the rendered list while searching every option, including beyond the first page', () => {
    const result = pickerResults(options, '');
    expect(result.total).toBe(2000);
    expect(result.items).toHaveLength(PICKER_LIMIT);
    expect(pickerResults(options, '1999 engineering').items.map((item) => item.value)).toEqual(['1999']);
    expect(pickerResults(options, 'missing').items).toEqual([]);
  });
  test('matches names case-insensitively with accents and multiple terms', () => {
    expect(pickerResults([{ value: 'a', label: 'José Engineering' }], ' ENGINEERING  jose ').total).toBe(1);
    expect(pickerResults(options, '1999 sales').total).toBe(0);
  });
  test('keyboard navigation stays within results and handles an empty search', () => {
    expect(pickerIndex('ArrowDown', 0, 3)).toBe(1);
    expect(pickerIndex('ArrowDown', 2, 3)).toBe(2);
    expect(pickerIndex('ArrowUp', 0, 3)).toBe(0);
    expect(pickerIndex('Home', 2, 3)).toBe(0);
    expect(pickerIndex('End', 0, 3)).toBe(2);
    expect(pickerIndex('ArrowDown', 0, 0)).toBe(-1);
  });
  test('closed picker renders only the selected label, even in a large list', () => {
    const html = renderToStaticMarkup(<SearchPicker label="User" value="1999" options={options} onChange={() => {}} />);
    expect(html).toContain('User 1999 engineering');
    expect(html).toContain('aria-haspopup="listbox"');
    expect(html).toContain('aria-expanded="false"');
    expect(html).not.toContain('User 1998 engineering');
    expect(html).not.toContain('role="option"');
    expect(html).not.toContain('<select');
  });
});

test('popup placement stays in its viewport and opens upward when space below is short', () => {
 const bottom = pickerPlacement({left:700,right:900,top:430,bottom:470},{width:920,height:500});
 expect(bottom.top).toBeLessThan(430);
 expect(bottom.top + bottom.maxHeight).toBeLessThanOrEqual(500 - 12);
 expect(bottom.left + bottom.width).toBeLessThanOrEqual(920 - 12);
 const narrow = pickerPlacement({left:20,right:200,top:30,bottom:70},{width:240,height:500});
 expect(narrow.left).toBe(12);
 expect(narrow.width).toBe(216);
 expect(narrow.top).toBe(76);
});
