import { describe, expect, it } from 'vitest';
import { capError, settle } from '../build/testing';
import { HistoryStore, NOT_LOADED, REVEAL_PAGES, type HistoryClient } from './history';
import { FakeWatch, chain, commit, refsOf, setup } from './historytesting';

// The refs half of the History store (plan m2n step 6; ADR-0070 §3).
describe('HistoryStore refs', () => {
	// Decision (ADR-0070 §3): the refs are read with the first status and again when head.oid changes, not when only the paths change.
	it('reads the refs with the first status and again when head.oid changes', async () => {
		const t = setup();
		await settle();
		expect(t.refsCalls).toHaveLength(1);
		expect(t.refsCalls[0].workspace).toBe('/proj');
		expect(t.store.view.refsState).toBe('ready');
		expect(t.store.view.refs?.head.branch).toBe('main');
		t.watch.set({ entries: [{ path: 'a', mark: 'modified' }] });
		await settle();
		expect(t.refsCalls).toHaveLength(1);
		t.watch.set({ head: { oid: 'n0', branch: 'main' } });
		await settle();
		expect(t.refsCalls).toHaveLength(2);
	});

	// Decision (ADR-0070 §3): refs whose tips differ from those the history started from reload the history from its first page.
	it('reloads the history when the refs have other tips', async () => {
		const t = setup(() => chain(5), 10);
		await settle();
		expect(t.calls).toHaveLength(1);
		t.state.refs = refsOf(['c0', 'c4'], {
			branches: [
				{ name: 'main', oid: 'c0' },
				{ name: 'dev', oid: 'c4' },
			],
		});
		t.store.refreshRefs();
		await settle();
		expect(t.calls).toHaveLength(2);
		expect(t.calls[1].params.from).toBeUndefined();
		expect(t.store.view.refs?.branches.map((b) => b.name)).toEqual(['main', 'dev']);
	});

	// Decision (ADR-0070 §3): equal tips leave the history alone (the rows, the scroll and an open expansion stay).
	it('does not reload the history when the tips are the same', async () => {
		const t = setup(() => chain(5), 10);
		await settle();
		t.state.refs = refsOf(['c0'], { tags: [{ name: 'v1', oid: 'c0' }] }); // a tag on a commit already in the tip set
		t.store.refreshRefs();
		await settle();
		expect(t.refsCalls).toHaveLength(2);
		expect(t.calls).toHaveLength(1);
		expect(t.store.view.refs?.tags).toHaveLength(1);
	});

	// Decision: a refs answer that arrives while the history is being read does not start another read; that read asks git afresh.
	it('does not reload the history for tips that arrive while it is being read', async () => {
		const logs: ((r: unknown) => void)[] = [];
		const client = {
			call: (name: string) => (name === 'git.refs' ? Promise.resolve(refsOf(['other'])) : new Promise((res) => logs.push(res))),
		} as unknown as HistoryClient;
		const s = new HistoryStore(client, { root: '/p' }, new FakeWatch());
		await settle();
		expect(logs).toHaveLength(1);
		expect(s.view.refsState).toBe('ready');
		logs[0]({ commits: [commit('c0')], tips: ['c0'], tipsTruncated: false, more: false });
		await settle();
		expect(logs).toHaveLength(1);
		expect(s.view.state).toBe('ready');
	});

	// Decision (ADR-0070 §3): showing the tab reads the refs; hiding it does not, and a tab already shown does not read again.
	it('reads the refs when the tab is shown, not when it is hidden or already shown', async () => {
		const t = setup();
		await settle();
		t.store.setVisible(true);
		await settle();
		expect(t.refsCalls).toHaveLength(2);
		t.store.setVisible(true);
		t.store.setVisible(false);
		await settle();
		expect(t.refsCalls).toHaveLength(2);
		t.store.setVisible(true);
		await settle();
		expect(t.refsCalls).toHaveLength(3);
	});

	// Decision (ADR-0070 §3): the window regaining focus reads the refs, and a ref another client moved then reloads the history.
	it('reads the refs when the window regains focus', async () => {
		const t = setup(() => chain(5), 10);
		await settle();
		t.state.refs = refsOf(['c0', 'c3']);
		t.store.focused();
		await settle();
		expect(t.refsCalls).toHaveLength(2);
		expect(t.calls).toHaveLength(2);
	});

	// Decision: a failed first read is text with a retry; a failed later read keeps the refs shown.
	it('shows a failed first read as text and keeps the refs when a later read fails', async () => {
		const t = setup();
		t.failRefsWith(capError('GIT_FAILED', 'no refs'));
		t.store.refreshRefs(); // overtakes the read the first status started
		await settle();
		expect(t.store.view.refsState).toBe('error');
		expect(t.store.view.refsProblem).toContain('no refs');
		t.failRefsWith(null);
		t.store.refreshRefs();
		await settle();
		expect(t.store.view.refsState).toBe('ready');
		t.failRefsWith(capError('GIT_FAILED', 'again'));
		t.store.refreshRefs();
		await settle();
		expect(t.store.view.refsState).toBe('ready');
		expect(t.store.view.refs).not.toBeNull();
	});

	// Decision: a selected ref that is no longer among the refs is dropped.
	it('drops a selected ref that has gone', async () => {
		const t = setup(() => chain(5), 10);
		await settle();
		await t.store.selectRef('refs/heads/main', 'c0');
		expect(t.store.view.selectedRef).toBe('refs/heads/main');
		t.state.refs = refsOf(['c0'], { branches: [{ name: 'other', oid: 'c0' }] });
		t.store.refreshRefs();
		await settle();
		expect(t.store.view.selectedRef).toBeNull();
	});

	// Decision (plan step 6): a click on a ref selects its commit, loading at most five more pages to find it, else saying so.
	it('loads at most five pages to reveal a commit, then says it is not loaded', async () => {
		const t = setup(() => chain(40), 2);
		await settle();
		const found = await t.store.reveal('c8'); // row 8 is in the fifth page after the first
		expect(found).toBe(true);
		expect(t.store.view.selected).toBe('c8');
		expect(t.store.view.scrollTo?.oid).toBe('c8');
		expect(t.store.view.notice).toBeNull();

		const t2 = setup(() => chain(40), 2);
		await settle();
		const missing = await t2.store.reveal('c30');
		expect(missing).toBe(false);
		expect(t2.calls).toHaveLength(1 + REVEAL_PAGES);
		expect(t2.store.view.rows).toHaveLength(2 + 2 * REVEAL_PAGES);
		expect(t2.store.view.selected).toBeNull();
		expect(t2.store.view.notice).toBe(NOT_LOADED);
		expect(NOT_LOADED).toBe('not in the loaded history; scroll to load more');
		// A later selection clears the note.
		t2.store.select('c1');
		expect(t2.store.view.notice).toBeNull();
	});

	// Decision (plan step 6): a commit that is not in the history at all stops at the end of it, with the note.
	it('stops at the end of the history', async () => {
		const t = setup(() => chain(3), 2);
		await settle();
		expect(await t.store.reveal('zzz')).toBe(false);
		expect(t.calls).toHaveLength(2);
		expect(t.store.view.notice).toBe(NOT_LOADED);
	});

	// Decision (plan step 6): Navigate to HEAD selects the current branch in the side panel and HEAD's row in the table; a detached HEAD selects only the row.
	it('navigates to HEAD', async () => {
		const t = setup(() => chain(5), 10);
		await settle();
		t.store.select('c3');
		expect(await t.store.navigateToHead()).toBe(true);
		expect(t.store.view.selectedRef).toBe('refs/heads/main');
		expect(t.store.view.selected).toBe('c0');
		const seq = t.store.view.scrollTo!.seq;

		t.state.refs = refsOf(['c0'], { head: { oid: 'c0', detached: true, upstreamGone: false, ahead: 0, behind: 0 } });
		t.store.refreshRefs();
		await settle();
		t.store.select('c3');
		expect(await t.store.navigateToHead()).toBe(true);
		expect(t.store.view.selectedRef).toBeNull();
		expect(t.store.view.selected).toBe('c0');
		expect(t.store.view.scrollTo!.seq).toBeGreaterThan(seq);
	});
});
