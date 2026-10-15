import { describe, expect, it } from 'vitest';
import { Lanes, type LaneInput, type LaneRow } from './lanes';

const c = (oid: string, ...parents: string[]): LaneInput => ({ oid, parents });
const layout = (rows: LaneInput[]) => new Lanes().extend(rows);
const cols = (r: LaneRow) => r.column;

// Each test asserts one clause of ADR-0070 §1; the comment names it.
describe('lane rule (ADR-0070 §1)', () => {
	// "its first parent inherits that lane": a chain never leaves column 0 or changes colour.
	it('keeps a linear history in column 0, one colour, one straight edge per row', () => {
		const rows = layout([c('a', 'b'), c('b', 'c'), c('c')]);
		expect(rows.map(cols)).toEqual([0, 0, 0]);
		expect(new Set(rows.map((r) => r.color)).size).toBe(1);
		expect(rows[0].edges).toEqual([{ kind: 'straight', from: 0, to: 0, color: rows[0].color }]);
		expect(rows[2].edges).toEqual([]); // a root ends its lane
		expect(rows.every((r) => r.through.length === 0 && r.width === 1)).toBe(true);
	});

	// "another parent ... opens one" and "lanes waiting for the same commit close where it is drawn".
	it('draws a branch and its merge: the merge opens a lane, the fork closes it', () => {
		// m merges (first parent a, second b); a and b both have parent r.
		const rows = layout([c('m', 'a', 'b'), c('a', 'r'), c('b', 'r'), c('r')]);
		const [m, a, b, r] = rows;
		expect(m.column).toBe(0);
		expect(m.edges.map((e) => [e.kind, e.from, e.to])).toEqual([
			['straight', 0, 0],
			['branch-out', 0, 1],
		]);
		expect(m.edges[1].color).not.toBe(m.color);
		expect([a.column, a.through]).toEqual([0, [{ column: 1, color: m.edges[1].color }]]);
		expect([b.column, b.through]).toEqual([1, [{ column: 0, color: m.color }]]);
		// r is waited for by two lanes: it takes the first, the other converges on it.
		expect(r.column).toBe(0);
		expect(r.incoming.map((l) => l.column)).toEqual([0, 1]);
		expect(r.through).toEqual([]);
		expect(rows.map((x) => x.width)).toEqual([2, 2, 2, 2]);
	});

	// "another parent joins a lane already waiting for it": a merge-in, in the existing lane's colour.
	it("merges into a lane that already waits for the parent (merge-in, in that lane's colour)", () => {
		const rows = layout([c('p', 'a'), c('q', 'b', 'a'), c('a'), c('b')]);
		const q = rows[1];
		expect(q.column).toBe(1); // not expected by any lane: the first free column
		expect(q.edges).toEqual([
			{ kind: 'straight', from: 1, to: 1, color: q.color },
			{ kind: 'merge-in', from: 1, to: 0, color: rows[0].color },
		]);
	});

	// "its first parent inherits that lane" even when another lane already waits for that parent.
	it('lets a first parent inherit the lane although another lane waits for it', () => {
		const rows = layout([c('p', 'a'), c('q', 'a'), c('a')]);
		expect(rows[1].edges).toEqual([{ kind: 'straight', from: 1, to: 1, color: rows[1].color }]);
		expect(rows[2].incoming.map((l) => l.column)).toEqual([0, 1]);
	});

	// An octopus: first parent straight, every other parent a new lane in the first free column.
	it('draws an octopus: one straight edge, the rest branch out to successive free columns', () => {
		const rows = layout([c('o', 'a', 'b', 'c', 'd'), c('a'), c('b'), c('c'), c('d')]);
		expect(rows[0].edges.map((e) => [e.kind, e.to])).toEqual([
			['straight', 0],
			['branch-out', 1],
			['branch-out', 2],
			['branch-out', 3],
		]);
		expect(new Set(rows[0].edges.map((e) => e.color)).size).toBe(4);
		expect(rows[0].width).toBe(4);
		// Each parent is a root: its lane closes at its node and frees the column.
		expect(rows.slice(1).map(cols)).toEqual([0, 1, 2, 3]);
		expect(rows[1].through.map((l) => l.column)).toEqual([1, 2, 3]);
		expect(rows[4].through).toEqual([]);
	});

	// "else the first free column", and a root ends its lane.
	it('lays out two roots in separate lanes', () => {
		const rows = layout([c('a', 'b'), c('x', 'y'), c('b'), c('y')]);
		expect(rows.map(cols)).toEqual([0, 1, 0, 1]);
		expect(rows[2].through).toEqual([{ column: 1, color: rows[1].color }]);
		expect(rows[3].through).toEqual([]);
	});

	// "else the first free column" after a lane closed; a new lane gets a new colour.
	it('reuses the first free column for a new lane, with a new colour', () => {
		const rows = layout([c('a', 'b'), c('x', 'y'), c('b'), c('z')]);
		expect(rows[3].column).toBe(0); // column 0 was freed by root b
		expect(rows[3].color).not.toBe(rows[0].color);
		expect(rows[3].through).toEqual([{ column: 1, color: rows[1].color }]);
	});

	// A parent not loaded yet keeps its lane waiting; the next page picks it up unchanged.
	it('runs the lane of a parent not loaded yet to the bottom edge and continues it in the next page', () => {
		const l = new Lanes();
		const page1 = l.extend([c('a', 'b'), c('m', 'x', 'y')]);
		expect(page1[1].edges.map((e) => [e.from, e.to])).toEqual([
			[1, 1],
			[1, 2],
		]);
		const page2 = l.extend([c('b'), c('x'), c('y')]);
		expect(page2[0].incoming).toEqual([{ column: 0, color: page1[0].color }]);
		expect(page2[1].column).toBe(1);
		expect(page2[1].incoming[0].color).toBe(page1[1].color);
		expect(page2[2].column).toBe(2);
		expect(page2[2].incoming[0].color).toBe(page1[1].edges[1].color);
	});

	it('reports the widest row so far', () => {
		const l = new Lanes();
		expect(l.width).toBe(0);
		l.extend([c('o', 'a', 'b', 'c')]);
		expect(l.width).toBe(3);
		l.extend([c('a'), c('b'), c('c')]);
		expect(l.width).toBe(3);
	});
});

// Seeded PRNG (mulberry32): the property test must be reproducible.
function prng(seed: number): () => number {
	let a = seed >>> 0;
	return () => {
		a = (a + 0x6d2b79f5) >>> 0;
		let t = a;
		t = Math.imul(t ^ (t >>> 15), t | 1);
		t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
		return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
	};
}

/** A random DAG in newest-first order: parents have a higher index than their child. */
function randomDag(rand: () => number, n: number): LaneInput[] {
	const rows: LaneInput[] = [];
	for (let i = 0; i < n; i++) {
		const parents: string[] = [];
		const k = i === n - 1 ? 0 : rand() < 0.04 ? 0 : rand() < 0.2 ? 2 + Math.floor(rand() * 3) : 1;
		for (let j = 0; j < k; j++) {
			// Mostly near, sometimes far; a duplicate is dropped (git has none).
			const dist = rand() < 0.7 ? 1 + Math.floor(rand() * 4) : 1 + Math.floor(rand() * (n - i));
			const p = i + dist;
			if (p < n && !parents.includes(`c${p}`)) parents.push(`c${p}`);
		}
		rows.push({ oid: `c${i}`, parents });
	}
	return rows;
}

describe('lane layout properties', () => {
	// The promise: every parent edge ends at its parent's node column, and no row has two lanes in
	// one column. Checked from the output alone: the oid each column waits for is rebuilt from the
	// edges, and every row must find exactly the lanes that wait for its commit.
	it("ends every parent edge at the parent's node column, with no two lanes sharing a column in a row", () => {
		const rand = prng(20261012);
		for (let g = 0; g < 2000; g++) {
			const input = randomDag(rand, 1 + Math.floor(rand() * 300));
			const rows = layout(input);
			const waiting = new Map<number, string>();
			rows.forEach((r, i) => {
				const { oid, parents } = input[i];
				const ctx = `dag ${g} row ${i}`;
				const byCol = (x: number, y: number) => x - y;
				const expectIn = [...waiting].filter(([, o]) => o === oid).map(([col]) => col).sort(byCol);
				const expectThrough = [...waiting].filter(([, o]) => o !== oid).map(([col]) => col).sort(byCol);
				expect(r.incoming.map((l) => l.column), ctx).toEqual(expectIn);
				expect(r.through.map((l) => l.column), ctx).toEqual(expectThrough);
				// The node sits in the first lane waiting for it, else in a column no through lane holds.
				if (expectIn.length) expect(r.column, ctx).toBe(expectIn[0]);
				else expect(expectThrough, ctx).not.toContain(r.column);
				// One edge per parent, all from the node; a merge-in lands on a lane waiting for that parent.
				expect(r.edges.length, ctx).toBe(parents.length);
				for (const l of r.incoming) waiting.delete(l.column);
				r.edges.forEach((e, p) => {
					expect(e.from, ctx).toBe(r.column);
					if (e.kind === 'merge-in') expect(waiting.get(e.to), ctx).toBe(parents[p]);
					else expect(waiting.has(e.to), ctx).toBe(false);
					waiting.set(e.to, parents[p]);
				});
				// Bottom edge: lanes are the through ones plus the straight and branch-out ends.
				const opened = r.edges.filter((e) => e.kind !== 'merge-in').map((e) => e.to);
				const bottom = [...expectThrough, ...opened];
				expect(new Set(bottom).size, ctx).toBe(bottom.length);
				// Top edge likewise.
				const top = [...r.through, ...r.incoming].map((l) => l.column);
				expect(new Set(top).size, ctx).toBe(top.length);
				expect(r.width, ctx).toBeGreaterThan(Math.max(r.column, ...top, ...bottom));
			});
			// Every parent is in the input, so no lane is left waiting at the end.
			expect(waiting.size, `dag ${g}`).toBe(0);
		}
	}, 60_000);

	// The promise of "extended page by page": any split gives the whole layout's rows.
	it('gives the same rows page by page as for the whole history', () => {
		const rand = prng(7);
		for (let g = 0; g < 200; g++) {
			const input = randomDag(rand, 1 + Math.floor(rand() * 300));
			const whole = layout(input);
			const l = new Lanes();
			const paged: LaneRow[] = [];
			for (let at = 0; at < input.length; ) {
				const size = 1 + Math.floor(rand() * 60);
				paged.push(...l.extend(input.slice(at, at + size)));
				at += size;
			}
			expect(paged).toEqual(whole);
			expect(l.width).toBe(Math.max(...whole.map((r) => r.width)));
		}
	});
});

describe('lane layout cost', () => {
	it("lays out 100,000 rows of gittest.Synthetic's shape (time recorded, not asserted)", () => {
		// main is a chain; 40 side branches of up to 10 commits fork from it and merge back.
		const total = 100_000;
		const branches = 40;
		const oldest: LaneInput[] = [];
		let n = 0;
		const mk = (parents: string[]) => {
			const oid = `m${n++}`;
			oldest.push({ oid, parents });
			return oid;
		};
		let tip = mk([]);
		let rest = total - 1;
		for (let b = 0; b < branches; b++) {
			const seg = Math.floor(rest / (branches - b));
			rest -= seg;
			const side = Math.min(10, Math.max(1, Math.floor(seg / 4)));
			for (let i = 0; i < seg - side - 1; i++) tip = mk([tip]);
			let st = tip;
			for (let i = 0; i < side; i++) st = mk([st]);
			tip = mk([tip, st]);
		}
		expect(oldest.length).toBe(total);
		const newest = oldest.reverse();
		const t0 = performance.now();
		const rows = new Lanes().extend(newest);
		const ms = performance.now() - t0;
		console.log(`lanes: ${rows.length} rows in ${ms.toFixed(1)} ms`);
		expect(rows.length).toBe(total);
	});
});
