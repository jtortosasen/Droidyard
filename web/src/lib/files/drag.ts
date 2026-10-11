// The tree's drag (plan m2g step 6; D53) as pure functions: the pointer events, the
// listeners and the ghost are FileTree.svelte's. Time is passed in, never read.
import { parentOf } from './ops';

/** Pixels the pointer must travel with the button down before a drag starts. */
export const DRAG_THRESHOLD = 4;
/** A collapsed folder under the pointer opens after this long. */
export const EXPAND_AFTER_MS = 600;
/** The list scrolls when the pointer is this close to its top or bottom. */
export const EDGE_PX = 24;

export interface Point {
	x: number;
	y: number;
}

/** Whether the pointer has moved far enough from where the primary button went down. */
export function dragStarts(down: Point, now: Point, buttons: number): boolean {
	if ((buttons & 1) === 0) return false;
	return Math.hypot(now.x - down.x, now.y - down.y) >= DRAG_THRESHOLD;
}

export interface DragRow {
	path: string;
	dir: boolean;
}

/**
 * The folder a drop on `row` ('' is the root: pass null for the empty area) would go to, or
 * null when it is no drop: a folder into itself or a descendant, or everything dragged already
 * in that folder.
 */
export function dropTarget(row: DragRow | null, sources: readonly string[]): string | null {
	const target = row === null ? '' : row.dir ? row.path : parentOf(row.path);
	if (sources.length === 0) return null;
	for (const s of sources) if (target === s || target.startsWith(`${s}/`)) return null;
	if (sources.every((s) => parentOf(s) === target)) return null;
	return target;
}

/** ⌥ held at the drop copies; otherwise it moves. */
export function dropMode(altKey: boolean): 'move' | 'copy' {
	return altKey ? 'copy' : 'move';
}

export interface Hover {
	path: string;
	since: number;
	fired: boolean;
}

/**
 * One step of the auto-expand timer: `over` is the collapsed folder under the pointer (null
 * for anything else). Returns the new state and whether to expand `over` now (once per stay).
 */
export function hoverStep(state: Hover | null, over: string | null, now: number): { state: Hover | null; expand: boolean } {
	if (over === null) return { state: null, expand: false };
	const h = state !== null && state.path === over ? state : { path: over, since: now, fired: false };
	if (!h.fired && now - h.since >= EXPAND_AFTER_MS) return { state: { ...h, fired: true }, expand: true };
	return { state: h, expand: false };
}

/** Pixels to scroll per tick (negative is up): 0 away from the edges, faster the closer to one. */
export function edgeScroll(y: number, top: number, bottom: number): number {
	if (y < top + EDGE_PX) return -Math.ceil((Math.min(EDGE_PX, top + EDGE_PX - y) / EDGE_PX) * 12);
	if (y > bottom - EDGE_PX) return Math.ceil((Math.min(EDGE_PX, y - (bottom - EDGE_PX)) / EDGE_PX) * 12);
	return 0;
}
