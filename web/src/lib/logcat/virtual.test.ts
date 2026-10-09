import { describe, expect, it } from 'vitest';
import { rowWindow } from './virtual';

const base = { viewportHeight: 360, rowHeight: 18, overscan: 5, following: false };

describe('rowWindow', () => {
	it('renders the rows in view plus the overscan', () => {
		const w = rowWindow({ ...base, scrollTop: 1800, count: 1000 });
		// rows 100..119 are in view; 5 more on each side
		expect(w.start).toBe(95);
		expect(w.end).toBe(125);
		expect(w.offset).toBe(95 * 18);
		expect(w.totalHeight).toBe(1000 * 18);
	});

	it('clamps to the list at both ends', () => {
		expect(rowWindow({ ...base, scrollTop: 0, count: 1000 }).start).toBe(0);
		const end = rowWindow({ ...base, scrollTop: 1e9, count: 1000 });
		expect(end.end).toBe(1000);
		expect(end.start).toBeLessThan(1000);
		const few = rowWindow({ ...base, scrollTop: 0, count: 3 });
		expect([few.start, few.end]).toEqual([0, 3]);
		expect(rowWindow({ ...base, scrollTop: 0, count: 0 })).toMatchObject({ start: 0, end: 0 });
	});

	// Decision: while following, the window is the bottom whatever the last scroll event
	// said, so a frame never renders blank rows under fresh lines.
	it('shows the bottom when following, ignoring a stale scroll position', () => {
		const w = rowWindow({ ...base, scrollTop: 0, count: 1000, following: true });
		expect(w.end).toBe(1000);
		expect(w.start).toBe(1000 - 20 - 5);
	});

	// Decision (A8): the DOM is the same size at 1,000 lines and at the cap.
	it('renders as many rows at 50,000 as at 1,000', () => {
		const at = (count: number) => {
			const w = rowWindow({ ...base, scrollTop: 0, count, following: true });
			return w.end - w.start;
		};
		expect(at(50_000)).toBe(at(1000));
		expect(at(1000)).toBe(25);
	});
});
