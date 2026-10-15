import { describe, expect, it } from 'vitest';
import { Lanes } from './lanes';
import { ROW, laneX, strokes } from './graph';

/** The (x, y) of a stroke's first and last point. */
function ends(d: string): { from: [number, number]; to: [number, number] } {
	const n = d.match(/-?\d+(\.\d+)?/g)!.map(Number);
	return { from: [n[0], n[1]], to: [n[n.length - 2], n[n.length - 1]] };
}

describe('strokes', () => {
	// Decision: a lane leaving the bottom edge of a row enters the top edge of the next at the same x, across a fork and a merge, so the graph is unbroken.
	it('meets the previous row’s lane ends at the next row’s top edge', () => {
		// m merges b into a's line; b and a both fork from r.
		const rows = new Lanes().extend([
			{ oid: 'm', parents: ['a', 'b'] },
			{ oid: 'a', parents: ['r'] },
			{ oid: 'b', parents: ['r'] },
			{ oid: 'r', parents: [] },
		]);
		for (let i = 1; i < rows.length; i++) {
			const bottoms = strokes(rows[i - 1])
				.map((s) => ends(s.d).to)
				.filter(([, y]) => y === ROW)
				.map(([x]) => x)
				.sort((p, q) => p - q);
			const tops = strokes(rows[i])
				.map((s) => ends(s.d).from)
				.filter(([, y]) => y === 0)
				.map(([x]) => x)
				.sort((p, q) => p - q);
			expect(tops, `row ${i}`).toEqual(bottoms);
		}
	});

	// Decision: a branch-out edge leaves the node and ends at the bottom edge of its own column.
	it('draws a branch-out from the node to the bottom of the new column', () => {
		const [m] = new Lanes().extend([{ oid: 'm', parents: ['a', 'b'] }]);
		const s = strokes(m);
		expect(s).toHaveLength(2);
		expect(ends(s[1].d)).toEqual({ from: [laneX(0), ROW / 2], to: [laneX(1), ROW] });
		expect(s[1].color).not.toBe(s[0].color);
	});
});
