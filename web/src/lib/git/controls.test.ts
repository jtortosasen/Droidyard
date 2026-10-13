import { render } from 'svelte/server';
import { describe, expect, it } from 'vitest';
import { settle } from '../build/testing';
import { BRANCH_MENU, TOOLBAR, activate } from './controls';
import { chain, refsOf, setup } from './historytesting';
import type { HistoryClient } from './history';
import BranchMenu from './BranchMenu.svelte';
import GitToolbar from './GitToolbar.svelte';

describe('the write controls', () => {
	const ENABLED = ['fetch', 'create-branch', 'checkout', 'new-branch-from'];

	// Decision (owner's items 6, 7, 8, 10): the toolbar and the branch menu list exactly SourceGit's and IntelliJ's entries; part 2 enabled Fetch (P2), Create Branch, Checkout and New branch from (P3); every other one is disabled with the milestone that enables it.
	it('lists the toolbar and the branch menu, all disabled but the ones part 2 enabled, each with a reason', () => {
		expect(TOOLBAR.map((c) => c.id)).toEqual(['fetch', 'pull', 'push', 'stash', 'create-branch']);
		expect(BRANCH_MENU.map((c) => c.label)).toEqual([
			'Checkout',
			'New branch from',
			'Compare with current',
			'Merge into current',
			'Rebase onto',
			'Push',
			'Rename',
			'Delete',
		]);
		expect([...TOOLBAR, ...BRANCH_MENU].filter((c) => c.enabled).map((c) => c.id)).toEqual(ENABLED);
		// The other six entries of the menu and Pull, Push and Stash stay disabled, each saying when.
		const rest = [...TOOLBAR, ...BRANCH_MENU].filter((c) => !c.enabled);
		expect(rest.map((c) => c.id)).toEqual(['pull', 'push', 'stash', 'compare', 'merge', 'rebase', 'push', 'rename', 'delete']);
		for (const c of rest) expect(c.title, c.id).toMatch(/milestone/);
		// Part 2 still enables this one (step P9); the rest wait for a later milestone.
		const part2 = rest.filter((c) => c.title.includes('part 2')).map((c) => c.label);
		expect(part2).toEqual(['Compare with current']);
	});

	// Decision: a click on a disabled control reaches no capability, however it is wired.
	it('calls no capability when any disabled control is activated', async () => {
		const t = setup(() => chain(3), 10);
		await settle();
		const before = t.calls.length + t.refsCalls.length;
		const other: string[] = [];
		const spy = { call: (name: string) => (other.push(name), Promise.resolve({})) } as unknown as HistoryClient;
		for (const c of [...TOOLBAR, ...BRANCH_MENU].filter((c) => !c.enabled)) {
			expect(activate(c, () => void spy.call('git.log' as never, {} as never)), c.id).toBe(false);
		}
		expect(other).toEqual([]);
		expect(t.calls.length + t.refsCalls.length).toBe(before);
		// An enabled control would run (the door is not simply shut).
		let ran = 0;
		expect(activate({ id: 'x', label: 'x', enabled: true, title: '' }, () => ran++)).toBe(true);
		expect(ran).toBe(1);
	});
});

describe('the toolbar markup', () => {
	const html = (head: Parameters<typeof GitToolbar>[1]['head']) => render(GitToolbar, { props: { head, onnavigate: () => {} } }).body;
	const tag = (body: string, id: string) => new RegExp(`<button[^>]*data-toolbar="${id}"[^>]*>`).exec(body)?.[0] ?? '';

	// Decision: every write control that is not enabled carries the `disabled` attribute and a title in the markup itself; Fetch, Create Branch (once HEAD's commit is known) and Navigate to HEAD are the ones that work.
	it('renders the write controls disabled but Fetch and Create Branch, and Navigate to HEAD enabled once HEAD is known', () => {
		const body = html(refsOf(['a']).head);
		expect(tag(body, 'fetch')).not.toMatch(/\sdisabled/);
		expect(tag(body, 'create-branch')).not.toMatch(/\sdisabled/);
		for (const id of ['pull', 'push', 'stash']) {
			const b = tag(body, id);
			expect(b, id).toMatch(/\sdisabled(=""|\s|>)/);
			expect(b, id).toMatch(/title="[^"]*milestone/);
		}
		expect(tag(body, 'navigate-head')).not.toMatch(/\sdisabled/);
		expect(tag(html(null), 'navigate-head')).toMatch(/\sdisabled/);
	});

	// Decision (P3): Create Branch starts at HEAD, so it waits for the refs and refuses a repository with no commit yet, saying why.
	it('disables Create Branch until HEAD has a commit', () => {
		expect(tag(html(null), 'create-branch')).toMatch(/\sdisabled/);
		expect(tag(html(null), 'create-branch')).toContain('Reading the refs');
		const unborn = { branch: 'main', detached: false, upstreamGone: false, ahead: 0, behind: 0 };
		expect(tag(html(unborn), 'create-branch')).toMatch(/\sdisabled/);
		expect(tag(html(unborn), 'create-branch')).toContain('HEAD has no commit yet');
	});

	// Decision (item 10): the current branch's display carries its ahead and behind, and a dot marks Pull when behind and Push when ahead.
	it('shows the branch with its ahead and behind, and the dots', () => {
		const head = { oid: 'a', branch: 'main', detached: false, upstream: 'origin/main', upstreamGone: false, ahead: 3, behind: 0 };
		const body = html(head);
		expect(body).toMatch(/data-branch-display[^>]*data-ahead="3"[^>]*data-behind="0"/);
		expect(body).toContain('main');
		expect(tag(body, 'push')).toContain('data-dot');
		expect(tag(body, 'pull')).not.toContain('data-dot');
	});

	// Decision: git's text is escaped, never markup.
	it('escapes a branch name', () => {
		const body = html({ ...refsOf(['a']).head, branch: '<img src=x onerror=1>' });
		expect(body).not.toContain('<img src=x');
		expect(body).toContain('&lt;img src=x');
	});
});

describe('the branch menu markup', () => {
	// Decision (items 7, 8, P3): the menu draws IntelliJ's eight entries in order; Checkout and New branch from are enabled, the other six disabled with their reason; the branch name is text.
	it('renders eight entries, two enabled, and escapes the branch name', () => {
		const chosen: string[] = [];
		const body = render(BranchMenu, { props: { name: '<i>x', onchoose: (id: string) => chosen.push(id) } }).body;
		const items = [...body.matchAll(/<button[^>]*data-menu-item="([a-z-]+)"[^>]*>/g)];
		expect(items.map((m) => m[1])).toEqual(['checkout', 'new-branch-from', 'compare', 'merge', 'rebase', 'push', 'rename', 'delete']);
		for (const m of items) {
			if (m[1] === 'checkout' || m[1] === 'new-branch-from') {
				expect(m[0], m[1]).not.toMatch(/\sdisabled/);
				continue;
			}
			expect(m[0], m[1]).toMatch(/\sdisabled(=""|\s|>)/);
			expect(m[0], m[1]).toMatch(/title="[^"]*milestone/);
		}
		expect(body).not.toContain('<i>x');
		expect(body).toContain('&lt;i>x');
		expect(chosen).toEqual([]);
	});
});
