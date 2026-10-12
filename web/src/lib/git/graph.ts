// The commit graph's drawing (plan m2n step 4; ADR-0070 §1): the strokes of ONE row's inline
// SVG, from its `LaneRow`. The row is `ROW` tall with the node at mid-height; column c is at
// x = c * COL + COL / 2. Lanes meet at the row edges: a lane that leaves the bottom edge of row n
// at x enters the top edge of row n + 1 at the same x, which is what keeps the graph unbroken
// from one row (and one page) to the next.
//   - `through` lane: a straight line from the top edge to the bottom edge;
//   - `incoming` lane: from the top edge to the node, a straight line in the node's column,
//     else an S curve (vertical at both ends) into the node;
//   - `edges`: from the node to the bottom edge at column `to`, straight when `to` is the
//     node's column, else the same S curve.
import type { LaneRow } from './lanes';

/** Width of a lane column and height of a row, in px. */
export const COL = 14;
export const ROW = 24;
/** The node's radius. */
export const NODE_R = 4;
/** How many lane colours the stylesheet defines (`--lane-0` .. `--lane-7`). */
export const LANE_COLORS = 8;

export interface Stroke {
	/** SVG path data. */
	d: string;
	/** The lane's colour identity (modulo `LANE_COLORS` for the palette). */
	color: number;
}

export const laneX = (column: number): number => column * COL + COL / 2;

/** A lane's colour as a CSS value. */
export const laneColor = (color: number): string => `var(--lane-${color % LANE_COLORS})`;

/** The SVG's width for `columns` lane columns. */
export const graphWidth = (columns: number): number => Math.max(1, columns) * COL;

const MID = ROW / 2;

function curve(x0: number, y0: number, x1: number, y1: number): string {
	const my = (y0 + y1) / 2;
	return `M ${x0} ${y0} C ${x0} ${my} ${x1} ${my} ${x1} ${y1}`;
}

export function strokes(row: LaneRow): Stroke[] {
	const out: Stroke[] = [];
	for (const l of row.through) out.push({ d: `M ${laneX(l.column)} 0 L ${laneX(l.column)} ${ROW}`, color: l.color });
	const nx = laneX(row.column);
	for (const l of row.incoming) {
		const x = laneX(l.column);
		out.push({ d: x === nx ? `M ${x} 0 L ${nx} ${MID}` : curve(x, 0, nx, MID), color: l.color });
	}
	for (const e of row.edges) {
		const x = laneX(e.to);
		out.push({ d: e.to === e.from ? `M ${nx} ${MID} L ${x} ${ROW}` : curve(nx, MID, x, ROW), color: e.color });
	}
	return out;
}
