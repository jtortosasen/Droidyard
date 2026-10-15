import { describe, expect, it } from 'vitest';
import { settle } from '../build/testing';
import { CommitText, commitFileOf } from './diffsource';
import { HistoryStore, type Commit, type HistoryClient } from './history';
import { FakeWatch, refsOf, showOf } from './historytesting';

// One log for every root (plan m2n P7): the store over a workspace and a submodule `lib`.
const at = (oid: string, commitTime: number, parents: string[] = []): Commit => ({
	oid,
	parents,
	authorName: 'Ada',
	authorEmail: 'ada@example.org',
	commitTime,
	subject: `commit ${oid}`,
});

function setup(roots: Record<string, Commit[]>, pageSize = 2) {
	const logs: { root: string; params: { from?: string[]; skip?: number } }[] = [];
	const refsCalls: string[] = [];
	const shows: { oid: string; root: string }[] = [];
	const state = { tips: Object.fromEntries(Object.entries(roots).map(([r, l]) => [r, [l[0]?.oid ?? '']])) as Record<string, string[]> };
	const client = {
		call: async (name: string, params: { root?: string; from?: string[]; skip?: number; oid?: string }) => {
			const root = params.root ?? '.';
			if (name === 'git.refs') {
				refsCalls.push(root);
				return refsOf(state.tips[root]);
			}
			if (name === 'git.show') {
				shows.push({ oid: params.oid!, root });
				return showOf(params.oid!);
			}
			logs.push({ root, params });
			const skip = params.skip ?? 0;
			return { commits: roots[root].slice(skip, skip + pageSize), tips: state.tips[root], tipsTruncated: false, more: skip + pageSize < roots[root].length };
		},
	} as unknown as HistoryClient;
	const watch = new FakeWatch();
	const store = new HistoryStore(client, { root: '/p' }, watch);
	return { store, logs, refsCalls, shows, state, watch, client };
}

const keys = (s: HistoryStore) => s.view.rows.map((r) => r.key);

describe('HistoryStore with submodule roots', () => {
	const ws = () => [at('a1', 90, ['a2']), at('a2', 70, ['a3']), at('a3', 30)];
	const lib = () => [at('b1', 80, ['b2']), at('b2', 60, ['b3']), at('b3', 20)];

	// Decision (ADR-0071 §7): a root is read with its `root` parameter, the workspace without it, and the rows are merged by time.
	it('merges a submodule root in when its first page arrives', async () => {
		const t = setup({ '.': ws(), lib: lib() });
		await settle();
		expect(keys(t.store)).toEqual(['a1', 'a2']); // the workspace alone until the submodule's page arrives
		t.store.setRoots(['lib']);
		await settle();
		expect(t.logs.filter((l) => l.root === 'lib')[0].params).toEqual({ root: 'lib' });
		expect(t.logs[0].params).toEqual({}); // the workspace's first read carries no root
		expect(t.store.view.roots).toEqual(['.', 'lib']);
		// a2 is out, then the workspace's page is drained with more behind it: the rows after it wait for its next page.
		expect(keys(t.store)).toEqual(['a1', 'lib:b1', 'a2']);
		expect(t.store.view.rows.map((r) => r.root)).toEqual(['.', 'lib', '.']);
	});

	// Decision (ADR-0071 §7): the next page is read for every root the merge waits for, with that root's own tips and skip.
	it('pages each waiting root with its own from and skip', async () => {
		const t = setup({ '.': ws(), lib: lib() });
		t.store.setRoots(['lib']);
		await settle();
		t.logs.length = 0;
		t.store.loadMore(); // only the workspace is waited for: lib still has b2 queued
		await settle();
		expect(t.logs).toEqual([{ root: '.', params: { from: ['a1'], skip: 2 } }]);
		expect(keys(t.store)).toEqual(['a1', 'lib:b1', 'a2', 'lib:b2']);
		t.store.loadMore(); // now lib's queue is out, with more behind it
		await settle();
		expect(t.logs[1]).toEqual({ root: 'lib', params: { from: ['b1'], skip: 2, root: 'lib' } });
		expect(keys(t.store)).toEqual(['a1', 'lib:b1', 'a2', 'lib:b2', 'a3', 'lib:b3']);
	});

	// Decision (ADR-0071 §7): the rows of a root, filtered, equal that root's own sequence, and the lanes are laid out from them.
	it('filters to a root and lays the lanes out again', async () => {
		const t = setup({ '.': ws(), lib: lib() }, 10);
		t.store.setRoots(['lib']);
		await settle();
		expect(keys(t.store)).toEqual(['a1', 'lib:b1', 'a2', 'lib:b2', 'a3', 'lib:b3']);
		t.store.toggleRoot('lib');
		expect(t.store.view.filter).toEqual(['lib']);
		expect(keys(t.store)).toEqual(['lib:b1', 'lib:b2', 'lib:b3']);
		// A linear history in one lane, the first row on column 0: the workspace's lanes are not in the way.
		expect(t.store.view.rows.map((r) => r.lane.column)).toEqual([0, 0, 0]);
		expect(t.store.indexOf('lib:b2')).toBe(1);
		expect(t.store.indexOf('a1')).toBe(-1);
		t.store.setFilter(null);
		expect(keys(t.store)).toHaveLength(6);
	});

	// Decision (ADR-0071 §7): rows are keyed root:oid; one oid in two roots is two rows, and the opened commit asks for its own root.
	it('keeps one oid of two roots apart and opens the commit in its own root', async () => {
		const t = setup({ '.': [at('same', 50)], lib: [at('same', 40)] });
		t.store.setRoots(['lib']);
		await settle();
		expect(keys(t.store)).toEqual(['same', 'lib:same']);
		t.store.expand('lib:same');
		await settle();
		expect(t.shows).toEqual([{ oid: 'same', root: 'lib' }]);
		expect(t.store.view.expanded).toMatchObject({ key: 'lib:same', oid: 'same', root: 'lib', state: 'ready' });
		expect(t.store.view.selected).toBe('lib:same');
		t.store.expand('same');
		await settle();
		expect(t.shows[1]).toEqual({ oid: 'same', root: '.' });
	});

	// Decision (ADR-0070 §3, per root): a submodule's tips that moved reload that root only.
	it('reloads only the root whose tips changed', async () => {
		const t = setup({ '.': ws(), lib: lib() }, 10);
		t.store.setRoots(['lib']);
		await settle();
		t.logs.length = 0;
		t.state.tips.lib = ['b9'];
		t.store.refreshRefs();
		await settle();
		expect(t.logs.map((l) => l.root)).toEqual(['lib']);
		expect(t.logs[0].params).toEqual({ root: 'lib' });
	});

	// Decision (ADR-0071 §7): a root removed leaves the log at once; the others stay.
	it('drops a root that is no longer wanted', async () => {
		const t = setup({ '.': ws(), lib: lib() }, 10);
		t.store.setRoots(['lib']);
		await settle();
		t.store.setRoots([]);
		expect(t.store.view.roots).toEqual(['.']);
		expect(keys(t.store)).toEqual(['a1', 'a2', 'a3']);
	});

	// Decision (ADR-0071 §7): a repository without a submodule behaves as in part 1 (no root parameter, the bare oid as key).
	it('without submodules calls git.log with no root and keys rows by the bare oid', async () => {
		const t = setup({ '.': ws() }, 10);
		await settle();
		expect(t.logs).toEqual([{ root: '.', params: {} }]);
		expect(keys(t.store)).toEqual(['a1', 'a2', 'a3']);
		expect(t.store.view.roots).toEqual(['.']);
	});

	// Decision (ADR-0071 §7): a commit's file of a submodule is read with its root (paths are workspace-relative already).
	it('reads a submodule commit file with the root', async () => {
		const calls: unknown[] = [];
		const client = {
			call: async (_n: string, p: unknown) => {
				calls.push(p);
				return { exists: true, text: 'x', binary: false, tooLarge: false };
			},
		} as never;
		const text = new CommitText(client, { root: '/p' });
		text.follow(commitFileOf({ oid: 'c', parents: ['p'] }, { path: 'lib/src/A.kt', status: 'modified', binary: false }, 'lib'));
		await settle();
		expect(calls).toEqual(expect.arrayContaining([{ path: 'lib/src/A.kt', base: 'p', root: 'lib' }, { path: 'lib/src/A.kt', base: 'c', root: 'lib' }]));
	});
});
