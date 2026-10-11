import { describe, expect, it } from 'vitest';
import {
	BOUNDS,
	DEFAULT_LAYOUT,
	SESSIONS_BOUNDS,
	STORAGE_KEY,
	clampSessionsSize,
	clampSize,
	closeSessions,
	loadLayout,
	openSessions,
	saveLayout,
	setRegion,
	type KeyValueStorage,
	type Layout,
} from './layout';

function memory(initial?: string): KeyValueStorage & { data: Map<string, string> } {
	const data = new Map<string, string>();
	if (initial !== undefined) data.set(STORAGE_KEY, initial);
	return {
		data,
		getItem: (k) => data.get(k) ?? null,
		setItem: (k, v) => void data.set(k, v),
	};
}

describe('layout', () => {
	// Decision (D25): a stored size is clamped on the way in, so a stale or hand-edited
	// entry cannot make a region unusable.
	it('clamps a size outside its bounds and replaces a non-number with the default', () => {
		expect(clampSize('left', 5)).toBe(BOUNDS.left.min);
		expect(clampSize('left', 99999)).toBe(BOUNDS.left.max);
		expect(clampSize('bottom', 'wide')).toBe(DEFAULT_LAYOUT.size.bottom);
		expect(clampSize('right', Number.NaN)).toBe(DEFAULT_LAYOUT.size.right);
		const l = loadLayout(memory(JSON.stringify({ size: { left: 1, right: 1e9, bottom: 300 } })));
		expect(l.size).toEqual({ left: BOUNDS.left.min, right: BOUNDS.right.max, bottom: 300 });
	});

	// Decision (D25): sizes, open state and density are restored as they were saved.
	it('restores what it saved', () => {
		const s = memory();
		const layout = {
			size: { left: 300, right: 400, bottom: 200 },
			open: { left: false, right: true, bottom: false },
			density: 'comfortable' as const,
			sessions: { open: true, size: 300, collapsedRight: true },
		};
		saveLayout(s, layout);
		expect(loadLayout(s)).toEqual(layout);
	});

	// Decision (D25): storage is a convenience; a storage that throws on read and on write
	// leaves the defaults and no exception, and so does corrupt stored text.
	it('survives a storage that throws and one that holds garbage', () => {
		const throwing: KeyValueStorage = {
			getItem: () => {
				throw new Error('blocked');
			},
			setItem: () => {
				throw new Error('blocked');
			},
		};
		expect(loadLayout(throwing)).toEqual(DEFAULT_LAYOUT);
		expect(() => saveLayout(throwing, DEFAULT_LAYOUT)).not.toThrow();
		expect(loadLayout(null)).toEqual(DEFAULT_LAYOUT);
		expect(loadLayout(memory('{not json'))).toEqual(DEFAULT_LAYOUT);
		expect(loadLayout(memory('"text"'))).toEqual(DEFAULT_LAYOUT);
		expect(loadLayout(memory(JSON.stringify({ density: 'huge', open: { left: 'yes' } })))).toEqual(DEFAULT_LAYOUT);
	});

	it('does not hand out the shared defaults to be mutated', () => {
		const a = loadLayout(null);
		a.size.left = 1;
		expect(loadLayout(null).size.left).toBe(DEFAULT_LAYOUT.size.left);
	});

	const base = (): Layout => structuredClone(DEFAULT_LAYOUT);

	// Decision (D33): opening the column collapses an open right region and records that it
	// did; the column starts closed and the right region open.
	it('opening the sessions column collapses the open right region and records it', () => {
		const l = openSessions(base());
		expect(l.sessions.open).toBe(true);
		expect(l.open.right).toBe(false);
		expect(l.sessions.collapsedRight).toBe(true);
	});

	// Decision (D33): closing restores the right region when the record stands.
	it('closing restores the right region it collapsed and drops the record', () => {
		const l = closeSessions(openSessions(base()));
		expect(l.sessions.open).toBe(false);
		expect(l.open.right).toBe(true);
		expect(l.sessions.collapsedRight).toBe(false);
	});

	// Decision (D33, ADR-0050 Alternatives): a right region the user collapsed before
	// stays collapsed after the column opens and closes ("always restore" is rejected).
	it('a right region collapsed by the user stays collapsed through open and close', () => {
		const user = setRegion(base(), 'right', false);
		const opened = openSessions(user);
		expect(opened.sessions.collapsedRight).toBe(false);
		expect(closeSessions(opened).open.right).toBe(false);
	});

	// Decision (D33): any user action on the right region while the column is open clears
	// the record, so closing no longer overrides it.
	it('a minimize or restore of the right region while the column is open clears the record', () => {
		const restored = setRegion(openSessions(base()), 'right', true);
		expect(restored.sessions.collapsedRight).toBe(false);
		expect(closeSessions(restored).open.right).toBe(true);
		// The user collapses it again, then closes the column: it stays collapsed.
		const again = setRegion(restored, 'right', false);
		expect(closeSessions(again).open.right).toBe(false);
		// A minimize of the left region does not touch the record.
		expect(setRegion(openSessions(base()), 'left', false).sessions.collapsedRight).toBe(true);
	});

	// Decision (D33): the transitions are pure; they never mutate the layout they are given.
	it('does not mutate its input', () => {
		const l = base();
		openSessions(l);
		setRegion(l, 'right', false);
		expect(l).toEqual(DEFAULT_LAYOUT);
	});

	// Decision (D33, D25): the column's width is clamped to 220-420 and stored with the rest;
	// a stored record without an open column is dropped, so it cannot restore a stale state.
	it('clamps and restores the sessions column, and drops a record without an open column', () => {
		expect(clampSessionsSize(1)).toBe(SESSIONS_BOUNDS.min);
		expect(clampSessionsSize(9999)).toBe(SESSIONS_BOUNDS.max);
		expect(clampSessionsSize('x')).toBe(DEFAULT_LAYOUT.sessions.size);
		const s = loadLayout(memory(JSON.stringify({ sessions: { open: true, size: 5, collapsedRight: true } })));
		expect(s.sessions).toEqual({ open: true, size: SESSIONS_BOUNDS.min, collapsedRight: true });
		const stale = loadLayout(memory(JSON.stringify({ sessions: { open: false, size: 300, collapsedRight: true } })));
		expect(stale.sessions.collapsedRight).toBe(false);
	});
});
