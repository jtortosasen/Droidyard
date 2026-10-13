import { render } from 'svelte/server';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { settle } from '../build/testing';
import { chain, refsOf, setup } from './historytesting';
import { FetchModel, type FetchClient } from './fetch';
import HistoryPanel from './HistoryPanel.svelte';
import type { GitWatch } from './watch';

// The History tab as the server-side renderer draws it: the markup the level-4 checks
// and a screen reader meet (no browser: effects do not run, the rows are the first window).
afterEach(() => vi.unstubAllGlobals());

async function panel(bad = false) {
	vi.stubGlobal('window', {});
	const t = setup(() => chain(6), 10);
	await settle();
	t.state.refs = refsOf(['c0'], {
		head: { oid: 'c0', branch: 'main', detached: false, upstream: 'origin/main', upstreamGone: false, ahead: 2, behind: 1 },
		branches: [
			{ name: 'main', oid: 'c0', upstream: 'origin/main' },
			{ name: bad ? '<b onmouseover=1>' : 'bugfix/x', oid: 'c2' },
			{ name: 'feature/y', oid: 'c3' },
		],
		remotes: [
			{ remote: 'origin', name: 'HEAD', oid: 'c0' },
			{ remote: 'origin', name: 'main', oid: 'c0' },
			{ remote: 'origin', name: 'dev', oid: 'c4' },
		],
		tags: [{ name: 'v1', oid: 'c1' }],
		worktrees: [{ path: '/w/app', branch: 'main', oid: 'c0', current: true }],
	});
	t.store.refreshRefs();
	await settle();
	t.store.select('c2');
	const body = render(HistoryPanel, { props: { store: t.store, watch: t.watch as unknown as GitWatch, workspaceId: 'ws1', fetcher: new FetchModel({ call: (() => Promise.resolve({})) as FetchClient['call'] }, { root: '/p' }, () => {}) } }).body;
	return { body, t };
}

describe('HistoryPanel markup', () => {
	// Decision (plan step 6): the side panel's sections carry their counts, origin/HEAD is not among the refs, each ref row names its full ref.
	it('has the four sections with their counts and a row per ref', async () => {
		const { body } = await panel();
		for (const [id, n] of [['local', 3], ['remotes', 2], ['tags', 1], ['worktrees', 1]] as const) {
			expect(body, id).toContain(`data-refs-section="${id}" data-count="${n}"`);
		}
		for (const ref of ['refs/heads/main', 'refs/heads/bugfix/x', 'refs/remotes/origin/dev', 'refs/tags/v1']) expect(body, ref).toContain(`data-ref="${ref}"`);
		expect(body).not.toContain('origin/HEAD');
		expect(body).toMatch(/data-ref="refs\/heads\/main"[^>]*data-current/);
		expect(body).toContain('data-worktree="/w/app"');
		expect(body).toContain('Filter Branches/Tags');
	});

	// Decision (item 10): the toolbar is across the tab, with the branch display and its ahead and behind.
	it('has the toolbar with the branch display', async () => {
		const { body } = await panel();
		for (const id of ['fetch', 'pull', 'push', 'stash', 'create-branch', 'navigate-head']) expect(body, id).toContain(`data-toolbar="${id}"`);
		expect(body).toMatch(/data-branch-display[^>]*data-ahead="2"[^>]*data-behind="1"/);
		expect(body).toContain('data-filter-settings');
	});

	// Decision (item 10): the table's rows keep their hooks and carry the badges; a branch joined with its remote is one badge.
	it('keeps the row hooks and draws the badges', async () => {
		const { body } = await panel();
		expect(body).toMatch(/aria-rowindex="1"[^>]*aria-selected="false"[^>]*data-oid="c0"/);
		expect(body).toMatch(/aria-selected="true"[^>]*data-oid="c2"/);
		expect(body).toMatch(/data-badge="branch"[^>]*>(?:(?!<\/span>\s*<\/span>).)*main \| origin/s);
		expect(body).not.toContain('>origin/main<');
		expect(body).toMatch(/data-badge="tag"/);
		expect(body).toMatch(/data-badge="remote"[^>]*>(?:(?!<\/span>\s*<\/span>).)*origin\/dev/s);
	});

	// Decision (step 6 fix): the GRAPH & SUBJECT cell is one line in this order: the graph, the badges, the subject. The table's wrapper must not
	// share the `main` class with the cell (its column layout once stacked and centred the cell's content).
	it('orders the main cell as graph, badges, subject, and the wrapper does not take the cell class', async () => {
		const { body } = await panel();
		const row = /data-row="commit"[^>]*data-oid="c0"[\s\S]*?<div class="cell svelte/.exec(body)?.[0] ?? '';
		const graph = row.indexOf('class="graph');
		const badge = row.indexOf('data-badge="branch"');
		const subject = row.indexOf('class="text subject');
		expect(graph).toBeGreaterThan(-1);
		expect(badge).toBeGreaterThan(graph);
		expect(subject).toBeGreaterThan(badge);
		expect(body).not.toMatch(/<div class="main[ "]/);
		expect(body).toContain('class="table-area');
	});

	// Decision: every text from git is a text node, never markup.
	it('escapes a ref name', async () => {
		const { body } = await panel(true);
		expect(body).not.toContain('<b onmouseover');
		expect(body).toContain('&lt;b onmouseover=1>'); // `<` is what opens a tag; the renderer escapes it
	});
});
