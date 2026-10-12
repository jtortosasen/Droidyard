import { describe, expect, it } from 'vitest';
import { rowAt, rowTop, rowWindow } from './rows';

const base = { viewportHeight: 100, rowHeight: 10, overscan: 2, count: 1000 };

describe('rowWindow', () => {
	// Decision: at the top the window starts at row 0 (no negative overscan) and holds the viewport's rows plus the overscan below.
	it('renders the viewport and the overscan at the top', () => {
		expect(rowWindow({ ...base, scrollTop: 0 })).toEqual({ start: 0, end: 12, totalHeight: 10000, offset: 0 });
	});
	// Decision: in the middle the window has overscan on both edges and offset is where `start` sits.
	it('renders overscan on both edges in the middle', () => {
		const w = rowWindow({ ...base, scrollTop: 5000 });
		expect(w).toEqual({ start: 498, end: 512, totalHeight: 10000, offset: 4980 });
	});
	// Decision: past the end the scroll is clamped, and the window stops at the last row.
	it('clamps at the end', () => {
		const w = rowWindow({ ...base, scrollTop: 99999 });
		expect(w.end).toBe(1000);
		expect(w.start).toBe(988);
		expect(rowWindow({ ...base, scrollTop: -50 }).start).toBe(0);
	});
	it('is empty without rows', () => {
		expect(rowWindow({ ...base, count: 0, scrollTop: 0 })).toEqual({ start: 0, end: 0, totalHeight: 0, offset: 0 });
	});

	// Decision (step 8's room): one taller row grows the total, and every row after it sits `extra` lower.
	it('makes room for one expanded row: top', () => {
		const expanded = { index: 1, extra: 50 };
		const w = rowWindow({ ...base, scrollTop: 0, expanded });
		// Row 0 is 0-10, row 1 is 10-70, row 2 starts at 70: the 100 px show rows 0 to 4, and 2 more are overscan.
		expect(w).toEqual({ start: 0, end: 7, totalHeight: 10050, offset: 0 });
	});
	it('makes room for one expanded row: middle, after the expanded row', () => {
		const expanded = { index: 1, extra: 50 };
		// scrollTop 5050 is row (5050-50)/10 = 500 exactly
		const w = rowWindow({ ...base, scrollTop: 5050, expanded });
		expect(w.start).toBe(498);
		expect(w.end).toBe(512);
		expect(w.offset).toBe(rowTop(498, 10, expanded));
		expect(w.offset).toBe(5030);
	});
	it('starts inside the expanded row when the scroll is within it', () => {
		const expanded = { index: 100, extra: 200 };
		const w = rowWindow({ ...base, scrollTop: 1100, expanded }); // inside [1000, 1210)
		expect(w.start).toBe(98);
		expect(rowAt(1100, { ...base, expanded })).toBe(100);
		expect(rowAt(1209, { ...base, expanded })).toBe(100);
		expect(rowAt(1210, { ...base, expanded })).toBe(101);
	});
	it('makes room for one expanded row: end', () => {
		const expanded = { index: 998, extra: 40 };
		const w = rowWindow({ ...base, scrollTop: 1e9, expanded });
		expect(w.totalHeight).toBe(10040);
		expect(w.end).toBe(1000);
		expect(w.offset).toBe(rowTop(w.start, 10, expanded));
	});
	// Decision: an expanded index outside the rows, or no extra, changes nothing.
	it('ignores an expanded row that does not exist', () => {
		const plain = rowWindow({ ...base, scrollTop: 300 });
		expect(rowWindow({ ...base, scrollTop: 300, expanded: { index: 5000, extra: 80 } })).toEqual(plain);
		expect(rowWindow({ ...base, scrollTop: 300, expanded: { index: 5, extra: 0 } })).toEqual(plain);
		expect(rowWindow({ ...base, scrollTop: 300, expanded: null })).toEqual(plain);
	});
});
