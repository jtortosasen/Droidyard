// The shell's remembered layout (plan m2d D25): the sizes of the left, right and bottom
// regions, which are open, and the density, kept per browser in localStorage under
// `droidyard.ui.`. Storage is a convenience, never a dependency: a read or a write that
// throws (a private window, blocked site data) leaves the defaults and no exception, and
// every stored value is clamped on the way in, so a hand-edited or stale entry cannot
// produce a region that is off the screen or unusable.

export type Region = 'left' | 'right' | 'bottom';
export type Density = 'compact' | 'comfortable';

export interface Layout {
	/** Pixel sizes: width of the left and right panels, height of the bottom one. */
	size: Record<Region, number>;
	open: Record<Region, boolean>;
	density: Density;
	sessions: Sessions;
}

/**
 * The sessions column (plan m2e D33, ADR-0050): `collapsedRight` records that opening the
 * column is what collapsed the right region, so closing restores it only then.
 */
export interface Sessions {
	open: boolean;
	size: number;
	collapsedRight: boolean;
}

/** The sessions column's width bounds in pixels (D33). */
export const SESSIONS_BOUNDS = { min: 220, max: 420 };

/** The inclusive bounds of each region's size in pixels. */
export const BOUNDS: Record<Region, { min: number; max: number }> = {
	left: { min: 160, max: 480 },
	right: { min: 240, max: 640 },
	bottom: { min: 120, max: 600 },
};

export const DEFAULT_LAYOUT: Layout = {
	size: { left: 260, right: 340, bottom: 260 },
	open: { left: true, right: true, bottom: true },
	density: 'compact',
	sessions: { open: false, size: 280, collapsedRight: false },
};

export const STORAGE_KEY = 'droidyard.ui.layout';

/** The slice of Storage the layout uses, so a test passes a fake. */
export interface KeyValueStorage {
	getItem(key: string): string | null;
	setItem(key: string, value: string): void;
}

/** A size inside its region's bounds; anything that is not a finite number is the default. */
export function clampSize(region: Region, value: unknown): number {
	const { min, max } = BOUNDS[region];
	if (typeof value !== 'number' || !Number.isFinite(value)) return DEFAULT_LAYOUT.size[region];
	return Math.min(max, Math.max(min, Math.round(value)));
}

/** The sessions column's width inside its bounds; a non-number is the default. */
export function clampSessionsSize(value: unknown): number {
	if (typeof value !== 'number' || !Number.isFinite(value)) return DEFAULT_LAYOUT.sessions.size;
	return Math.min(SESSIONS_BOUNDS.max, Math.max(SESSIONS_BOUNDS.min, Math.round(value)));
}

/**
 * Opens the sessions column. An open right region is collapsed and the layout records that
 * this did it; a right region already collapsed is left alone and not recorded (D33).
 */
export function openSessions(l: Layout): Layout {
	if (l.sessions.open) return l;
	const collapse = l.open.right;
	return {
		...l,
		open: { ...l.open, right: false },
		sessions: { ...l.sessions, open: true, collapsedRight: collapse },
	};
}

/** Closes the column; the right region comes back only while the record stands (D33). */
export function closeSessions(l: Layout): Layout {
	if (!l.sessions.open) return l;
	return {
		...l,
		open: { ...l.open, right: l.sessions.collapsedRight ? true : l.open.right },
		sessions: { ...l.sessions, open: false, collapsedRight: false },
	};
}

/** A user's minimize or restore of a region; on the right it clears the record (D33). */
export function setRegion(l: Layout, region: Region, open: boolean): Layout {
	return {
		...l,
		open: { ...l.open, [region]: open },
		sessions: region === 'right' ? { ...l.sessions, collapsedRight: false } : l.sessions,
	};
}

const REGIONS: readonly Region[] = ['left', 'right', 'bottom'];

/** The layout stored in `storage`, or the defaults; never throws. */
export function loadLayout(storage: KeyValueStorage | null): Layout {
	const layout: Layout = structuredClone(DEFAULT_LAYOUT);
	if (storage === null) return layout;
	let parsed: unknown;
	try {
		const raw = storage.getItem(STORAGE_KEY);
		if (raw === null) return layout;
		parsed = JSON.parse(raw);
	} catch {
		return layout;
	}
	if (typeof parsed !== 'object' || parsed === null) return layout;
	const p = parsed as { size?: Record<string, unknown>; open?: Record<string, unknown>; density?: unknown; sessions?: unknown };
	for (const r of REGIONS) {
		layout.size[r] = clampSize(r, p.size?.[r]);
		const o = p.open?.[r];
		if (typeof o === 'boolean') layout.open[r] = o;
	}
	const s = p.sessions;
	if (typeof s === 'object' && s !== null) {
		const q = s as Record<string, unknown>;
		layout.sessions.size = clampSessionsSize(q.size);
		if (typeof q.open === 'boolean') layout.sessions.open = q.open;
		// The record means something only while the column is open.
		if (typeof q.collapsedRight === 'boolean') layout.sessions.collapsedRight = q.collapsedRight && layout.sessions.open;
	}
	if (p.density === 'compact' || p.density === 'comfortable') layout.density = p.density;
	return layout;
}

/** Stores the layout; a storage that refuses is ignored. */
export function saveLayout(storage: KeyValueStorage | null, layout: Layout): void {
	if (storage === null) return;
	try {
		storage.setItem(STORAGE_KEY, JSON.stringify(layout));
	} catch {
		// Not remembered; the layout in memory is still right.
	}
}

/** The browser's localStorage, or null when even reaching it throws. */
export function browserStorage(win: Window): KeyValueStorage | null {
	try {
		return win.localStorage;
	} catch {
		return null;
	}
}
