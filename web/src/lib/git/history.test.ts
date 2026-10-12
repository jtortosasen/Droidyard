import { describe, expect, it } from 'vitest';
import { settle, capError } from '../build/testing';
import { HistoryStore, avatarHue, initials, relativeTime, type Commit, type HistoryClient, type HistoryWatch } from './history';
import type { GitView } from './watch';

const commit = (oid: string, parents: string[] = []): Commit => ({
	oid,
	parents,
	authorName: 'Ada Lovelace',
	authorEmail: 'ada@example.org',
	commitTime: 1_700_000_000,
	subject: `commit ${oid}`,
});

/** A linear history c0 (newest) .. c{n-1}. */
function chain(n: number, prefix = 'c'): Commit[] {
	return Array.from({ length: n }, (_, i) => commit(`${prefix}${i}`, i + 1 < n ? [`${prefix}${i + 1}`] : []));
}

class FakeWatch implements HistoryWatch {
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

type LogParams = { from?: string[]; skip?: number; limit?: number };

function setup(all: () => Commit[] = () => chain(5), pageSize = 2, tips = ['c0']) {
	const calls: { params: LogParams; workspace?: string }[] = [];
	let fail: Error | null = null;
	const client = {
		call: async (_n: string, params: LogParams, o?: { workspace?: string }) => {
			calls.push({ params, workspace: o?.workspace });
			if (fail) throw fail;
			const skip = params.skip ?? 0;
			const list = all();
			const commits = list.slice(skip, skip + pageSize);
			return { commits, tips: params.from ? [] : tips, tipsTruncated: false, more: skip + pageSize < list.length };
		},
	} as unknown as HistoryClient;
	const watch = new FakeWatch();
	const store = new HistoryStore(client, { root: ' /proj ' }, watch);
	return { calls, watch, store, failWith: (e: Error | null) => (fail = e) };
}

describe('HistoryStore', () => {
	// Decision (ADR-0069 §2): the first page has no `from`; the next passes the first page's tips with `skip` = the commits received, so pages stay one walk.
	it('reads the first page without from, then the next with the tips and skip', async () => {
		const t = setup();
		await settle();
		expect(t.calls).toHaveLength(1);
		expect(t.calls[0].params.from).toBeUndefined();
		expect(t.calls[0].params.skip).toBeUndefined();
		expect(t.calls[0].workspace).toBe('/proj');
		expect(t.store.view.state).toBe('ready');
		expect(t.store.view.rows.map((r) => r.commit.oid)).toEqual(['c0', 'c1']);
		expect(t.store.view.more).toBe(true);

		t.store.loadMore();
		t.store.loadMore(); // one page in flight at a time
		await settle();
		expect(t.calls).toHaveLength(2);
		expect(t.calls[1].params).toEqual({ from: ['c0'], skip: 2 });
		expect(t.store.view.rows.map((r) => r.commit.oid)).toEqual(['c0', 'c1', 'c2', 'c3']);
	});

	// Decision (ADR-0070 §1): the lanes are extended page by page, so a lane waiting at the end of a page continues in the next.
	it('extends the lanes across pages', async () => {
		const t = setup();
		await settle();
		const last = t.store.view.rows[1];
		expect(last.lane.edges).toEqual([{ kind: 'straight', from: 0, to: 0, color: last.lane.color }]);
		t.store.loadMore();
		await settle();
		const next = t.store.view.rows[2];
		expect(next.lane.incoming).toEqual([{ column: 0, color: last.lane.color }]);
		expect(next.lane.color).toBe(last.lane.color);
	});

	// Decision (ADR-0070 §3, this step): a new head.oid reloads from the first page, without `from`; the same oid does not.
	it('reloads from the first page when head.oid changes, and not when it stays', async () => {
		let list = chain(5);
		const t = setup(() => list);
		await settle();
		t.watch.set({ entries: [{ path: 'a', mark: 'modified' }] }); // same HEAD: no reload
		await settle();
		expect(t.calls).toHaveLength(1);
		list = [commit('n0', ['c0']), ...chain(5)];
		t.watch.set({ head: { oid: 'n0', branch: 'main' } });
		await settle();
		expect(t.calls).toHaveLength(2);
		expect(t.calls[1].params.from).toBeUndefined();
		expect(t.store.view.rows.map((r) => r.commit.oid)).toEqual(['n0', 'c0']);
	});

	// Decision: nothing is read before the watch has a status, and its first status is the baseline, not a change.
	it('waits for the first status and reads once', async () => {
		let n = 0;
		const client = {
			call: async () => {
				n++;
				return { commits: [], tips: [], tipsTruncated: false, more: false };
			},
		} as unknown as HistoryClient;
		const w = new FakeWatch();
		w.view = { ...w.view, head: null };
		const s = new HistoryStore(client, { root: '/p' }, w);
		expect(s.view.state).toBe('idle');
		expect(n).toBe(0);
		w.set({ head: { oid: 'c0' } });
		await settle();
		expect(n).toBe(1);
		expect(s.view.state).toBe('ready');
		w.set({ entries: [] });
		await settle();
		expect(n).toBe(1);
	});

	// Decision (ADR-0070 §3): the selection and the scroll stay at the same oid when it is still loaded, else the selection clears and the scroll goes to the top.
	it('keeps the selected oid and the scroll oid when still loaded, else clears them', async () => {
		let list = chain(5);
		const t = setup(() => list, 10);
		await settle();
		t.store.select('c3');
		t.store.setAnchor('c2');
		list = [commit('n0', ['c0']), ...chain(5)];
		t.watch.set({ head: { oid: 'n0' } });
		await settle();
		expect(t.store.view.selected).toBe('c3');
		expect(t.store.view.restore?.oid).toBe('c2');
		expect(t.store.indexOf('c2')).toBe(3);

		// A history rewritten so that neither oid is loaded any more.
		list = chain(3, 'z');
		t.watch.set({ head: { oid: 'z0' } });
		await settle();
		expect(t.store.view.selected).toBeNull();
		expect(t.store.view.restore?.oid).toBeNull();
	});

	// Decision: rows stay shown until the reload's page arrives, and each finished reload is a new restore request.
	it('keeps the rows shown while a reload is on its way', async () => {
		const t = setup(() => chain(5), 10);
		await settle();
		const seq = t.store.view.restore!.seq;
		t.watch.set({ head: { oid: 'x' } });
		expect(t.store.view.state).toBe('ready');
		expect(t.store.view.rows).toHaveLength(5);
		await settle();
		expect(t.store.view.restore!.seq).toBe(seq + 1);
	});

	// Decision (plan step 4): the Uncommitted Changes row shows the watch's entry count, and none when 0.
	it('counts the uncommitted paths from the watch', async () => {
		const t = setup();
		await settle();
		expect(t.store.view.uncommitted).toBe(0);
		t.watch.set({
			entries: [
				{ path: 'a', mark: 'modified' },
				{ path: 'b', mark: 'untracked' },
			],
		});
		expect(t.store.view.uncommitted).toBe(2);
		t.watch.set({ entries: [] });
		expect(t.store.view.uncommitted).toBe(0);
	});

	// Decision: a failure is text, the Retry reads again; a failed later page keeps the rows.
	it('shows a failed first read as text and retries; a failed page keeps the rows', async () => {
		const t = setup();
		t.failWith(capError('GIT_NOT_FOUND', 'git is missing'));
		t.store.reload();
		await settle();
		expect(t.store.view.state).toBe('error');
		expect(t.store.view.problem).toContain('GIT_NOT_FOUND');
		t.failWith(null);
		t.store.reload();
		await settle();
		expect(t.store.view.state).toBe('ready');
		t.failWith(capError('GIT_FAILED', 'boom'));
		t.store.loadMore();
		await settle();
		expect(t.store.view.rows).toHaveLength(2);
		expect(t.store.view.moreProblem).toContain('boom');
		expect(t.store.view.loadingMore).toBe(false);
	});

	// Decision: an answer of a read that a newer reload overtook is dropped.
	it('drops the answer of a read that a reload overtook', async () => {
		const resolvers: ((r: unknown) => void)[] = [];
		const client = { call: () => new Promise((res) => resolvers.push(res)) } as unknown as HistoryClient;
		const w = new FakeWatch();
		const s = new HistoryStore(client, { root: '/p' }, w);
		w.set({ head: { oid: 'b' } });
		const page = (oid: string) => ({ commits: [commit(oid)], tips: [oid], tipsTruncated: false, more: false });
		resolvers[1](page('b'));
		await settle();
		resolvers[0](page('a'));
		await settle();
		expect(s.view.rows.map((r) => r.commit.oid)).toEqual(['b']);
	});
});

describe('avatar and time', () => {
	// Decision: the avatar is drawn locally from the name's letters and a colour from the e-mail.
	it('takes initials from the name, else the e-mail, by code points', () => {
		expect(initials('Ada Lovelace', 'a@x')).toBe('AL');
		expect(initials('  grace  ', 'g@x')).toBe('G');
		expect(initials('', 'zed@x')).toBe('Z');
		expect(initials('', '')).toBe('?');
		expect(initials('\u{1F600}x', 'a@x')).toBe('\u{1F600}');
	});
	it('gives one hue per e-mail, case-insensitive, in 0..359', () => {
		expect(avatarHue('Ada@X.org')).toBe(avatarHue(' ada@x.org '));
		const h = avatarHue('someone@example.org');
		expect(h).toBeGreaterThanOrEqual(0);
		expect(h).toBeLessThan(360);
		expect(avatarHue('a@x.org')).not.toBe(avatarHue('b@x.org'));
	});
	it('words a commit time relative to now', () => {
		const now = 1_000_000_000_000;
		const at = (secondsAgo: number) => relativeTime(now / 1000 - secondsAgo, now);
		expect(at(10)).toBe('just now');
		expect(at(5 * 60)).toBe('5 min ago');
		expect(at(3 * 3600)).toBe('3 h ago');
		expect(at(2 * 86400)).toBe('2 d ago');
		expect(at(14 * 86400)).toBe('2 w ago');
		expect(at(90 * 86400)).toBe('3 mo ago');
		expect(at(800 * 86400)).toBe('2 y ago');
		expect(relativeTime(now / 1000 + 100, now)).toBe('just now'); // a clock skew is not "-3 min"
	});
});
