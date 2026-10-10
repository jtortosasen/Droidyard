import { describe, expect, it } from 'vitest';
import { BOUNDS, DEFAULT_LAYOUT, STORAGE_KEY, clampSize, loadLayout, saveLayout, type KeyValueStorage } from './layout';

function memory(initial?: string): KeyValueStorage & { data: Map<string, string> } {
	const data = new Map<string, string>();
	if (initial !== undefined) data.set(STORAGE_KEY, initial);
	return {
		data,
		getItem: (k) => data.get(k) ?? null,
		setItem: (k, v) => void data.set(k, v),
	};
}

describe('layout', () => {
	// Decision (D25): a stored size is clamped on the way in, so a stale or hand-edited
	// entry cannot make a region unusable.
	it('clamps a size outside its bounds and replaces a non-number with the default', () => {
		expect(clampSize('left', 5)).toBe(BOUNDS.left.min);
		expect(clampSize('left', 99999)).toBe(BOUNDS.left.max);
		expect(clampSize('bottom', 'wide')).toBe(DEFAULT_LAYOUT.size.bottom);
		expect(clampSize('right', Number.NaN)).toBe(DEFAULT_LAYOUT.size.right);
		const l = loadLayout(memory(JSON.stringify({ size: { left: 1, right: 1e9, bottom: 300 } })));
		expect(l.size).toEqual({ left: BOUNDS.left.min, right: BOUNDS.right.max, bottom: 300 });
	});

	// Decision (D25): sizes, open state and density are restored as they were saved.
	it('restores what it saved', () => {
		const s = memory();
		const layout = {
			size: { left: 300, right: 400, bottom: 200 },
			open: { left: false, right: true, bottom: false },
			density: 'comfortable' as const,
		};
		saveLayout(s, layout);
		expect(loadLayout(s)).toEqual(layout);
	});

	// Decision (D25): storage is a convenience; a storage that throws on read and on write
	// leaves the defaults and no exception, and so does corrupt stored text.
	it('survives a storage that throws and one that holds garbage', () => {
		const throwing: KeyValueStorage = {
			getItem: () => {
				throw new Error('blocked');
			},
			setItem: () => {
				throw new Error('blocked');
			},
		};
		expect(loadLayout(throwing)).toEqual(DEFAULT_LAYOUT);
		expect(() => saveLayout(throwing, DEFAULT_LAYOUT)).not.toThrow();
		expect(loadLayout(null)).toEqual(DEFAULT_LAYOUT);
		expect(loadLayout(memory('{not json'))).toEqual(DEFAULT_LAYOUT);
		expect(loadLayout(memory('"text"'))).toEqual(DEFAULT_LAYOUT);
		expect(loadLayout(memory(JSON.stringify({ density: 'huge', open: { left: 'yes' } })))).toEqual(DEFAULT_LAYOUT);
	});

	it('does not hand out the shared defaults to be mutated', () => {
		const a = loadLayout(null);
		a.size.left = 1;
		expect(loadLayout(null).size.left).toBe(DEFAULT_LAYOUT.size.left);
	});
});
