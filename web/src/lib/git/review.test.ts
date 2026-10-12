import { describe, expect, it } from 'vitest';
import {
	alignLeft,
	alignRight,
	anchorOf,
	Driver,
	ElasticMap,
	gutter,
	lineOfRow,
	rowOfLine,
	rows,
	type LineHunk,
	type Row,
} from './review';

const H = 20;
const h = (fromA: number, toA: number, fromB: number, toB: number): LineHunk => ({ fromA, toA, fromB, toB });
const kinds = (rs: Row[]) => rs.map((r) => (r.kind === 'same' ? 's' : r.kind === 'add' ? '+' : '-')).join('');

function model(hunks: LineHunk[], nA: number, nB: number) {
	const rs = rows(hunks, nA, nB);
	const r2l = rowOfLine(rs, nB);
	const geom = {
		left: Array.from({ length: nB }, (_, i) => ({ top: i * H, height: H })),
		right: rs.map((_, i) => ({ top: i * H, height: H })),
		rowOfLine: r2l,
	};
	return { rs, r2l, l2r: lineOfRow(rs, hunks, nB), map: new ElasticMap(geom) };
}

describe('rows', () => {
	// Decision (Q2 §4): the right pane has a row per buffer line, and the base's deleted
	// lines as red rows above their hunk's green ones; nothing else.
	it('lays out each shape of change', () => {
		expect(kinds(rows([h(2, 2, 2, 4)], 5, 7))).toBe('ss++sss'); // pure add
		expect(kinds(rows([h(2, 4, 2, 2)], 6, 4))).toBe('ss--ss'); // deletion mid-file
		expect(kinds(rows([h(0, 2, 0, 0)], 5, 3))).toBe('--sss'); // at the top
		expect(kinds(rows([h(3, 5, 3, 3)], 5, 3))).toBe('sss--'); // at the end
		expect(kinds(rows([h(1, 3, 1, 2)], 4, 3))).toBe('s--+s'); // modification: red above green
		expect(kinds(rows([h(1, 2, 1, 2), h(2, 3, 2, 3)], 4, 4))).toBe('s-+-+s'); // adjacent hunks
		expect(kinds(rows([h(0, 0, 0, 3)], 0, 3))).toBe('+++'); // empty base
		expect(kinds(rows([h(0, 3, 0, 0)], 3, 0))).toBe('---'); // empty buffer
		expect(rows([], 0, 0)).toEqual([]);
	});

	it('names the lines each row shows', () => {
		expect(rows([h(1, 3, 1, 2)], 4, 3)).toEqual([
			{ kind: 'same', a: 0, b: 0 },
			{ kind: 'del', a: 1, hunk: 0 },
			{ kind: 'del', a: 2, hunk: 0 },
			{ kind: 'add', b: 1 },
			{ kind: 'same', a: 3, b: 2 },
		]);
	});

	// Decision: rowOfLine is exact for every line; lineOfRow of a red row is its block's line
	// (where it was deleted), clamped into the buffer.
	it('maps lines to rows and back', () => {
		const cases: [LineHunk[], number, number][] = [
			[[h(2, 4, 2, 2)], 6, 4],
			[[h(0, 2, 0, 0)], 5, 3],
			[[h(3, 5, 3, 3)], 5, 3],
			[[h(1, 3, 1, 2), h(4, 4, 3, 5)], 6, 7],
			[[h(0, 3, 0, 0)], 3, 0],
		];
		for (const [hs, nA, nB] of cases) {
			const { rs, r2l, l2r } = model(hs, nA, nB);
			for (let line = 0; line < nB; line++) {
				const r = rs[r2l[line]!]!;
				expect(r.kind === 'del' ? -1 : r.b).toBe(line);
				expect(l2r[r2l[line]!]).toBe(line);
			}
			rs.forEach((r, i) => {
				if (r.kind === 'del') expect(l2r[i]).toBe(Math.min(hs[r.hunk]!.fromB, Math.max(nB - 1, 0)));
			});
		}
		const m = model([h(2, 4, 2, 2)], 6, 4);
		expect(m.l2r[2]).toBe(2); // red rows 2 and 3 sit on line 2
		expect(m.l2r[3]).toBe(2);
	});
});

describe('gutter', () => {
	// Decision: a hunk that only adds marks `added`, one that also removes marks `modified`,
	// and a pure deletion is a marker between lines.
	it('marks added, modified and deleted', () => {
		expect(gutter([h(2, 2, 2, 4), h(5, 7, 7, 8), h(9, 11, 10, 10)])).toEqual({
			lines: [
				{ line: 2, kind: 'added' },
				{ line: 3, kind: 'added' },
				{ line: 7, kind: 'modified' },
			],
			deleted: [{ at: 10, count: 2 }],
		});
	});
});

describe('the elastic map', () => {
	// Decision: a left line's top meets its row's top exactly.
	it('puts every line top on its row top', () => {
		for (const [hs, nA, nB] of [
			[[h(2, 4, 2, 2)], 6, 4],
			[[h(0, 3, 0, 0)], 6, 3],
			[[h(3, 6, 3, 3)], 6, 3],
			[[h(1, 3, 1, 2), h(2, 2, 3, 5)], 4, 6],
		] as [LineHunk[], number, number][]) {
			const { rs, r2l, map } = model(hs, nA, nB);
			expect(rs.length).toBeGreaterThan(0);
			for (let i = 0; i < nB; i++) expect(map.toRight(i * H)).toBe(r2l[i]! * H);
		}
	});

	// A deletion block of ten rows after line 4: the right offset must be continuous and
	// monotone through it, at most as steep as (block + line) / line.
	const block = 10;
	const hunks = [h(5, 5 + block, 5, 5)];
	const slope = (block * H + H) / H;

	function steps(toRight: (y: number) => number) {
		const out: number[] = [];
		for (let y = 3 * H; y < 6 * H; y++) out.push(toRight(y + 1) - toRight(y));
		return out;
	}

	it('slides through a deletion block without a step', () => {
		const { map } = model(hunks, 5 + block + 3, 8);
		const s = steps((y) => map.toRight(y));
		expect(Math.min(...s)).toBeGreaterThanOrEqual(0);
		expect(Math.max(...s)).toBeLessThanOrEqual(slope + 1e-9);
		// Inside the line before the block the slope is exactly (block + line) / line.
		expect(map.toRight(4 * H + 1) - map.toRight(4 * H)).toBeCloseTo(slope, 9);
		expect(map.toRight(5 * H)).toBe((5 + block) * H);
	});

	it('is failed by a step map that jumps at the line boundary', () => {
		// The wrong map: slope 1 inside a line, and a jump to the row at the boundary.
		const { r2l } = model(hunks, 5 + block + 3, 8);
		const stepMap = (y: number) => {
			const i = Math.min(Math.floor(y / H), 7);
			return r2l[i]! * H + (y - i * H);
		};
		expect(Math.max(...steps(stepMap))).toBeGreaterThan(slope + 1e-9);
	});

	// Decision: toLeft inverts toRight, so the right pane can drive the left.
	it('is inverted by toLeft', () => {
		for (const [hs, nA, nB] of [
			[hunks, 5 + block + 3, 8],
			[[h(0, 2, 0, 0)], 5, 3],
			[[h(3, 5, 3, 3)], 5, 3],
			[[h(0, 0, 0, 0)], 0, 0],
			[[h(0, 3, 0, 0)], 3, 0],
		] as [LineHunk[], number, number][]) {
			const { map } = model(hs, nA, nB);
			for (let y = -50; y < 400; y += 3.7) expect(Math.abs(map.toLeft(map.toRight(y)) - y)).toBeLessThan(1e-6);
		}
	});

	it('sends a red row into the line before its block', () => {
		const { map } = model(hunks, 5 + block + 3, 8);
		const redTop = 7 * H; // the third red row
		expect(map.toLeft(redTop)).toBeGreaterThanOrEqual(4 * H);
		expect(map.toLeft(redTop)).toBeLessThan(5 * H);
	});

	it('copes with an empty buffer and with measured, unequal heights', () => {
		// The editor still shows one empty line: the caller says the left content is H tall.
		const rs0 = rows([h(0, 3, 0, 0)], 3, 0);
		const empty = new ElasticMap({ left: [], right: rs0.map((_, i) => ({ top: i * H, height: H })), rowOfLine: [], leftEnd: H });
		expect(empty.toRight(0)).toBe(0);
		expect(empty.toRight(H)).toBe(3 * H); // one empty line against three red rows
		const rs = rows([h(1, 2, 1, 1)], 3, 2);
		const m = new ElasticMap({
			left: [{ top: 0, height: 18 }, { top: 18, height: 30 }],
			right: [{ top: 0, height: 18 }, { top: 18, height: 40 }, { top: 58, height: 30 }],
			rowOfLine: rowOfLine(rs, 2),
		});
		expect(m.toRight(18)).toBe(58);
		expect(m.toRight(9)).toBeCloseTo(29, 9); // halfway through the line before the 40 px block
		expect(m.toRight(0 + 18 - 1e-9)).toBeCloseTo(58, 6);
	});
});

describe('alignment', () => {
	it('pads the pane so any anchor can sit at its offset', () => {
		const { map } = model([h(0, 4, 0, 0)], 7, 3); // four red rows above line 0
		// Line 0's top is at right y 80; at offset 200 the scroll would be negative.
		expect(alignRight(map, 0, 200, 300)).toEqual({ scrollTop: 0, padTop: 120, padBottom: 0 });
		// Near the end the right pane needs room below.
		const a = alignRight(map, 2 * H, 0, 500);
		expect(a.scrollTop).toBe(6 * H);
		expect(a.padBottom).toBe(6 * H + 500 - 7 * H);
		const l = alignLeft(map, 5 * H, 10, 100);
		expect(l.scrollTop).toBeCloseTo(map.toLeft(5 * H) - 10, 9);
	});
});

describe('the anchor', () => {
	// Decision: the cursor line while it is visible, else the viewport's centre.
	it('is the cursor line in view, else the centre', () => {
		const vp = { scrollTop: 100, height: 200 };
		expect(anchorOf({ top: 150, height: 20 }, vp)).toEqual({ y: 150, offset: 50 });
		expect(anchorOf({ top: 20, height: 20 }, vp)).toEqual({ y: 200, offset: 100 });
		expect(anchorOf({ top: 290, height: 20 }, vp)).toEqual({ y: 200, offset: 100 }); // half out
		expect(anchorOf(null, vp)).toEqual({ y: 200, offset: 100 });
	});
});

describe('the driver', () => {
	// Decision: the pane the user last scrolled drives; the follower's echo is not the user.
	it('ignores the follower echo and switches on a user scroll there', () => {
		const d = new Driver();
		expect(d.side).toBe('left');
		d.expect('right', 300);
		expect(d.scrolled('right', 300)).toBe(false);
		expect(d.side).toBe('left');
		expect(d.scrolled('left', 10)).toBe(true);
		d.expect('right', 300);
		expect(d.scrolled('right', 450)).toBe(true); // not what code set
		expect(d.side).toBe('right');
		expect(d.follower).toBe('left');
		// An echo is consumed once: the same position again is the user's.
		d.expect('left', 90);
		expect(d.scrolled('left', 90.4)).toBe(false);
		expect(d.scrolled('left', 90.4)).toBe(true);
	});

	// Decision (step 7): code that set a position the pane was already at gets no scroll event; the
	// echo it recorded is released, so the user's next scroll to about that place is the user's.
	it('releases a pending echo that no event followed', () => {
		const d = new Driver();
		d.expect('right', 300);
		d.release('right', 300);
		expect(d.scrolled('right', 300)).toBe(true);
		expect(d.side).toBe('right');
	});

	// Decision (step 7, code-review): while a scroll eases in, each frame sets a newer position; a
	// release from an earlier frame must not drop it, or the follower's own echo takes over the drive.
	it('keeps a newer pending echo when an older one is released', () => {
		const d = new Driver();
		d.expect('right', 300);
		d.expect('right', 310); // the next frame of the easing
		d.release('right', 300); // the first frame's release, two frames late
		expect(d.scrolled('right', 310)).toBe(false); // still the code's echo
		expect(d.side).toBe('left');
	});
});
