// The commit graph's lane layout (plan m2n step 3; ADR-0070 §1): pure, no DOM, no Svelte.
//
// One newest-first pass over the commits and their parents. The state between two rows is the
// list of LANES, one per column, each waiting for one commit (the oid it will be drawn at) and
// carrying a colour. A row's incoming lanes are the previous row's outgoing ones. For a commit:
//   - it takes the first (leftmost) lane waiting for it, else the first free column; every other
//     lane waiting for it closes here (drawn converging on the node from above);
//   - its first parent inherits that lane (same column, same colour), even when another lane
//     already waits for that parent: the two close where the parent is drawn;
//   - each further parent joins a lane already waiting for it (a merge-in edge, in that lane's
//     colour), else opens one in the first free column (a branch-out edge, in a new colour);
//   - a commit without parents ends its lane.
// The first free column is the leftmost one no lane holds; trailing free columns are dropped.
// There is no lane cap. A parent not loaded yet keeps its lane waiting, so the lane runs to the
// bottom edge of the last row and continues in the next page.
//
// Geometry for the renderer (one inline SVG per row, the node at mid-height, columns left to
// right): `incoming` lanes run from the top edge to the node, `through` lanes from the top edge
// to the bottom edge in their column, and each `edges` entry from the node to the bottom edge at
// column `to`. `color` is a lane identity, stable for the lane's whole life and increasing as
// lanes open; the renderer takes it modulo its palette.
//
// `extend` lays out only the rows it is given and keeps the lane list, so laying pages out one at
// a time gives the rows laying them all out at once gives. A row never changes once produced.

/** A lane in a column; its colour is the lane's identity. */
export interface Lane {
	column: number;
	color: number;
}

/**
 * An edge from the node to the bottom edge of its row.
 * `straight`: the first parent, in the node's own lane (`from === to`).
 * `branch-out`: a further parent that opens a new lane at `to`, in a new colour.
 * `merge-in`: a further parent whose lane already exists at `to`, in that lane's colour.
 */
export interface LaneEdge {
	kind: 'straight' | 'branch-out' | 'merge-in';
	from: number;
	to: number;
	color: number;
}

export interface LaneRow {
	oid: string;
	/** The node's column. */
	column: number;
	/** The node's colour: the colour of the lane it sits in. */
	color: number;
	/** Lanes waiting for this commit, from the top edge to the node; the node's own lane is one of them unless the commit was not expected. */
	incoming: Lane[];
	/** Lanes passing the row from the top edge to the bottom edge, not touching the node. */
	through: Lane[];
	/** One per parent, in parent order. */
	edges: LaneEdge[];
	/** Columns this row occupies: one past the highest column used on its top or bottom edge or by the node. */
	width: number;
}

export interface LaneInput {
	oid: string;
	parents: readonly string[];
}

interface Slot {
	oid: string;
	color: number;
}

export class Lanes {
	/** One entry per column; null is a free column. Never ends with null. */
	private slots: (Slot | null)[] = [];
	private nextColor = 0;
	private maxWidth = 0;

	/** The widest row so far. */
	get width(): number {
		return this.maxWidth;
	}

	/** Lay out `rows`, newest first, after every row given before. Returns the new rows only. */
	extend(rows: readonly LaneInput[]): LaneRow[] {
		const out: LaneRow[] = new Array(rows.length);
		for (let i = 0; i < rows.length; i++) out[i] = this.step(rows[i]);
		return out;
	}

	private step({ oid, parents }: LaneInput): LaneRow {
		const slots = this.slots;
		const before = slots.length;

		let column = -1;
		const incoming: Lane[] = [];
		const through: Lane[] = [];
		for (let c = 0; c < slots.length; c++) {
			const s = slots[c];
			if (!s) continue;
			if (s.oid === oid) {
				if (column < 0) column = c;
				incoming.push({ column: c, color: s.color });
			} else {
				through.push({ column: c, color: s.color });
			}
		}

		let color: number;
		if (column >= 0) {
			color = slots[column]!.color;
			for (const l of incoming) if (l.column !== column) slots[l.column] = null;
		} else {
			column = this.free();
			color = this.nextColor++;
		}

		const edges: LaneEdge[] = [];
		if (parents.length === 0) {
			slots[column] = null;
		} else {
			slots[column] = { oid: parents[0], color };
			edges.push({ kind: 'straight', from: column, to: column, color });
			for (let p = 1; p < parents.length; p++) {
				const parent = parents[p];
				let at = -1;
				for (let c = 0; c < slots.length; c++) {
					if (slots[c]?.oid === parent) {
						at = c;
						break;
					}
				}
				if (at >= 0) {
					edges.push({ kind: 'merge-in', from: column, to: at, color: slots[at]!.color });
				} else {
					at = this.free();
					const c = this.nextColor++;
					slots[at] = { oid: parent, color: c };
					edges.push({ kind: 'branch-out', from: column, to: at, color: c });
				}
			}
		}

		let width = Math.max(before, column + 1);
		while (slots.length > 0 && slots[slots.length - 1] === null) slots.pop();
		width = Math.max(width, slots.length);
		if (width > this.maxWidth) this.maxWidth = width;
		return { oid, column, color, incoming, through, edges, width };
	}

	/** The first free column, growing the list when every column is held. */
	private free(): number {
		const slots = this.slots;
		for (let c = 0; c < slots.length; c++) if (slots[c] === null) return c;
		slots.push(null);
		return slots.length - 1;
	}
}
