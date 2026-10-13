import { render } from 'svelte/server';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Result } from '../api/capabilities.gen';
import { CapabilityError } from '../api/client';
import { settle } from '../build/testing';
import { FetchModel, type FetchClient } from './fetch';
import HistoryPanel from './HistoryPanel.svelte';
import { chain, idleBranches, refsOf, setup, submodulesOf, idleUpdater } from './historytesting';
import type { RefsResult } from './refs';
import { SubmodulesModel, shortOid, submoduleBranch, submoduleRows, type Submodule, type SubmodulesClient } from './submodules';
import type { GitWatch } from './watch';

// The Submodules section (plan m2n P4): the model, the rows and the markup the level-4 checks meet.
afterEach(() => vi.unstubAllGlobals());

const R = 'a'.repeat(40);
const C = 'b'.repeat(40);
const LIST: Submodule[] = [
	{ path: 'extra', recorded: R, state: 'uninitialised' },
	{ path: 'lib', recorded: R, checkedOut: R, state: 'clean' },
	{ path: 'lib/nested', recorded: R, checkedOut: C, state: 'outdated' },
	{ path: 'ui/Theme', recorded: R, checkedOut: R, state: 'dirty' },
];

/** A client whose reads stay open until the test answers them. */
function open() {
	const calls: { params: unknown; workspace: string | undefined }[] = [];
	const answers: { ok: (r: Result<'git.submodules'>) => void; fail: (e: unknown) => void }[] = [];
	const client: SubmodulesClient = {
		call: (_name, params, opts) => {
			calls.push({ params, workspace: opts?.workspace });
			return new Promise((ok, fail) => answers.push({ ok, fail }));
		},
	};
	return { calls, answers, client };
}

describe('SubmodulesModel', () => {
	// Decision: one git.submodules per load, with no parameter, on the project's trimmed root; the list and the flag are kept as the daemon said them.
	it('calls git.submodules once per load, on the project, and keeps the list', async () => {
		const o = open();
		const m = new SubmodulesModel(o.client, { root: ' /proj ' });
		expect(m.view.state).toBe('idle');
		const p = m.load();
		expect(m.view.state).toBe('loading');
		expect(o.calls).toEqual([{ params: {}, workspace: '/proj' }]);
		o.answers[0].ok({ submodules: LIST, truncated: true });
		await p;
		expect(m.view).toEqual({ state: 'ready', problem: null, list: LIST, truncated: true });
	});

	// Decision: a first read that fails shows its reason and Retry (load again) recovers; a later failure keeps what was shown.
	it('shows a first failure, recovers on a new load, and keeps the list when a later read fails', async () => {
		const o = open();
		const m = new SubmodulesModel(o.client, { root: '/p' });
		let p = m.load();
		o.answers[0].fail(new CapabilityError({ code: 'GIT_FAILED', message: 'git failed' }, false));
		await p;
		expect(m.view.state).toBe('error');
		expect(m.view.problem).toContain('git failed');
		p = m.load();
		expect(m.view.state).toBe('loading');
		o.answers[1].ok({ submodules: LIST, truncated: false });
		await p;
		expect(m.view.state).toBe('ready');
		p = m.load();
		o.answers[2].fail(new Error('boom'));
		await p;
		expect(m.view.state).toBe('ready');
		expect(m.view.list).toEqual(LIST);
	});

	// Decision (verifier round): one read at a time. Calls made while one is in flight start nothing and, together, one more read when it ends (no stacked walks on repeated focus); that read's answer is the one shown.
	it('runs one read at a time and one more after the calls made meanwhile', async () => {
		const o = open();
		const m = new SubmodulesModel(o.client, { root: '/p' });
		const first = m.load();
		const second = m.load();
		const third = m.load();
		m.load();
		expect(o.calls, 'only the first read is on its way').toHaveLength(1);
		o.answers[0].ok({ submodules: [LIST[0]], truncated: false });
		await vi.waitFor(() => expect(o.calls).toHaveLength(2));
		expect(m.view.list, 'the first answer is shown while the next read runs').toEqual([LIST[0]]);
		o.answers[1].ok({ submodules: [LIST[1]], truncated: false });
		await Promise.all([first, second, third]);
		expect(o.calls, 'four calls made two reads').toHaveLength(2);
		expect(m.view.list).toEqual([LIST[1]]);
		// Idle again: the next call reads once more.
		const next = m.load();
		expect(o.calls).toHaveLength(3);
		o.answers[2].ok({ submodules: LIST, truncated: false });
		await next;
		expect(m.view.list).toEqual(LIST);
	});

	// Decision: a read that fails while another is wanted still lets the wanted one run, and a failure after a success keeps the list.
	it('runs the wanted read after one that failed', async () => {
		const o = open();
		const m = new SubmodulesModel(o.client, { root: '/p' });
		const first = m.load();
		m.load();
		o.answers[0].fail(new Error('boom'));
		await vi.waitFor(() => expect(o.calls).toHaveLength(2));
		o.answers[1].ok({ submodules: LIST, truncated: false });
		await first;
		expect(m.view.state).toBe('ready');
		expect(m.view.list).toEqual(LIST);
	});

	// Decision (plan P4): the section is read with the refs: when the store has its first answer of them and each time it holds a new one, not at the changes of the history, and not after dispose.
	it('follows the History store: a read per new answer of the refs', async () => {
		const t = setup(() => chain(3), 10);
		await settle();
		const o = open();
		const m = new SubmodulesModel(o.client, { root: '/p' });
		const off = m.follow(t.store);
		expect(o.calls, 'the refs are already there').toHaveLength(1);
		o.answers[0].ok({ submodules: [], truncated: false });
		await settle();
		t.store.select('c1'); // another change of the view, the same refs
		expect(o.calls).toHaveLength(1);
		t.state.refs = refsOf(['c0']); // the daemon answers with a new object each time
		t.store.refreshRefs(); // a new answer
		await settle();
		expect(o.calls).toHaveLength(2);
		o.answers[1].ok({ submodules: [], truncated: false });
		await settle();
		off();
		t.state.refs = refsOf(['c0']);
		t.store.refreshRefs();
		await settle();
		expect(o.calls).toHaveLength(2);
	});

	it('waits for the first refs when there are none yet', () => {
		const o = open();
		const m = new SubmodulesModel(o.client, { root: '/p' });
		let notify = () => {};
		const source = {
			view: { refs: null as unknown },
			subscribe: (l: () => void) => {
				notify = l;
				return () => {};
			},
		};
		m.follow(source);
		expect(o.calls).toHaveLength(0);
		source.view = { refs: {} };
		notify();
		expect(o.calls).toHaveLength(1);
	});
});

describe('submoduleRows', () => {
	// Decision: the rows keep the daemon's order (depth first) and say the short commit; an outdated one says the checked-out commit and then the recorded one.
	it('keeps the order and shows both commits only when they differ', () => {
		const rows = submoduleRows(LIST, '');
		expect(rows.map((r) => r.path)).toEqual(['extra', 'lib', 'lib/nested', 'ui/Theme']);
		expect(rows.map((r) => r.oids)).toEqual([shortOid(R), shortOid(R), `${shortOid(C)} → ${shortOid(R)}`, shortOid(R)]);
		expect(rows[2].title).toContain(`checked out ${shortOid(C)}, recorded ${shortOid(R)}`);
		expect(rows[0].checkedOut).toBeUndefined();
	});

	// Decision (step 6's filter): the box filters the path, case-insensitively, and the count follows.
	it('filters by path, case-insensitively', () => {
		expect(submoduleRows(LIST, ' THEME ').map((r) => r.path)).toEqual(['ui/Theme']);
		expect(submoduleRows(LIST, 'lib').map((r) => r.path)).toEqual(['lib', 'lib/nested']);
		expect(submoduleRows(LIST, 'nothing')).toEqual([]);
	});

	const head = (over: Partial<RefsResult['head']>): RefsResult['head'] => ({ oid: R, branch: 'main', detached: false, upstreamGone: false, ahead: 0, behind: 0, ...over });

	// Decision (R1): a row shows its display name, its branch from its own refs, and the subject of the commit checked out.
	it('shows the display name, the branch and the subject of the commit checked out', () => {
		const rows = submoduleRows(LIST, '', {
			names: new Map([['lib', 'LIB']]),
			refs: new Map([['lib', refsOf([R], { head: head({ branch: 'feature/x' }) })]]),
			subjects: new Map([[`lib:${R}`, 'Fix the thing']]),
		});
		expect(rows[1]).toMatchObject({ path: 'lib', name: 'LIB', branch: 'feature/x', subject: 'Fix the thing' });
		expect(rows[0]).toMatchObject({ path: 'extra', name: 'extra' }); // no name known: the path
		expect(rows[0].branch).toBeUndefined();
		expect(rows[0].subject).toBeUndefined();
	});

	// Decision (R1): a detached HEAD says "detached" and the local and remote-tracking branches at its commit, if any.
	it('says detached and the branches whose tip is the commit', () => {
		const detached = head({ branch: undefined, detached: true });
		const refs = refsOf([R], {
			head: detached,
			branches: [{ name: 'main', oid: R }, { name: 'other', oid: C }, { name: 'dev', oid: R }],
			remotes: [{ remote: 'origin', name: 'main', oid: R }, { remote: 'origin', name: 'HEAD', oid: R }, { remote: 'origin', name: 'far', oid: C }],
		});
		expect(submoduleBranch(refs, R)).toEqual({ text: 'detached (main, dev, origin/main)', title: 'detached at main, dev, origin/main' });
		expect(submoduleBranch(refsOf([R], { head: detached, branches: [], remotes: [] }), R)).toEqual({ text: 'detached', title: 'detached' });
		const many = refsOf([R], { head: detached, branches: ['a', 'b', 'c', 'd', 'e'].map((name) => ({ name, oid: R })), remotes: [] });
		expect(submoduleBranch(many, R)?.text).toBe('detached (a, b, c, +2)');
	});

	// Decision (R1): refs older than the list (another commit than the one shown) or missing show no branch, rather than a wrong one.
	it('shows no branch without refs or when they describe another commit', () => {
		expect(submoduleBranch(undefined, R)).toBeUndefined();
		expect(submoduleBranch(refsOf([C], { head: head({ oid: C }) }), R)).toBeUndefined();
		expect(submoduleBranch(refsOf([R], { head: head({}) }), undefined)).toBeUndefined();
	});
});

async function panel(list: Submodule[], over: { truncated?: boolean; filter?: boolean } = {}) {
	vi.stubGlobal('window', {});
	const t = setup(() => chain(3), 10);
	await settle();
	t.state.refs = refsOf(['c0'], { tags: [{ name: 'v1', oid: 'c1' }], worktrees: [{ path: '/w/app', branch: 'main', oid: 'c0', current: true }] });
	t.store.refreshRefs();
	await settle();
	const submodules = await submodulesOf(list, over.truncated ?? false);
	const fetcher = new FetchModel({ call: (() => Promise.resolve({})) as FetchClient['call'] }, { root: '/p' }, () => {});
	return render(HistoryPanel, { props: { store: t.store, watch: t.watch as unknown as GitWatch, workspaceId: 'ws1', fetcher, branches: idleBranches(), submodules, updater: idleUpdater() } }).body;
}

describe('the Submodules section markup', () => {
	// Decision (plan P4's hooks): the section carries its count, between Tags and Worktrees, and each row its path, state and both commits in full.
	it('has the section with its count between Tags and Worktrees, and a row per submodule with its hooks', async () => {
		const body = await panel(LIST);
		expect(body).toContain('data-refs-section="submodules" data-count="4"');
		const at = (s: string) => body.indexOf(`data-refs-section="${s}"`);
		expect(at('tags')).toBeGreaterThan(-1);
		expect(at('submodules')).toBeGreaterThan(at('tags'));
		expect(at('worktrees')).toBeGreaterThan(at('submodules'));
		expect(body).toMatch(new RegExp(`data-submodule="lib/nested"[^>]*data-state="outdated"[^>]*data-recorded="${R}"[^>]*data-checked-out="${C}"`));
		expect(body).toMatch(new RegExp(`data-submodule="extra"[^>]*data-state="uninitialised"[^>]*data-recorded="${R}"`));
		expect(/data-submodule="extra"[^>]*data-checked-out/.test(body), 'an uninitialised row has no checked-out commit').toBe(false);
		expect(body).toContain(`${shortOid(C)} → ${shortOid(R)}`);
		expect(body).toContain('Filter Branches/Tags/Submodules');
	});

	// Decision (R1): a row is shown by its display name with the full path in its title; the section's button reads "Update submodules".
	it('names a row by its last segment, keeps the path in the title, and labels the button', async () => {
		const body = await panel(LIST);
		expect(body).toMatch(/data-submodule-name[^>]*>nested</);
		expect(body).toMatch(/title="lib\/nested: checked out at another commit[^"]*"[^>]*data-submodule="lib\/nested"/);
		expect(body).toMatch(/data-submodules-update="all"[\s\S]*?<span>Update submodules<\/span>/);
		expect(body).not.toContain('data-update-summary');
	});

	// Decision (R1): the filter and the stripes name the workspace by the project folder and two submodules with the same last segment by
	// the parents that tell them apart; the full path stays in the title.
	it('labels the root filter with the folder name and the shortest unique names', async () => {
		vi.stubGlobal('window', {});
		const t = setup(() => chain(3), 10);
		t.store.setRoots(['x/hev-server/src/core', 'x/hev-tunnel/src/core']);
		await settle();
		const submodules = await submodulesOf([
			{ path: 'x/hev-server/src/core', recorded: R, checkedOut: R, state: 'clean' },
			{ path: 'x/hev-tunnel/src/core', recorded: R, checkedOut: R, state: 'clean' },
		]);
		const fetcher = new FetchModel({ call: (() => Promise.resolve({})) as FetchClient['call'] }, { root: '/work/android-kids-app' }, () => {});
		const body = render(HistoryPanel, { props: { store: t.store, watch: t.watch as unknown as GitWatch, workspaceId: 'ws1', fetcher, branches: idleBranches(), submodules, updater: idleUpdater() } }).body;
		expect(body).toMatch(/data-root-option="\."[^>]*title="\/proj"[^>]*>[\s\S]*?<span class="text[^"]*">proj</);
		expect(body).toMatch(/data-root-option="x\/hev-server\/src\/core"[^>]*title="x\/hev-server\/src\/core"[^>]*>[\s\S]*?<span class="text[^"]*">hev-server\/src\/core</);
		expect(body).toMatch(/data-root-option="x\/hev-tunnel\/src\/core"[\s\S]*?<span class="text[^"]*">hev-tunnel\/src\/core</);
		expect(body).toMatch(/data-submodule-name[^>]*>hev-server\/src\/core</);
	});

	// Decision: a path is git's text. A markup-looking one is escaped, never a tag or an attribute that opens.
	it('renders a hostile path as text', async () => {
		const bad = '<img src=x onerror=alert(1)>';
		const body = await panel([{ path: bad, recorded: R, state: 'clean', checkedOut: R }]);
		expect(body).not.toContain('<img');
		expect(body).toContain('&lt;img src=x onerror=alert(1)>');
	});

	// Decision: with no submodule the section says None and counts 0; a cut list says so.
	it('says None for no submodule and notes a truncated list', async () => {
		expect(await panel([])).toMatch(/data-refs-section="submodules" data-count="0"[\s\S]*?None/);
		expect(await panel(LIST, { truncated: true })).toContain('Some submodules are not listed');
	});

	// Decision: a failed first read shows its reason with a Retry inside the section, and the rest of the panel stays.
	it('shows the reason and Retry when the first read failed', async () => {
		vi.stubGlobal('window', {});
		const t = setup(() => chain(3), 10);
		await settle();
		const client = { call: () => Promise.reject(new CapabilityError({ code: 'GIT_FAILED', message: 'git failed' }, false)) } as unknown as SubmodulesClient;
		const submodules = new SubmodulesModel(client, { root: '/p' });
		await submodules.load();
		const fetcher = new FetchModel({ call: (() => Promise.resolve({})) as FetchClient['call'] }, { root: '/p' }, () => {});
		const body = render(HistoryPanel, { props: { store: t.store, watch: t.watch as unknown as GitWatch, workspaceId: 'ws1', fetcher, branches: idleBranches(), submodules, updater: idleUpdater() } }).body;
		expect(body).toMatch(/data-refs-section="submodules"[\s\S]*?role="alert"[\s\S]*?git failed[\s\S]*?Retry/);
		expect(body).toContain('data-refs-section="local"');
	});
});
