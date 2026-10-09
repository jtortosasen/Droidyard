// The fixed-row virtual list's arithmetic (plan m2a step 5): which rows to render for a
// scroll position. A row has a fixed height, so the window is plain division, and the
// DOM holds the same number of rows at 1,000 lines as at 50,000.

export interface WindowInput {
	scrollTop: number;
	viewportHeight: number;
	rowHeight: number;
	/** Rows rendered beyond each edge of the viewport. */
	overscan: number;
	/** Rows in the list. */
	count: number;
	/** At the bottom, following: the scroll position is the bottom, whatever the last scroll event said. */
	following: boolean;
}

export interface RowWindow {
	/** The first row rendered. */
	start: number;
	/** One past the last row rendered. */
	end: number;
	/** The height of the whole list, to size the scroll area. */
	totalHeight: number;
	/** Where `start` sits, from the top of the scroll area. */
	offset: number;
}

export function rowWindow(w: WindowInput): RowWindow {
	const totalHeight = w.count * w.rowHeight;
	const maxScroll = Math.max(0, totalHeight - w.viewportHeight);
	const top = w.following ? maxScroll : Math.min(Math.max(0, w.scrollTop), maxScroll);
	const first = Math.floor(top / w.rowHeight);
	const last = Math.ceil((top + w.viewportHeight) / w.rowHeight);
	const start = Math.max(0, first - w.overscan);
	const end = Math.min(w.count, last + w.overscan);
	return { start, end: Math.max(start, end), totalHeight, offset: start * w.rowHeight };
}
