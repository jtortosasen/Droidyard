import { describe, expect, it } from 'vitest';
import { settle } from '../build/testing';
import type { KeyValueStorage } from '../ui/layout';
import { HistoryStore, type Commit, type HistoryClient } from './history';
import { FakeWatch, refsOf } from './historytesting';
import { ROOT_FILTER_PREFIX, loadRootFilter, narrowFilter, rootFilterMemory, saveRootFilter } from './rootfilter';

const mem = (): KeyValueStorage & { data: Map<string, string> } => {
	const data = new Map<string, string>();
	return { data, getItem: (k) => data.get(k) ?? null, setItem: (k, v) => void data.set(k, v) };
};
const throwing: KeyValueStorage = {
	getItem: () => {
		throw new Error('blocked');
	},
	setItem: () => {
		throw new Error('blocked');
	},
};

describe('the stored root filter', () => {
	// Decision (R1): the filter is a per-viewer convenience; absent, garbage or a storage that throws is "every root", never an error.
	it('reads nothing as every root, and never throws', () => {
		const s = mem();
		expect(loadRootFilter(s, 'p')).toBeNull();
		expect(loadRootFilter(null, 'p')).toBeNull();
		expect(loadRootFilter(throwing, 'p')).toBeNull();
		s.data.set(ROOT_FILTER_PREFIX + 'p', '{not json');
		expect(loadRootFilter(s, 'p')).toBeNull();
		s.data.set(ROOT_FILTER_PREFIX + 'p', '{"a":1}');
		expect(loadRootFilter(s, 'p')).toBeNull();
		s.data.set(ROOT_FILTER_PREFIX + 'p', '["lib", 3, "", null, "app/x"]');
		expect(loadRootFilter(s, 'p')).toEqual(['lib', 'app/x']);
		expect(() => saveRootFilter(throwing, 'p', ['lib'])).not.toThrow();
		expect(() => saveRootFilter(null, 'p', ['lib'])).not.toThrow();
	});

	// Decision (R1): the key includes the workspace, so two projects do not share a choice.
	it('keeps one choice per workspace', () => {
		const s = mem();
		saveRootFilter(s, 'one', ['lib']);
		expect(loadRootFilter(s, 'one')).toEqual(['lib']);
		expect(loadRootFilter(s, 'two')).toBeNull();
		saveRootFilter(s, 'one', null);
		expect(loadRootFilter(s, 'one')).toBeNull();
	});

	// Decision (R1): roots that are no longer there are dropped; a choice that comes to none or to all is "every root".
	it('narrows a stored choice to the roots present', () => {
		expect(narrowFilter(['lib', 'gone'], ['.', 'lib', 'other'])).toEqual(['lib']);
		expect(narrowFilter(['gone'], ['.', 'lib'])).toBeNull();
		expect(narrowFilter(['.', 'lib'], ['.', 'lib'])).toBeNull();
		expect(narrowFilter(null, ['.', 'lib'])).toBeNull();
	});
});

const at = (oid: string, commitTime: number, parents: string[] = []): Commit => ({ oid, parents, authorName: 'Ada', authorEmail: 'a@example.org', commitTime, subject: `subject of ${oid}` });

function setup() {
	const logs: { root: string; params: { from?: string[]; limit?: number } }[] = [];
	let failLog = false;
	const roots: Record<string, Commit[]> = { '.': [at('a1', 90)], lib: [at('b1', 80)], mod: [at('c1', 70)] };
	const client = {
		call: async (name: string, params: { root?: string; from?: string[]; limit?: number }) => {
			const root = params.root ?? '.';
			if (name === 'git.refs') return refsOf([roots[root][0].oid]);
			logs.push({ root, params });
			if (failLog && params.limit === 1) throw new Error('boom');
			const list = params.from && params.limit === 1 ? roots[root].filter((c) => c.oid === params.from![0]) : roots[root];
			return { commits: list, tips: [roots[root][0].oid], tipsTruncated: false, more: false };
		},
	} as unknown as HistoryClient;
	const store = new HistoryStore(client, { root: ' /proj ' }, new FakeWatch());
	return { store, logs, failLog: (b: boolean) => (failLog = b) };
}
const keys = (s: HistoryStore) => s.view.rows.map((r) => r.key);

describe('HistoryStore with a remembered root filter', () => {
	// Decision (R1): the remembered choice is restored when the roots are loaded, not before (the submodules arrive after the workspace).
	it('applies the stored choice when its roots arrive, dropping the ones that are gone', async () => {
		const s = mem();
		saveRootFilter(s, 'p', ['lib', 'gone']);
		const t = setup();
		t.store.remember(rootFilterMemory(s, 'p'));
		await settle();
		expect(t.store.view.filter).toBeNull(); // only the workspace is loaded
		t.store.setRoots(['lib', 'mod']);
		await settle();
		expect(t.store.view.filter).toEqual(['lib']);
		expect(keys(t.store)).toEqual(['lib:b1']);
		expect(s.data.get(ROOT_FILTER_PREFIX + 'p')).toBe('["lib","gone"]'); // restoring does not rewrite the stored choice
	});

	// Decision (R1): remembering after the log is shown applies at once.
	it('applies the stored choice to a log already shown', async () => {
		const s = mem();
		saveRootFilter(s, 'p', ['.']);
		const t = setup();
		t.store.setRoots(['lib']);
		await settle();
		expect(keys(t.store)).toEqual(['a1', 'lib:b1']);
		t.store.remember(rootFilterMemory(s, 'p'));
		expect(t.store.view.filter).toEqual(['.']);
		expect(keys(t.store)).toEqual(['a1']);
	});

	// Decision (R1): the person's click is written, and from then on it is the person's choice, not the stored one.
	it('stores the click and keeps it over the stored choice when roots change', async () => {
		const s = mem();
		saveRootFilter(s, 'p', ['lib']);
		const t = setup();
		t.store.remember(rootFilterMemory(s, 'p'));
		t.store.setRoots(['lib', 'mod']);
		await settle();
		expect(t.store.view.filter).toEqual(['lib']);
		t.store.toggleRoot('mod');
		expect(t.store.view.filter).toEqual(['lib', 'mod']);
		expect(s.data.get(ROOT_FILTER_PREFIX + 'p')).toBe('["lib","mod"]');
		t.store.setRoots(['lib', 'mod', 'z']);
		expect(t.store.view.filter).toEqual(['lib', 'mod']);
		t.store.setFilter(null);
		expect(s.data.get(ROOT_FILTER_PREFIX + 'p')).toBe('null');
		expect(t.store.view.filter).toBeNull();
	});

	// Decision (R1): a storage that throws leaves the filter working in memory.
	it('works when the storage throws', async () => {
		const t = setup();
		t.store.remember(rootFilterMemory(throwing, 'p'));
		t.store.setRoots(['lib']);
		await settle();
		expect(t.store.view.filter).toBeNull();
		t.store.toggleRoot('lib');
		expect(keys(t.store)).toEqual(['lib:b1']);
	});
});

describe('HistoryStore.readSubjects', () => {
	// Decision (R1): a submodule's HEAD subject is read from its own repository with one log commit, once per commit.
	it('reads a subject once and keeps it by the row key', async () => {
		const t = setup();
		t.store.setRoots(['lib']);
		await settle();
		t.logs.length = 0;
		t.store.readSubjects([{ root: 'lib', oid: 'b1' }]);
		t.store.readSubjects([{ root: 'lib', oid: 'b1' }]);
		await settle();
		expect(t.logs).toEqual([{ root: 'lib', params: { root: 'lib', from: ['b1'], limit: 1 } }]);
		expect(t.store.view.subjects.get('lib:b1')).toBe('subject of b1');
	});

	// Decision (R1): a failed read is asked again by the next call, and shows nothing meanwhile.
	it('asks again after a failure', async () => {
		const t = setup();
		t.store.setRoots(['lib']);
		await settle();
		t.failLog(true);
		t.store.readSubjects([{ root: 'lib', oid: 'b1' }]);
		await settle();
		expect(t.store.view.subjects.size).toBe(0);
		t.failLog(false);
		t.store.readSubjects([{ root: 'lib', oid: 'b1' }]);
		await settle();
		expect(t.store.view.subjects.get('lib:b1')).toBe('subject of b1');
	});
});
