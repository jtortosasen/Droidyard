import { describe, expect, it } from 'vitest';
import { Selection, topmost } from './selection';

const rows = ['a', 'a/x', 'b', 'c', 'd'];

describe('Selection', () => {
	// Decision (D51): ⌘-click toggles one row in and out, without touching the others.
	it('toggles a row in and out', () => {
		const s = new Selection();
		s.click('a');
		s.toggle('c');
		expect([...s.paths]).toEqual(['a', 'c']);
		s.toggle('a');
		expect([...s.paths]).toEqual(['c']);
	});

	// Decision (D51): ⇧-click selects from the anchor to the row, in row order, either direction.
	it('selects the range from the anchor in row order', () => {
		const s = new Selection();
		s.click('b');
		s.range('d', rows);
		expect([...s.paths]).toEqual(['b', 'c', 'd']);
		s.range('a/x', rows);
		expect([...s.paths]).toEqual(['a/x', 'b']);
		expect(s.anchor).toBe('b');
	});

	// Decision (D51): ⇧↓ grows the range from the anchor and ⇧↑ shrinks it.
	it('extends and shrinks with the keys', () => {
		const s = new Selection();
		s.click('a/x');
		s.extend(1, rows);
		s.extend(1, rows);
		expect([...s.paths]).toEqual(['a/x', 'b', 'c']);
		s.extend(-1, rows);
		expect([...s.paths]).toEqual(['a/x', 'b']);
	});

	// Decision (D51): an operation acts on the top-most paths; a folder's selected child is dropped,
	// a sibling sharing the prefix is not.
	it('keeps a folder and drops its child, not a prefix sibling', () => {
		expect(topmost(['a', 'a/x', 'a/x/y', 'ab', 'b/c'])).toEqual(['a', 'ab', 'b/c']);
		const s = new Selection();
		s.click('a');
		s.toggle('a/x');
		expect(s.topmost()).toEqual(['a']);
	});

	// Decision (D51): rows that went are pruned from the selection, the anchor and the focus.
	it('prunes paths that are gone', () => {
		const s = new Selection();
		s.click('b');
		s.range('d', rows);
		s.prune((p) => p !== 'c' && p !== 'd');
		expect([...s.paths]).toEqual(['b']);
		expect(s.focus).toBeNull();
		expect(s.anchor).toBe('b');
	});
});
