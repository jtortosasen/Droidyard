// Review mode's maps (ADR-0067 §4; plan m2l step 6), pure: numbers in, numbers out. No
// CodeMirror, no DOM. The view (step 7) measures the two panes and feeds the geometry in.
//
// Left is the buffer (B), right is the rendering: one row per buffer line, plus red rows
// for the base's (A) deleted lines, each block above its hunk's green rows.

/** A changed range of lines, 0-based, half-open: base lines [fromA, toA) became buffer lines [fromB, toB). Sorted. */
export interface LineHunk {
	fromA: number;
	toA: number;
	fromB: number;
	toB: number;
}

/** A right-pane row: `a` is a base line, `b` a buffer line. A red row also names its hunk. */
export type Row = { kind: 'same'; a: number; b: number } | { kind: 'add'; b: number } | { kind: 'del'; a: number; hunk: number };

/** The right pane's rows, top to bottom, for these hunks. */
export function rows(hunks: readonly LineHunk[], nA: number, nB: number): Row[] {
	const out: Row[] = [];
	let a = 0;
	let b = 0;
	const same = (toB: number) => {
		while (b < toB && b < nB) out.push({ kind: 'same', a: a++, b: b++ });
	};
	hunks.forEach((h, i) => {
		same(h.fromB);
		a = h.fromA;
		for (let x = h.fromA; x < h.toA; x++) out.push({ kind: 'del', a: x, hunk: i });
		for (let y = h.fromB; y < h.toB; y++) out.push({ kind: 'add', b: y });
		a = h.toA;
		b = h.toB;
	});
	same(nB);
	return out;
}

/** The row of each buffer line, indexed by line. */
export function rowOfLine(rs: readonly Row[], nB: number): number[] {
	const out = new Array<number>(nB).fill(0);
	rs.forEach((r, i) => {
		if (r.kind !== 'del') out[r.b] = i;
	});
	return out;
}

/**
 * The buffer line of each row, indexed by row. A red row answers its block's line: the line
 * the block was deleted before, clamped to the buffer (the last line for a deletion at the
 * end, line 0 for an empty buffer).
 */
export function lineOfRow(rs: readonly Row[], hunks: readonly LineHunk[], nB: number): number[] {
	const last = Math.max(nB - 1, 0);
	return rs.map((r) => (r.kind === 'del' ? Math.min(hunks[r.hunk]!.fromB, last) : r.b));
}

export interface Gutter {
	/** Lines the left gutter marks: a hunk that only added lines marks them `added`, one that also removed some marks them `modified`. */
	lines: { line: number; kind: 'added' | 'modified' }[];
	/** Deletion markers between lines: `at` is the line the lines were deleted before (the buffer's length for the end), `count` how many. */
	deleted: { at: number; count: number }[];
}

export function gutter(hunks: readonly LineHunk[]): Gutter {
	const g: Gutter = { lines: [], deleted: [] };
	for (const h of hunks) {
		const kind = h.toA > h.fromA ? 'modified' : 'added';
		for (let y = h.fromB; y < h.toB; y++) g.lines.push({ line: y, kind });
		if (h.toB === h.fromB) g.deleted.push({ at: h.fromB, count: h.toA - h.fromA });
	}
	return g;
}

/** A measured line or row: its top and height in its pane's content coordinates. */
export interface Span {
	top: number;
	height: number;
}

/** What `elastic` needs: the measured panes and which row each left line is. */
export interface Geometry {
	left: readonly Span[];
	right: readonly Span[];
	/** `rowOfLine(...)`: the right row of each left line. */
	rowOfLine: readonly number[];
	/** Content ends; default the last span's bottom (0 with no spans). */
	leftEnd?: number;
	rightEnd?: number;
}

/**
 * The elastic map between the panes: piecewise linear through the points (each left line's
 * top, its row's top) and (left end, right end), so a line's top meets its row's top exactly
 * and the right pane slides through a deletion block while the preceding line goes by (slope
 * (block + line) / line there). Strictly increasing on both axes, so `toLeft` inverts
 * `toRight`; beyond the ends both continue with slope 1.
 */
export class ElasticMap {
	private readonly xs: number[] = [];
	private readonly ys: number[] = [];
	readonly leftEnd: number;
	readonly rightEnd: number;

	constructor(g: Geometry) {
		const bottom = (s: readonly Span[]) => (s.length ? s[s.length - 1]!.top + s[s.length - 1]!.height : 0);
		this.leftEnd = g.leftEnd ?? bottom(g.left);
		this.rightEnd = g.rightEnd ?? bottom(g.right);
		const add = (x: number, y: number) => {
			const n = this.xs.length;
			if (n > 0 && (x <= this.xs[n - 1]! || y <= this.ys[n - 1]!)) return;
			this.xs.push(x);
			this.ys.push(y);
		};
		// An empty buffer has no line to anchor the start: both panes begin at their first content.
		if (g.left.length === 0) add(0, g.right[0]?.top ?? 0);
		g.left.forEach((l, i) => add(l.top, g.right[g.rowOfLine[i]!]!.top));
		add(this.leftEnd, this.rightEnd);
	}

	private eval(from: number[], to: number[], v: number): number {
		const n = from.length;
		if (n === 0) return v;
		if (v <= from[0]!) return to[0]! + (v - from[0]!);
		if (v >= from[n - 1]!) return to[n - 1]! + (v - from[n - 1]!);
		let lo = 0;
		let hi = n - 1;
		while (hi - lo > 1) {
			const mid = (lo + hi) >> 1;
			if (from[mid]! <= v) lo = mid;
			else hi = mid;
		}
		const t = (v - from[lo]!) / (from[hi]! - from[lo]!);
		return to[lo]! + t * (to[hi]! - to[lo]!);
	}

	/** The right-pane y that sits level with left-pane y. */
	toRight(y: number): number {
		return this.eval(this.xs, this.ys, y);
	}

	/** The left-pane y that sits level with right-pane y. A red row lands in the line before its block. */
	toLeft(y: number): number {
		return this.eval(this.ys, this.xs, y);
	}
}

/** Where to put a pane's scroll, and the padding that makes the place reachable. */
export interface Alignment {
	scrollTop: number;
	/** Space to add above and below the content (a scroll cannot go below 0 or past the end). */
	padTop: number;
	padBottom: number;
}

function align(target: number, offset: number, viewport: number, content: number): Alignment {
	const want = target - offset;
	const padTop = Math.max(0, -want);
	const padBottom = Math.max(0, want + viewport - (content + padTop));
	return { scrollTop: want + padTop, padTop, padBottom };
}

/** The right pane's scroll that puts the point level with left-pane `y` at `offset` px below the viewport's top. */
export function alignRight(m: ElasticMap, y: number, offset: number, viewport: number): Alignment {
	return align(m.toRight(y), offset, viewport, m.rightEnd);
}

/** The left pane's scroll that puts the point level with right-pane `y` at `offset` px below the viewport's top. */
export function alignLeft(m: ElasticMap, y: number, offset: number, viewport: number): Alignment {
	return align(m.toLeft(y), offset, viewport, m.leftEnd);
}

/** The point the panes are held level at: `y` in left content coordinates, `offset` px below the viewport's top. */
export interface Anchor {
	y: number;
	offset: number;
}

export interface ViewportInfo {
	scrollTop: number;
	height: number;
}

/** The cursor line's span in left content coordinates, or null with no cursor. */
export type CursorSpan = Span | null;

/** The cursor line's top while the whole line is in view, else the viewport's centre. */
export function anchorOf(cursor: CursorSpan, vp: ViewportInfo): Anchor {
	if (cursor && cursor.top >= vp.scrollTop && cursor.top + cursor.height <= vp.scrollTop + vp.height) {
		return { y: cursor.top, offset: cursor.top - vp.scrollTop };
	}
	return { y: vp.scrollTop + vp.height / 2, offset: vp.height / 2 };
}

export type Side = 'left' | 'right';

/** Scroll events nearer than this to a position the code set are that position's echo. */
const ECHO_PX = 1;

/**
 * Which pane drives. The pane the user last scrolled does; the other is set by code, and the
 * scroll event that setting causes must not be taken for the user: `expect` records the
 * position set, `scrolled` says whether an event is the user's.
 */
export class Driver {
	side: Side = 'left';
	private expected: Record<Side, number | null> = { left: null, right: null };

	get follower(): Side {
		return this.side === 'left' ? 'right' : 'left';
	}

	/** Code set `side`'s scroll to `top`; its next event at that position is the echo. */
	expect(side: Side, top: number): void {
		this.expected[side] = top;
	}

	/**
	 * No scroll event followed the position code set on `side` at `top`: that pending echo is
	 * dropped, so a later user scroll near it is not taken for one. Only that one: a newer
	 * position set since (an easing sets one a frame) keeps its own.
	 */
	release(side: Side, top: number): void {
		if (this.expected[side] === top) this.expected[side] = null;
	}

	/** A scroll event on `side` at `top`. True when it is the user's (which makes `side` the driver), false for an echo. */
	scrolled(side: Side, top: number): boolean {
		const e = this.expected[side];
		this.expected[side] = null;
		if (e !== null && Math.abs(e - top) <= ECHO_PX) return false;
		this.side = side;
		return true;
	}
}
