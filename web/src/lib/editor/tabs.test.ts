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
	// Decision (plan step 6): Terminal and Diff are disabled in the registry and cannot be
	// made the active centre tab, so no file ever opens in them.
	it('cannot activate Terminal or Diff', () => {
		const a = initialActive();
		expect(a.centre).toBe('editor');
		expect(activate(a, 'terminal').centre).toBe('editor');
		expect(activate(a, 'diff').centre).toBe('editor');
	});
});
