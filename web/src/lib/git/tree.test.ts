import { describe, expect, it } from 'vitest';
import type { BusEvent } from '../api/client';
import { FakeCalls, FakeStream, settle } from '../build/testing';
import { FileTreeModel, type TreeClient } from '../files/tree';
import { FileWatch, type WatchClient } from '../files/watch';
import { FakeFrames } from '../logcat/testing';
import type { KeyValueStorage } from '../ui/layout';
import type { Entry } from './changes';
import { DEFAULT_PREFS, GitTreeModel, loadPrefs, PREFS_PREFIX, savePrefs } from './tree';

const f = (path: string) => ({ path, type: 'file' as const });
const d = (path: string) => ({ path, type: 'dir' as const });
const st = (path: string, mark: Entry['mark'] = 'modified'): Entry => ({ path, mark });

function setup(baseline = [d('src'), d('src/main'), f('src/main/A.kt'), f('src/main/B.kt'), d('docs'), f('docs/x.md'), f('README.md')], status: Entry[] = []) {
	const m = new GitTreeModel();
	m.baseline(baseline);
	m.setStatus(status);
	return m;
}
const shown = (m: GitTreeModel) => m.rows.map((r) => `${r.depth}${r.kind === 'dir' ? (r.expanded ? '-' : '+') : ' '}${r.path}`);

describe('the Git tree rows', () => {
	// Decision (ADR-0067 §5): the whole project, and only the folders holding changes open.
	it('opens the ancestors of the changes and no other folder', () => {
		const m = setup(undefined, [st('src/main/A.kt')]);
		expect(shown(m)).toEqual(['1+docs', '1-src', '2-src/main', '3 src/main/A.kt', '3 src/main/B.kt', '1 README.md']);
		expect(m.rows.find((r) => r.path === 'src/main/A.kt')).toMatchObject({ changed: true, letter: 'M' });
		expect(m.rows.find((r) => r.path === 'src')).toMatchObject({ changed: true });
		expect(m.rows.find((r) => r.path === 'docs')).toMatchObject({ changed: false });
	});

	// Decision: a status path the watch does not list (a deleted file) sits under its folder.
	it('puts a deleted file under its folder, struck through', () => {
		const m = setup(undefined, [st('docs/gone.md', 'deleted')]);
		expect(shown(m)).toContain('2 docs/gone.md');
		expect(m.rows.find((r) => r.path === 'docs/gone.md')).toMatchObject({ deleted: true, letter: 'D' });
		expect(m.rows.find((r) => r.path === 'docs')).toMatchObject({ expanded: true });
	});

	// Decision: a change under a folder the watch excludes (.idea) adds that folder with its file.
	it('adds the folder of a change under .idea/ that the watch does not list', () => {
		const m = setup(undefined, [st('.idea/workspace.xml')]);
		expect(shown(m)).toContain('1-.idea');
		expect(shown(m)).toContain('2 .idea/workspace.xml');
	});

	// Decision: folders first, names in numeric order.
	it('sorts folders first and names numerically', () => {
		const m = setup([f('file10'), f('file2'), d('zdir')], []);
		expect(m.rows.map((r) => r.label)).toEqual(['zdir', 'file2', 'file10']);
	});

	// Decision: the watch's changes are applied (a created file shows, a deleted folder takes its children).
	it('follows the file watch', () => {
		const m = setup(undefined, []);
		m.toggle('docs');
		m.changes([{ path: 'docs/new.md', kind: 'created', type: 'file' }]);
		expect(shown(m)).toContain('2 docs/new.md');
		m.changes([{ path: 'docs', kind: 'deleted' }]);
		expect(m.rows.map((r) => r.path)).not.toContain('docs');
		expect(m.rows.map((r) => r.path)).not.toContain('docs/x.md');
	});
});

describe('the Git tree expansion rules', () => {
	// Decision: a folder the user collapsed stays collapsed while the same paths stay changed...
	it('keeps a folder the user closed closed across statuses that change nothing new', () => {
		const m = setup(undefined, [st('src/main/A.kt')]);
		m.toggle('src');
		expect(shown(m)).toContain('1+src');
		m.setStatus([st('src/main/A.kt', 'added')]); // the same path, another mark
		expect(shown(m)).toContain('1+src');
		m.setStatus([st('src/main/A.kt'), st('docs/x.md')]); // a change elsewhere
		expect(shown(m)).toContain('1+src');
		expect(shown(m)).toContain('1-docs');
	});

	// ... and opens again when a NEW change lands beneath it, once.
	it('opens a closed folder again when a new change lands beneath it, once', () => {
		const m = setup(undefined, [st('src/main/A.kt')]);
		m.toggle('src');
		m.setStatus([st('src/main/A.kt'), st('src/main/B.kt')]);
		expect(shown(m)).toContain('1-src');
		m.toggle('src');
		m.setStatus([st('src/main/A.kt'), st('src/main/B.kt')]);
		expect(shown(m)).toContain('1+src');
	});

	// Decision: a path that stopped being changed and changes again is new.
	it('treats a path that left the status and came back as a new change', () => {
		const m = setup(undefined, [st('docs/x.md')]);
		m.toggle('docs');
		m.setStatus([]);
		m.setStatus([st('docs/x.md')]);
		expect(shown(m)).toContain('1-docs');
	});

	// Decision (ADR-0067 §5): the Git tree's expansion is its own. Expanding here leaves a
	// FileTreeModel on the same FileWatch unchanged. Seen failing with one shared expansion set.
	it('leaves the Editor tree model unchanged when it expands and collapses', async () => {
		const stream = new FakeStream('fw');
		const frames = new FakeFrames();
		const ws = { id: 'w1' as string | null, root: '/proj', subscribe: () => () => {} };
		const files = new FileTreeModel(new FakeCalls() as unknown as TreeClient, { id: 'w1', root: '/proj/' });
		const git = new GitTreeModel();
		const watch = new FileWatch(stream as unknown as WatchClient, ws, files.sink, frames);
		const off = watch.listen(git);
		stream.answer(0, {
			subscriptionId: 'fw1',
			watched: 4,
			excluded: [],
			entries: [d('src'), d('src/main'), f('src/main/A.kt'), f('README.md')],
		});
		await settle();
		git.setStatus([st('src/main/A.kt')]);
		const editorBefore = files.rows.map((r) => `${r.path}:${r.expanded}`);
		expect(editorBefore).toEqual(['src/main:false', 'README.md:false']);
		expect(shown(git)).toContain('2-src/main'); // open in the Git tree

		const editor = () => files.rows.map((r) => `${r.path}:${r.expanded}`);
		git.toggle('src/main'); // closed in the Git tree
		await settle();
		expect(shown(git)).toContain('2+src/main');
		expect(editor()).toEqual(editorBefore);
		git.toggle('src/main'); // open again
		await settle();
		expect(shown(git)).toContain('2-src/main');
		expect(editor()).toEqual(editorBefore);
		// ... and the other way: the Editor's expansion does not open the Git tree's folders.
		const gitBefore = shown(git);
		await files.expand('src/main');
		expect(shown(git)).toEqual(gitBefore);
		// A change arrives through the one watch and both models see it.
		stream.emit(0, { topic: 'file.changes', type: 'changes', payload: { path: '.', changes: [{ path: 'src/main/C.kt', kind: 'created', type: 'file' }], truncated: false } } as Partial<BusEvent> & { topic: string; type: string; payload: unknown });
		frames.tick();
		expect(shown(git)).toContain('3 src/main/C.kt');
		off();
	});
});

class Mem implements KeyValueStorage {
	data = new Map<string, string>();
	getItem(k: string) {
		return this.data.get(k) ?? null;
	}
	setItem(k: string, v: string) {
		this.data.set(k, v);
	}
}

describe('the remembered Git tree view', () => {
	// Decision (ADR-0067 §5): per browser and project.
	it('returns what was saved, per project', () => {
		const s = new Mem();
		expect(loadPrefs(s, 'a')).toEqual(DEFAULT_PREFS);
		savePrefs(s, 'a', { view: 'tree', group: 'flat' });
		expect(loadPrefs(s, 'a')).toEqual({ view: 'tree', group: 'flat' });
		expect(loadPrefs(s, 'b')).toEqual(DEFAULT_PREFS);
	});

	// Decision: values are checked on the way in, field by field.
	it('refuses stored values that are not its words', () => {
		const s = new Mem();
		for (const bad of ['', 'null', '[]', '"tree"', '{', '{"view":"Tree","group":1}', '{"view":"x","group":"x"}']) {
			s.data.set(PREFS_PREFIX + 'a', bad);
			expect(loadPrefs(s, 'a')).toEqual(DEFAULT_PREFS);
		}
		s.data.set(PREFS_PREFIX + 'a', '{"view":"tree","group":"nope"}');
		expect(loadPrefs(s, 'a')).toEqual({ view: 'tree', group: DEFAULT_PREFS.group });
	});

	// Decision: a storage that throws, or none, breaks nothing.
	it('survives a storage that throws, or none', () => {
		const bad: KeyValueStorage = {
			getItem: () => {
				throw new Error('blocked');
			},
			setItem: () => {
				throw new Error('blocked');
			},
		};
		expect(loadPrefs(bad, 'a')).toEqual(DEFAULT_PREFS);
		expect(() => savePrefs(bad, 'a', { view: 'tree', group: 'flat' })).not.toThrow();
		expect(loadPrefs(null, 'a')).toEqual(DEFAULT_PREFS);
		expect(() => savePrefs(null, 'a', DEFAULT_PREFS)).not.toThrow();
	});
});
