import { describe, expect, it } from 'vitest';
import { FileTreeModel, fileIcon, type TreeClient } from './tree';
import type { Baseline } from './watch';
import { FakeCalls, settle } from '../build/testing';

type T = 'file' | 'dir';
const e = (path: string, type: T = 'file') => ({ path, type });

function setup(entries: Baseline = [], complete = true) {
	const calls = new FakeCalls();
	const model = new FileTreeModel(calls as unknown as TreeClient, { id: 'w1', root: '/proj/' });
	if (entries.length > 0 || !complete) model.sink.baseline(entries, complete);
	return { calls, model };
}
const labels = (m: FileTreeModel) => m.rows.map((r) => r.label);
const list = (entries: { name: string; type: T }[], truncated = false) => ({ path: '.', entries, truncated });

describe('the file tree rows', () => {
	// Decision: names sort in numeric order and folders come first (plan Q3).
	it('puts file2 before file10 and folders before files', () => {
		const { model } = setup([e('file10'), e('file2'), e('zdir', 'dir'), e('adir/x', 'file'), e('adir', 'dir'), e('b.txt')]);
		expect(labels(model)).toEqual(['adir', 'zdir', 'b.txt', 'file2', 'file10']);
	});

	// Decision (plan Q3): a chain of folders with one folder each is a single row, and
	// expanding that row shows the leaf's content.
	it('compacts single-child folder chains into one row that expands to the leaf', async () => {
		const chain = ['src', 'src/main', 'src/main/java', 'src/main/java/com', 'src/main/java/com/x'];
		const { model } = setup([...chain.map((p) => e(p, 'dir')), e('src/main/java/com/x/A.kt')]);
		expect(labels(model)).toEqual(['src/main/java/com/x']);
		expect(model.rows[0]!.path).toBe('src/main/java/com/x');
		await model.expand('src/main/java/com/x');
		expect(labels(model)).toEqual(['src/main/java/com/x', 'A.kt']);
		expect(model.rows[1]!.depth).toBe(2);
	});

	// Decision: rows are those of expanded folders only, so an event under a collapsed
	// folder changes no row, and one under an expanded folder adds one.
	it('adds a created entry under an expanded folder and none under a collapsed one', async () => {
		const { model } = setup([e('a', 'dir'), e('a/one.kt'), e('b', 'dir'), e('b/two.kt')]);
		await model.expand('a');
		const before = labels(model);
		model.applyChanges([{ path: 'b/three.kt', kind: 'created', type: 'file' }]);
		expect(labels(model)).toEqual(before);
		model.applyChanges([{ path: 'a/zero.kt', kind: 'created', type: 'file' }]);
		expect(labels(model)).toEqual(['a', 'one.kt', 'zero.kt', 'b']);
		// ... and the collapsed folder has it when it is opened.
		await model.expand('b');
		expect(labels(model)).toContain('three.kt');
	});

	// Decision: deleting the selected row selects its neighbour, not nothing.
	it('selects the neighbour of a deleted selected row', async () => {
		const { model } = setup([e('a.kt'), e('b.kt'), e('c.kt')]);
		model.select('b.kt');
		model.applyChanges([{ path: 'b.kt', kind: 'deleted', type: 'file' }]);
		expect(model.view.selected).toBe('c.kt');
		model.applyChanges([{ path: 'c.kt', kind: 'deleted', type: 'file' }]);
		expect(model.view.selected).toBe('a.kt');
	});

	// Decision (D28): a folder holding build.gradle(.kts) is marked, with no trust needed.
	it('marks module folders by their Gradle file', () => {
		const { model } = setup([e('app', 'dir'), e('app/build.gradle.kts'), e('docs', 'dir'), e('docs/a.md')]);
		expect(model.rows.map((r) => [r.label, r.module, r.icon])).toEqual([
			['app', true, 'module'],
			['docs', false, 'folder'],
		]);
	});

	it('picks an icon by type and place', () => {
		expect(fileIcon('a/B.kt')).toBe('code');
		expect(fileIcon('x.json')).toBe('json');
		expect(fileIcon('app/src/main/res/drawable/ic.xml')).toBe('image');
		expect(fileIcon('app/src/main/AndroidManifest.xml')).toBe('text');
		expect(fileIcon('LICENSE')).toBe('file');
	});

	// Decision: the filter keeps the folders above a match, open, and drops the rest.
	it('keeps the ancestors of a filter match', () => {
		const { model } = setup([e('a', 'dir'), e('a/x', 'dir'), e('a/x/Main.kt'), e('a/x/Other.kt'), e('b', 'dir'), e('b/n.txt'), e('c.kt')]);
		model.setFilter('main');
		expect(labels(model)).toEqual(['a/x', 'Main.kt']);
		model.setFilter('');
		expect(labels(model)).toEqual(['a/x', 'b', 'c.kt']);
	});

	// Decision (D26/Q3): relative and absolute paths for Copy path.
	it('gives relative and absolute paths without doubling the slash', () => {
		const { model } = setup([e('a.kt')]);
		expect(model.relativePath('a/b.kt')).toBe('a/b.kt');
		expect(model.absolutePath('a/b.kt')).toBe('/proj/a/b.kt');
	});

	// Decision: 10,000 entries become rows in well under 50 ms.
	it('builds rows for 10,000 entries in under 50 ms', async () => {
		const entries: Baseline = [];
		for (let d = 0; d < 100; d++) {
			entries.push(e(`d${d}`, 'dir'));
			for (let f = 0; f < 99; f++) entries.push(e(`d${d}/f${f}.kt`));
		}
		const { model } = setup(entries);
		for (let d = 0; d < 100; d++) await model.expand(`d${d}`);
		const t = performance.now();
		model.setFilter('f9'); // forces a rebuild of every open folder
		model.setFilter('');
		const ms = performance.now() - t;
		expect(model.rows.length).toBe(100 + 100 * 99);
		expect(ms).toBeLessThan(50);
	});
});

describe('keyboard', () => {
	async function tree() {
		const t = setup([e('app', 'dir'), e('app/main.kt'), e('app/map.kt'), e('lib', 'dir'), e('lib/z.kt'), e('readme.md'), e('mod.txt')]);
		t.model.select('app');
		return t;
	}

	// Decision: Right opens a folder, then steps into it; Left closes it, then goes to the parent.
	it('moves with Right and Left', async () => {
		const { model } = await tree();
		model.key('ArrowRight');
		expect(model.rows.find((r) => r.path === 'app')!.expanded).toBe(true);
		model.key('ArrowRight');
		expect(model.view.selected).toBe('app/main.kt');
		model.key('ArrowLeft');
		expect(model.view.selected).toBe('app');
		model.key('ArrowLeft');
		expect(model.rows.find((r) => r.path === 'app')!.expanded).toBe(false);
	});

	it('goes to the first and last row with Home and End', async () => {
		const { model } = await tree();
		model.key('End');
		expect(model.view.selected).toBe('readme.md');
		model.key('Home');
		expect(model.view.selected).toBe('app');
	});

	// Decision: printable keys are type-ahead; typed quickly they make one text ("ma").
	it('jumps by type-ahead and forgets the text after a pause', async () => {
		const { model } = await tree();
		model.key('ArrowRight');
		model.key('m', 1000);
		expect(model.view.selected).toBe('app/main.kt');
		model.key('a', 1100);
		expect(model.view.selected).toBe('app/main.kt');
		model.key('p', 1200); // "map" narrows from the current row
		expect(model.view.selected).toBe('app/map.kt');
		model.key('r', 3000); // a pause: new text "r"
		expect(model.view.selected).toBe('readme.md');
	});

	it('opens a file with Enter and toggles a folder', async () => {
		const { model } = await tree();
		expect(model.key('Enter')).toEqual({ handled: true });
		expect(model.rows.find((r) => r.path === 'app')!.expanded).toBe(true);
		model.select('readme.md');
		expect(model.key('Enter')).toEqual({ handled: true, open: 'readme.md' });
	});
});

describe('listing', () => {
	// Decision: with an incomplete baseline the children come from file.list, on expand,
	// and the events are applied to listed folders only.
	it('lists the root and a folder on expand when the baseline was truncated', async () => {
		const { calls, model } = setup([], false);
		calls.on('file.list', (p) => {
			const path = (p as { path?: string }).path;
			return path === undefined ? list([{ name: 'a', type: 'dir' }, { name: 'r.txt', type: 'file' }]) : list([{ name: 'x.kt', type: 'file' }, { name: 'y', type: 'dir' }]);
		});
		model.sink.baseline([], false);
		await settle();
		expect(labels(model)).toEqual(['a', 'r.txt']);
		await model.expand('a');
		expect(calls.calls.filter((c) => c.name === 'file.list').map((c) => (c.params as { path?: string }).path)).toContain('a');
		expect(labels(model)).toEqual(['a', 'y', 'x.kt', 'r.txt']);
		// A change in a folder never listed is not applied; one in a listed folder is.
		model.applyChanges([{ path: 'a/y/deep.kt', kind: 'created', type: 'file' }]);
		model.applyChanges([{ path: 'a/new.kt', kind: 'created', type: 'file' }]);
		expect(labels(model)).toEqual(['a', 'y', 'new.kt', 'x.kt', 'r.txt']);
	});

	// Decision: a cut event of an incomplete baseline re-lists the open folders.
	it('lists the open folders again on a cut', async () => {
		const { calls, model } = setup([], false);
		calls.on('file.list', (p) =>
			(p as { path?: string }).path === undefined ? list([{ name: 'a', type: 'dir' }, { name: 'b', type: 'dir' }]) : list([{ name: 'f.kt', type: 'file' }]),
		);
		model.sink.baseline([], false);
		await settle();
		await model.expand('a');
		const n = calls.calls.length;
		model.sink.cut();
		await settle();
		expect(calls.calls.slice(n).map((c) => (c.params as { path?: string }).path)).toEqual([undefined, 'a']);
	});

	// Decision (D27): hidden entries are what file.list returns and the complete baseline
	// lacks; the toggle shows them for the expanded folders and takes them away again.
	it('shows the entries file.list has beyond the baseline when the toggle is on', async () => {
		const { calls, model } = setup([e('app', 'dir'), e('app/a.kt')]);
		calls.on('file.list', (p) => {
			const path = (p as { path?: string }).path;
			return path === 'app'
				? list([{ name: 'a.kt', type: 'file' }, { name: 'build', type: 'dir' }])
				: list([{ name: 'app', type: 'dir' }, { name: '.git', type: 'dir' }]);
		});
		await model.expand('app');
		expect(labels(model)).toEqual(['app', 'a.kt']);
		expect(calls.calls).toEqual([]); // nothing is listed while the toggle is off
		model.setShowHidden(true);
		await settle();
		expect(labels(model)).toEqual(['.git', 'app', 'build', 'a.kt']);
		model.setShowHidden(false);
		expect(labels(model)).toEqual(['app', 'a.kt']);
	});

	it('reveals a file by opening the folders above it and selecting it', async () => {
		const { model } = setup([e('a', 'dir'), e('a/b', 'dir'), e('a/b/c.kt'), e('a/d.kt')]);
		await model.reveal('a/b/c.kt');
		expect(model.view.selected).toBe('a/b/c.kt');
		expect(labels(model)).toContain('c.kt');
	});
});
