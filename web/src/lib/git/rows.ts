// The visible window of the History table (plan m2n step 4): a virtualised table of fixed row
// height, pure arithmetic like logcat/virtual.ts, with room for ONE taller row (the expanded
// commit of step 8): the row at `expanded.index` is `rowHeight + extra` tall and pushes every
// row after it down by `extra`. The DOM holds the same number of rows at 500 commits as at
// 100,000.

export interface Expanded {
	/** The row that is taller. */
	index: number;
	/** How much taller than `rowHeight`, in px. */
	extra: number;
}

export interface WindowInput {
	scrollTop: number;
	viewportHeight: number;
	rowHeight: number;
	/** Rows rendered beyond each edge of the viewport. */
	overscan: number;
	/** Rows in the table. */
	count: number;
	expanded?: Expanded | null;
}

export interface RowWindow {
	/** The first row rendered. */
	start: number;
	/** One past the last row rendered. */
	end: number;
	/** The height of the whole table, to size the scroll area. */
	totalHeight: number;
	/** Where `start` sits, from the top of the scroll area. */
	offset: number;
}

function extraOf(w: { count: number; expanded?: Expanded | null }): number {
	const e = w.expanded;
	return e && e.index >= 0 && e.index < w.count && e.extra > 0 ? e.extra : 0;
}

/** Where row `index` starts, from the top of the scroll area. */
export function rowTop(index: number, rowHeight: number, expanded?: Expanded | null): number {
	const e = expanded;
	return index * rowHeight + (e && e.extra > 0 && index > e.index ? e.extra : 0);
}

/** The row that contains the vertical position `y` (clamped to the rows). */
export function rowAt(y: number, w: Pick<WindowInput, 'rowHeight' | 'count' | 'expanded'>): number {
	if (w.count <= 0) return 0;
	const extra = extraOf(w);
	let i: number;
	if (extra > 0 && y < (w.expanded!.index + 1) * w.rowHeight + extra) {
		i = Math.min(Math.floor(y / w.rowHeight), w.expanded!.index);
	} else {
		i = Math.floor((y - extra) / w.rowHeight);
	}
	return Math.min(Math.max(0, i), w.count - 1);
}

export function rowWindow(w: WindowInput): RowWindow {
	if (w.count <= 0) return { start: 0, end: 0, totalHeight: 0, offset: 0 };
	const extra = extraOf(w);
	const totalHeight = w.count * w.rowHeight + extra;
	const maxScroll = Math.max(0, totalHeight - w.viewportHeight);
	const top = Math.min(Math.max(0, w.scrollTop), maxScroll);
	const first = rowAt(top, w);
	const last = rowAt(Math.max(top, top + w.viewportHeight - 0.001), w) + 1;
	const start = Math.max(0, first - w.overscan);
	const end = Math.min(w.count, last + w.overscan);
	return { start, end: Math.max(start, end), totalHeight, offset: rowTop(start, w.rowHeight, w.expanded) };
}
