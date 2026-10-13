import { render } from 'svelte/server';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Result } from '../api/capabilities.gen';
import { settle } from '../build/testing';
import BranchMenu from './BranchMenu.svelte';
import { canCompare, entryAction, sourceOf, type BranchTarget } from './branchops';
import { branchUnion } from './branchroots';
import { FetchModel, type FetchClient } from './fetch';
import HistoryPanel from './HistoryPanel.svelte';
import { HistoryStore, type Commit, type HistoryClient } from './history';
import { FakeWatch, idleBranches, idleUpdater, refsOf, submodulesOf } from './historytesting';
import { NO_FOLDER_CHOICE, collapsedFolders, panelModel, type PanelInput, type RefRow, type RefsResult } from './refs';
import type { GitWatch } from './watch';

// The side panel lists the branches of every root once, each with a mark per root that has it (plan m2n R2; ADR-0074 §3).
afterEach(() => vi.unstubAllGlobals());

const OID = 'd'.repeat(40);
const A = 'a'.repeat(40);
const LIB = 'b'.repeat(40);
const NESTED = 'c'.repeat(40);

function refsWith(locals: Record<string, string>, remotes: Record<string, string> = {}, over: Partial<RefsResult> = {}): RefsResult {
	return refsOf([OID], {
		branches: Object.entries(locals).map(([name, oid]) => ({ name, oid })),
		remotes: Object.entries(remotes).map(([ref, oid]) => ({ remote: ref.split('/')[0], name: ref.split('/').slice(1).join('/'), oid })),
		...over,
	});
}

const ws = refsWith({ main: OID, 'feature/x': A }, { 'origin/main': OID, 'origin/feature/x': A });
const lib = refsWith({ main: OID, 'accessibility/fix': LIB }, { 'origin/accessibility/fix': LIB, 'origin/main': OID });
const nested = refsWith({ 'feature/x': NESTED }, {}, { head: { oid: NESTED, branch: 'feature/x', detached: false, upstreamGone: false, ahead: 0, behind: 0 } });
const rootRefs = new Map([
	['lib', lib],
	['lib/nested', nested],
]);
const roots = ['.', 'lib', 'lib/nested'];

const input = (union?: ReturnType<typeof branchUnion>, over: Partial<PanelInput> = {}): PanelInput => ({
	filter: '',
	favourites: new Set(),
	collapsed: new Set(),
	...(union !== undefined ? { union } : {}),
	...over,
});
const refRows = (refs: RefsResult, section: 'local' | 'remotes', union?: ReturnType<typeof branchUnion>, over: Partial<PanelInput> = {}): RefRow[] =>
	panelModel(refs, input(union, over))
		.find((s) => s.id === section)!
		.rows.filter((r): r is RefRow => r.kind === 'ref');
const marks = (r: RefRow | undefined) => r?.holders?.map((h) => h.root);

describe('the union of the branches of every root', () => {
	// Decision (ADR-0074 §3): each branch is one entry by name, with the roots that have it in the order of the log's roots (the order of the stripes), the workspace first; a remote-tracking branch is keyed by remote and name; a remote's HEAD is no branch.
	it('lists each branch once with the roots that have it, in the order of the roots', () => {
		const withHead = refsWith({}, { 'origin/HEAD': OID });
		const u = branchUnion(roots, ws, rootRefs);
		expect(u.locals.get('main')?.holders.map((h) => h.root)).toEqual(['.', 'lib']);
		expect(u.locals.get('feature/x')?.holders.map((h) => h.root)).toEqual(['.', 'lib/nested']);
		expect(u.locals.get('accessibility/fix')?.holders.map((h) => h.root)).toEqual(['lib']);
		expect(u.remotes.get('origin/main')?.holders.map((h) => h.root)).toEqual(['.', 'lib']);
		expect(u.remotes.get('origin/accessibility/fix')?.holders.map((h) => h.root)).toEqual(['lib']);
		expect(u.remotes.get('origin/accessibility/fix')).toMatchObject({ remote: 'origin', branch: 'accessibility/fix' });
		expect(branchUnion(roots, withHead, new Map()).remotes.size).toBe(0);
		// the holder carries that root's own tip
		expect(u.locals.get('feature/x')?.holders.map((h) => h.oid)).toEqual([A, NESTED]);
	});

	// Decision: a root whose refs are not read yet has nothing to list for now, and the workspace's refs not read (null) leave only the submodules'.
	it('skips a root whose refs are not read', () => {
		expect(branchUnion(roots, ws, new Map([['lib', lib]])).locals.has('feature/x')).toBe(true);
		expect(branchUnion(roots, ws, new Map([['lib', lib]])).locals.get('feature/x')?.holders.map((h) => h.root)).toEqual(['.']);
		expect(branchUnion(roots, null, rootRefs).locals.get('main')?.holders.map((h) => h.root)).toEqual(['lib']);
	});

	// Decision: a root's current branch is marked on that root's entry (a detached HEAD marks none).
	it('marks the root a branch is checked out in', () => {
		const u = branchUnion(roots, ws, rootRefs);
		expect(u.locals.get('feature/x')?.holders.map((h) => h.current)).toEqual([false, true]);
		// (the fixtures of lib and the project are both on main)
		expect(u.locals.get('main')?.holders.map((h) => h.current)).toEqual([true, true]);
		expect(u.locals.get('accessibility/fix')?.holders.map((h) => h.current)).toEqual([false]);
		const detached = refsWith({ main: OID }, {}, { head: { oid: OID, detached: true, upstreamGone: false, ahead: 0, behind: 0 } });
		expect(branchUnion(['.'], detached, new Map()).locals.get('main')?.holders[0].current).toBe(false);
	});
});

describe('the panel rows over the union', () => {
	const u = branchUnion(roots, ws, rootRefs);

	// Decision (ADR-0074 §3): Local and Remotes list the union once by name: a branch only a submodule has is a row of its own, at that root's tip, and the section's count includes it.
	it('lists a branch only a submodule has, once, and counts it', () => {
		const rows = refRows(ws, 'local', u);
		expect(rows.map((r) => r.name).sort()).toEqual(['accessibility/fix', 'feature/x', 'main']);
		const only = rows.find((r) => r.name === 'accessibility/fix')!;
		expect(marks(only)).toEqual(['lib']);
		expect(only.oid).toBe(LIB);
		expect(only.root).toBe('lib');
		expect(only.current).toBe(false);
		expect(panelModel(ws, input(u)).find((s) => s.id === 'local')!.count).toBe(3);
		// without the union the list is the workspace's own, as before
		expect(refRows(ws, 'local').map((r) => r.name).sort()).toEqual(['feature/x', 'main']);
		expect(refRows(ws, 'local')[0].holders).toBeUndefined();
	});

	// Decision (ADR-0074 §3): the workspace's own mark is there when it has the branch, so a branch with only a submodule mark is visibly submodule-only; the current marker stays the workspace's, whatever the submodules have checked out.
	it('marks every root that has a branch, the workspace included', () => {
		const rows = refRows(ws, 'local', u);
		const main = rows.find((r) => r.name === 'main')!;
		expect(marks(main)).toEqual(['.', 'lib']);
		expect(main.root).toBeUndefined();
		expect(main.oid).toBe(OID);
		expect(main.current).toBe(true);
		const feature = rows.find((r) => r.name === 'feature/x')!;
		expect(marks(feature)).toEqual(['.', 'lib/nested']);
		expect(feature.current).toBe(false); // lib/nested has it checked out; the project does not
		expect(feature.oid).toBe(A); // the workspace's tip when it has the branch
	});

	// Decision: the remote-tracking branches get the same union and marks, keyed remote/name.
	it('lists and marks the remote-tracking branches', () => {
		const rows = refRows(ws, 'remotes', u);
		expect(rows.map((r) => r.name).sort()).toEqual(['origin/accessibility/fix', 'origin/feature/x', 'origin/main']);
		expect(marks(rows.find((r) => r.name === 'origin/main'))).toEqual(['.', 'lib']);
		const only = rows.find((r) => r.name === 'origin/accessibility/fix')!;
		expect(marks(only)).toEqual(['lib']);
		expect(only).toMatchObject({ remote: 'origin', branch: 'accessibility/fix', root: 'lib', oid: LIB });
		expect(panelModel(ws, input(u)).find((s) => s.id === 'remotes')!.count).toBe(3);
	});

	// Decision: the filter and the favourites keep working over the union, and its folders are listed like the workspace's.
	it('filters the union and keeps starred branches first', () => {
		expect(refRows(ws, 'local', u, { filter: 'access' }).map((r) => r.name)).toEqual(['accessibility/fix']);
		expect(refRows(ws, 'remotes', u, { filter: 'ACCESS' }).map((r) => r.name)).toEqual(['origin/accessibility/fix']);
		const starred = panelModel(ws, input(u, { favourites: new Set(['accessibility/fix']) })).find((s) => s.id === 'local')!.rows;
		expect(starred[0]).toMatchObject({ kind: 'folder', label: 'accessibility' });
		const folders = collapsedFolders(ws, NO_FOLDER_CHOICE, u);
		expect(folders.has('local:accessibility')).toBe(true);
		expect(folders.has('remotes:origin/accessibility')).toBe(true);
		expect(collapsedFolders(ws, NO_FOLDER_CHOICE).has('local:accessibility')).toBe(false);
	});

	// Decision (the bound): ~150 remote-tracking branches over 13 roots is one pass over their refs; the panel stays quick.
	it('stays bounded for 150 remotes over 13 roots', () => {
		const many = Array.from({ length: 12 }, (_, i) => `sub/m${i}`);
		const remotesOf = (i: number) => Object.fromEntries(Array.from({ length: 150 }, (_, k) => [`origin/team/b${k}`, `${i}`.padStart(40, '0')]));
		const refs = new Map(many.map((r, i) => [r, refsWith({ main: OID }, remotesOf(i))]));
		const start = performance.now();
		const big = branchUnion(['.', ...many], refsWith({ main: OID }, remotesOf(99)), refs);
		const rows = refRows(refsWith({ main: OID }, remotesOf(99)), 'remotes', big);
		expect(rows).toHaveLength(150);
		expect(rows.every((r) => r.holders?.length === 13)).toBe(true);
		expect(performance.now() - start).toBeLessThan(1500);
	});
});

describe('a branch only a submodule has', () => {
	const onlyLib: BranchTarget = { kind: 'local', name: 'accessibility/fix', oid: LIB, onlyIn: 'lib' };

	// Decision (ADR-0074 §3): Checkout and New branch from are offered for it; Compare with current, which reads the project's commits, is not, whatever the project's HEAD.
	it('offers Checkout and New branch from, and not Compare with current', () => {
		expect(entryAction('checkout', onlyLib)).toMatchObject({ kind: 'checkout', target: onlyLib });
		expect(entryAction('new-branch-from', onlyLib)).toMatchObject({ kind: 'new-branch', source: { oid: LIB, onlyIn: 'lib', label: 'accessibility/fix' } });
		expect(entryAction('compare', onlyLib)).toBeNull();
		expect(canCompare(onlyLib, null)).toBe(false);
		expect(canCompare(onlyLib, refsWith({}).head)).toBe(false);
		expect(canCompare({ ...onlyLib, onlyIn: undefined }, null)).toBe(true);
		expect(sourceOf(onlyLib)?.onlyIn).toBe('lib');
	});

	// Decision (ADR-0074 §3): the menu shows Checkout and New branch from enabled, the entries that only make sense for the project disabled, and says why Compare is off.
	it('shows the menu with only those entries enabled', () => {
		const note = 'Compare with current: the project does not have this branch';
		const body = render(BranchMenu, { props: { name: 'accessibility/fix', canCompare: false, compareNote: note, onchoose: () => {} } }).body;
		const item = (id: string) => new RegExp(`<button[^>]*data-menu-item="${id}"[^>]*>`).exec(body)?.[0] ?? '';
		expect(item('checkout')).not.toMatch(/\sdisabled/);
		expect(item('new-branch-from')).not.toMatch(/\sdisabled/);
		for (const id of ['compare', 'merge', 'rebase', 'push', 'rename', 'delete']) expect(item(id), id).toMatch(/\sdisabled/);
		expect(item('compare')).toContain(note);
	});
});

// --- the panel's markup over a store with a submodule root -------------------------------------

const commit = (oid: string, time: number): Commit => ({ oid, parents: [], authorName: 'Ada', authorEmail: 'ada@example.org', commitTime: time, subject: `commit ${oid}` });

async function panel(refs: Record<string, RefsResult>, rootPaths: string[]) {
	vi.stubGlobal('window', {});
	const client = {
		call: async (name: string, params: { root?: string }) => {
			const root = params.root ?? '.';
			if (name === 'git.refs') return refs[root];
			return { commits: [commit(`${root === '.' ? 'w' : 'l'}1`, root === '.' ? 20 : 10)], tips: refs[root].tips, tipsTruncated: false, more: false };
		},
	} as unknown as HistoryClient;
	const watch = new FakeWatch();
	const store = new HistoryStore(client, { root: '/work/my-app' }, watch);
	await settle();
	store.setRoots(rootPaths);
	store.refreshRefs();
	await settle();
	const list: Result<'git.submodules'>['submodules'] = rootPaths.map((path) => ({ path, recorded: OID, checkedOut: OID, state: 'clean' as const }));
	const submodules = await submodulesOf(list);
	const fetcher = new FetchModel({ call: (() => Promise.resolve({})) as FetchClient['call'] }, { root: '/p' }, () => {});
	return {
		store,
		body: render(HistoryPanel, { props: { store, watch: watch as unknown as GitWatch, workspaceId: 'ws1', fetcher, branches: idleBranches(), submodules, updater: idleUpdater() } }).body,
	};
}
const rowOf = (body: string, ref: string) => new RegExp(`data-ref="${ref}"[\\s\\S]*?</div>`).exec(body)?.[0] ?? '';

describe('the side panel’s markup over several roots', () => {
	const wsRefs = refsWith({ main: OID, other: A });
	const libRefs = refsWith({ main: OID, a11y: LIB, 'deep/one': NESTED });

	// Decision (ADR-0074 §3): with more than one root every branch row carries a mark per root that has it, in the stripe's colour (--ri is the root's position among the log's roots) and titled with the root's name; a branch only a submodule has is a row with that one mark, and has a branch menu.
	it('marks each row with its roots and lists a submodule-only branch', async () => {
		const { body } = await panel({ '.': wsRefs, lib: libRefs }, ['lib']);
		const main = rowOf(body, 'refs/heads/main');
		expect([...main.matchAll(/data-root-mark="([^"]+)"/g)].map((m) => m[1])).toEqual(['.', 'lib']);
		const mark = (row: string, root: string) => new RegExp(`<span[^>]*data-root-mark="${root}"[^>]*>`).exec(row)?.[0] ?? '';
		expect(mark(main, '.')).toMatch(/title="my-app[^"]*"/);
		expect(mark(main, '.')).toContain('--ri: 0');
		expect(mark(main, 'lib')).toMatch(/title="lib[^"]*"/);
		expect(mark(main, 'lib')).toContain('--ri: 1');
		// the submodule-only branch is a row of its own with the one mark, and a menu
		const only = rowOf(body, 'refs/heads/a11y');
		expect([...only.matchAll(/data-root-mark="([^"]+)"/g)].map((m) => m[1])).toEqual(['lib']);
		expect(only).toContain('data-branch-menu');
		expect(only).toContain(`data-oid="${LIB}"`);
		// the project's branch that no submodule has carries the project's mark alone
		expect([...rowOf(body, 'refs/heads/other').matchAll(/data-root-mark="([^"]+)"/g)].map((m) => m[1])).toEqual(['.']);
		expect(/data-refs-section="local"[^>]*data-count="(\d+)"/.exec(body)?.[1]).toBe('4');
		// folders start closed (R1) over the union too
		expect(body).toMatch(/aria-expanded="false"[^>]*data-folder="local:deep"/);
	});

	// Decision (ADR-0074 §3): a branch without a folder shows in the list; the current marker is the workspace's branch only.
	it('keeps the current marker on the workspace’s branch', async () => {
		const { body } = await panel({ '.': wsRefs, lib: refsWith({ main: OID, extra: LIB }, {}, { head: { oid: LIB, branch: 'extra', detached: false, upstreamGone: false, ahead: 0, behind: 0 } }) }, ['lib']);
		const extra = rowOf(body, 'refs/heads/extra');
		expect(extra).toContain('data-root-mark="lib"');
		expect(extra).not.toContain('data-root-mark="."');
		expect(extra).not.toContain('data-current-mark');
		expect(extra).toContain('data-branch-menu');
		expect(extra).toContain('data-root-current');
		expect(rowOf(body, 'refs/heads/main')).toContain('data-current-mark');
	});

	// Decision (ADR-0074 §3): the marks appear only when the log has more than one root.
	it('draws no marks with one root', async () => {
		const { body } = await panel({ '.': wsRefs }, []);
		expect(body).not.toContain('data-root-mark');
		expect(body).not.toContain('data-root-marks');
		expect(rowOf(body, 'refs/heads/main')).toContain('data-current-mark');
	});
});

describe('selecting a submodule-only branch', () => {
	// Decision (ADR-0074 §3): a click selects that root's commit (its row key is root:oid), not the project's; the tip is found among the rows loaded.
	it('reveals the commit of the root that has the branch', async () => {
		vi.stubGlobal('window', {});
		const { store } = await panel({ '.': refsWith({ main: OID }), lib: refsWith({ 'accessibility/fix': 'l1' }) }, ['lib']);
		expect(store.view.rows.map((r) => r.key)).toEqual(['w1', 'lib:l1']);
		expect(await store.selectRef('refs/heads/accessibility/fix', 'l1', 'lib')).toBe(true);
		expect(store.view.selected).toBe('lib:l1');
		expect(store.view.selectedRef).toBe('refs/heads/accessibility/fix');
		// the workspace's own, as before
		expect(await store.selectRef('refs/heads/main', 'w1')).toBe(true);
		expect(store.view.selected).toBe('w1');
		// a commit not loaded says so
		expect(await store.selectRef('refs/heads/accessibility/fix', 'zz', 'lib')).toBe(false);
		expect(store.view.notice).not.toBeNull();
	});
});
