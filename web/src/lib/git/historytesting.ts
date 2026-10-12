// Fakes for the History store's tests: commits, a Git watch, and a client that answers
// git.log from a list and git.refs from `state.refs`.
import type { Commit, HistoryClient, HistoryWatch } from './history';
import { HistoryStore } from './history';
import type { RefsResult } from './refs';
import type { GitView } from './watch';

/** What git.refs answers for a repository on `main` whose tip set is `tips`. */
export function refsOf(tips: string[], over: Partial<RefsResult> = {}): RefsResult {
	return {
		head: { oid: tips[0], branch: 'main', detached: false, upstreamGone: false, ahead: 0, behind: 0 },
		branches: [{ name: 'main', oid: tips[0] ?? '' }],
		branchesTruncated: false,
		remotes: [],
		remotesTruncated: false,
		tags: [],
		tagsTruncated: false,
		worktrees: [],
		worktreesTruncated: false,
		tips,
		tipsTruncated: false,
		...over,
	};
}

export const commit = (oid: string, parents: string[] = []): Commit => ({
	oid,
	parents,
	authorName: 'Ada Lovelace',
	authorEmail: 'ada@example.org',
	commitTime: 1_700_000_000,
	subject: `commit ${oid}`,
});

/** A linear history c0 (newest) .. c{n-1}. */
export function chain(n: number, prefix = 'c'): Commit[] {
	return Array.from({ length: n }, (_, i) => commit(`${prefix}${i}`, i + 1 < n ? [`${prefix}${i + 1}`] : []));
}

export class FakeWatch implements HistoryWatch {
	view: GitView = { state: 'watching', problem: null, head: { oid: 'c0', branch: 'main' }, entries: [], truncated: false };
	#l = new Set<() => void>();
	subscribe(l: () => void) {
		this.#l.add(l);
		return () => this.#l.delete(l);
	}
	set(over: Partial<GitView>) {
		this.view = { ...this.view, ...over };
		for (const l of [...this.#l]) l();
	}
}

export type LogParams = { from?: string[]; skip?: number; limit?: number };

export function setup(all: () => Commit[] = () => chain(5), pageSize = 2, tips = ['c0']) {
	const calls: { params: LogParams; workspace?: string }[] = [];
	const refsCalls: { workspace?: string }[] = [];
	let fail: Error | null = null;
	let failRefs: Error | null = null;
	// What git.refs answers; a test changes it to move a ref.
	const state = { refs: refsOf(tips) };
	const client = {
		call: async (name: string, params: LogParams, o?: { workspace?: string }) => {
			if (name === 'git.refs') {
				refsCalls.push({ workspace: o?.workspace });
				if (failRefs) throw failRefs;
				return state.refs;
			}
			calls.push({ params, workspace: o?.workspace });
			if (fail) throw fail;
			const skip = params.skip ?? 0;
			const list = all();
			const commits = list.slice(skip, skip + pageSize);
			return { commits, tips: params.from ? [] : state.refs.tips, tipsTruncated: false, more: skip + pageSize < list.length };
		},
	} as unknown as HistoryClient;
	const watch = new FakeWatch();
	const store = new HistoryStore(client, { root: ' /proj ' }, watch);
	return { calls, refsCalls, state, watch, store, failWith: (e: Error | null) => (fail = e), failRefsWith: (e: Error | null) => (failRefs = e) };
}
