import { describe, expect, it } from 'vitest';
import type { KeyValueStorage } from '../ui/layout';
import { activate, initialActive } from '../ui/panels';
import { MAX_TABS, STORAGE_PREFIX, TabsModel } from './tabs';

class Mem implements KeyValueStorage {
	data = new Map<string, string>();
	getItem(k: string) {
		return this.data.get(k) ?? null;
	}
	setItem(k: string, v: string) {
		this.data.set(k, v);
	}
}

const yes = () => Promise.resolve(true);

describe('TabsModel', () => {
	// Decision: a single click is a preview tab and the next single click replaces it.
	it('replaces the preview tab with the next preview', () => {
		const t = new TabsModel(null, 'w1');
		t.open('a.kt');
		t.open('b.kt');
		expect(t.view.tabs).toEqual([{ path: 'b.kt', preview: true }]);
		expect(t.view.active).toBe('b.kt');
	});

	// Decision: opening the preview again, with no edit between, makes it permanent;
	// a permanent tab is not replaced.
	it('keeps a preview opened a second time, and does not replace a kept tab', () => {
		const t = new TabsModel(null, 'w1');
		t.open('a.kt');
		t.open('a.kt');
		expect(t.view.tabs).toEqual([{ path: 'a.kt', preview: false }]);
		t.open('b.kt');
		t.open('c.kt');
		expect(t.view.tabs.map((x) => x.path)).toEqual(['a.kt', 'c.kt']);
	});

	it('focuses an open file instead of duplicating it', () => {
		const t = new TabsModel(null, 'w1');
		t.open('a.kt');
		t.keep('a.kt');
		t.open('b.kt');
		t.keep('b.kt');
		t.activate('a.kt');
		t.open('b.kt');
		expect(t.view.tabs).toHaveLength(2);
		expect(t.view.active).toBe('b.kt');
	});

	it('activates the neighbour when the active tab closes', () => {
		const t = new TabsModel(null, 'w1');
		for (const p of ['a', 'b', 'c']) {
			t.open(p);
			t.keep(p);
		}
		t.activate('b');
		expect(t.close('b')).toBe('c');
		expect(t.close('c')).toBe('a');
		expect(t.close('a')).toBeNull();
		expect(t.view.tabs).toEqual([]);
	});

	// Decision (D25): the tabs are remembered per project, and a path that vanished is
	// dropped on restore.
	it('restore drops a path that no longer exists and keeps the others', async () => {
		const s = new Mem();
		const first = new TabsModel(s, 'w1');
		await first.restore(yes);
		for (const p of ['gone.kt', 'here.kt']) {
			first.open(p);
			first.keep(p);
		}
		first.activate('gone.kt');
		const second = new TabsModel(s, 'w1');
		await second.restore((p) => Promise.resolve(p !== 'gone.kt'));
		expect(second.view.tabs.map((x) => x.path)).toEqual(['here.kt']);
		expect(second.view.active).toBe('here.kt');
		// Another project's tabs are its own.
		const other = new TabsModel(s, 'w2');
		await other.restore(yes);
		expect(other.view.tabs).toEqual([]);
	});

	// Decision: nothing is written before the restore ran, so a slow start cannot erase what
	// the last session left.
	it('writes nothing before the restore', async () => {
		const s = new Mem();
		s.setItem(STORAGE_PREFIX + 'w1', JSON.stringify({ tabs: [{ path: 'a.kt', preview: false }], active: 'a.kt' }));
		const t = new TabsModel(s, 'w1');
		t.open('b.kt');
		expect(JSON.parse(s.getItem(STORAGE_PREFIX + 'w1')!).tabs.map((x: { path: string }) => x.path)).toEqual(['a.kt']);
		await t.restore(yes);
		expect(t.view.tabs.map((x) => x.path)).toEqual(['a.kt', 'b.kt']);
	});

	// Decision (D57): a rename re-points the file's tab and the tabs under a moved folder by
	// path prefix, never a sibling that shares the prefix (src/ab for src/a).
	it('moved re-points a file, a folder\'s tabs and the active one, not a prefix sibling', () => {
		const t = new TabsModel(null, 'w1');
		for (const p of ['src/a/X.kt', 'src/a/deep/Y.kt', 'src/ab/Z.kt', 'src/a']) {
			t.open(p);
			t.keep(p);
		}
		t.activate('src/a/deep/Y.kt');
		t.moved('src/a', 'src/c');
		expect(t.view.tabs.map((x) => x.path)).toEqual(['src/c/X.kt', 'src/c/deep/Y.kt', 'src/ab/Z.kt', 'src/c']);
		expect(t.view.active).toBe('src/c/deep/Y.kt');
		t.moved('src/ab/Z.kt', 'src/ab/W.kt');
		expect(t.view.tabs.map((x) => x.path)).toContain('src/ab/W.kt');
	});

	// Decision (m2i D7): a tab with unsaved changes has a mark; the first edit makes a preview
	// tab permanent; the mark is never stored; closing the tab clears it.
	it('marks a tab dirty, keeps a preview on the first edit, and does not store the mark', async () => {
		const s = new Mem();
		const t = new TabsModel(s, 'w1');
		await t.restore(yes);
		t.open('a.kt');
		expect(t.view.tabs).toEqual([{ path: 'a.kt', preview: true }]);
		t.setDirty('a.kt', true);
		expect(t.view.tabs).toEqual([{ path: 'a.kt', preview: false, dirty: true }]);
		// A new preview does not replace it any more.
		t.open('b.kt');
		expect(t.view.tabs.map((x) => x.path)).toEqual(['a.kt', 'b.kt']);
		expect(s.data.get(STORAGE_PREFIX + 'w1')).not.toContain('dirty');
		t.setDirty('a.kt', false);
		expect(t.view.tabs[0]).toEqual({ path: 'a.kt', preview: false });
		t.setDirty('a.kt', true);
		t.close('a.kt');
		expect(t.isDirty('a.kt')).toBe(false);
		t.setDirty('gone.kt', true); // no tab: no mark
		expect(t.isDirty('gone.kt')).toBe(false);
	});

	// Decision (m2i D7, review N-B1): a dirty tab follows its file through a move and is never
	// the one dropped when two tabs meet at a target.
	it('moved keeps the dirty mark with its tab, and a dirty tab at a collision', () => {
		const t = new TabsModel(null, 'w1');
		for (const p of ['dir/a.kt', 'dir/b.kt', 'other.kt']) {
			t.open(p);
			t.keep(p);
		}
		t.setDirty('dir/a.kt', true);
		t.moved('dir', 'moved');
		expect(t.view.tabs.map((x) => `${x.path}${x.dirty ? '*' : ''}`)).toEqual(['moved/a.kt*', 'moved/b.kt', 'other.kt']);

		// A clean tab at the target is replaced by the moved one: the dirty one wins.
		t.moved('moved/a.kt', 'moved/b.kt');
		expect(t.view.tabs.map((x) => `${x.path}${x.dirty ? '*' : ''}`)).toEqual(['moved/b.kt*', 'other.kt']);

		// A dirty tab at the target stays; the clean moved tab goes.
		t.open('c.kt');
		t.keep('c.kt');
		t.moved('c.kt', 'moved/b.kt');
		expect(t.view.tabs.map((x) => `${x.path}${x.dirty ? '*' : ''}`)).toEqual(['moved/b.kt*', 'other.kt']);
	});

	// Decision (m2i D7, verifier): the safety net never drops a dirty mark. Two dirty tabs meeting at
	// a target (FileOps refuses that move, so this is the net under it) both stay, marks included.
	it('moved keeps both tabs and both marks when two dirty tabs meet', () => {
		const t = new TabsModel(null, 'w1');
		for (const p of ['a.kt', 'b.kt']) {
			t.open(p);
			t.keep(p);
			t.setDirty(p, true);
		}
		t.activate('a.kt');
		t.moved('a.kt', 'b.kt');
		expect(t.view.tabs.map((x) => `${x.path}${x.dirty ? '*' : ''}`)).toEqual(['a.kt*', 'b.kt*']);
		expect(t.view.active).toBe('a.kt');
	});

	it('moved replaces a clean tab at the target by the moved one', () => {
		const t = new TabsModel(null, 'w1');
		for (const p of ['a.kt', 'gone.kt', 'z.kt']) {
			t.open(p);
			t.keep(p);
		}
		t.activate('a.kt');
		t.moved('a.kt', 'gone.kt');
		expect(t.view.tabs.map((x) => x.path)).toEqual(['gone.kt', 'z.kt']);
		expect(t.view.active).toBe('gone.kt');
	});

	// Decision (D25): storage is a convenience: a refusing or garbage storage changes nothing.
	it('survives a storage that throws or holds garbage', async () => {
		const throwing: KeyValueStorage = {
			getItem() {
				throw new Error('blocked');
			},
			setItem() {
				throw new Error('blocked');
			},
		};
		const t = new TabsModel(throwing, 'w1');
		await t.restore(yes);
		t.open('a.kt');
		expect(t.view.tabs).toHaveLength(1);
		const s = new Mem();
		s.setItem(STORAGE_PREFIX + 'w1', JSON.stringify({ tabs: [{ path: 5 }, null, { path: '' }, { path: 'x'.repeat(5000) }, { path: 'ok.kt' }, { path: 'ok.kt' }], active: 7 }));
		const g = new TabsModel(s, 'w1');
		await g.restore(yes);
		expect(g.view.tabs.map((x) => x.path)).toEqual(['ok.kt']);
		const many = new Mem();
		many.setItem(STORAGE_PREFIX + 'w1', JSON.stringify({ tabs: Array.from({ length: 500 }, (_, i) => ({ path: `f${i}` })) }));
		const m = new TabsModel(many, 'w1');
		await m.restore(yes);
		expect(m.view.tabs).toHaveLength(MAX_TABS);
	});
});

describe('first-level tabs', () => {
	// Decision (plan step 6): Terminal is disabled in the registry and cannot be made the active
	// centre tab, so no file ever opens in it. Plan m2l step 5: the Diff tab can be activated (a Git row shows it).
	it('cannot activate Terminal, can activate Diff', () => {
		const a = initialActive();
		expect(a.centre).toBe('editor');
		expect(activate(a, 'terminal').centre).toBe('editor');
		expect(activate(a, 'diff').centre).toBe('diff');
	});
});
