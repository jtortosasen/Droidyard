import { describe, expect, it } from 'vitest';
import type { KeyValueStorage } from '../ui/layout';
import { FAVOURITES_PREFIX, badgesByOid, collapsedFolders, currentBranchFolders, headDisplay, localFoldersOf, toggledFolder, withOpened, loadFavourites, panelModel, refNames, saveFavourites, toggled, type PanelInput, type PanelRow, type RefsResult } from './refs';
import { refsOf } from './historytesting';

const input = (over: Partial<PanelInput> = {}): PanelInput => ({ filter: '', favourites: new Set(), collapsed: new Set(), ...over });

/** A repository with folders, an upstream, tags, a work tree and origin/HEAD. */
function repo(over: Partial<RefsResult> = {}): RefsResult {
	return refsOf(['m1'], {
		head: { oid: 'm1', branch: 'main', detached: false, upstream: 'origin/main', upstreamGone: false, ahead: 2, behind: 1 },
		branches: [
			{ name: 'main', oid: 'm1', upstream: 'origin/main' },
			{ name: 'bugfix/x', oid: 'b1' },
			{ name: 'bugfix/y', oid: 'b2' },
			{ name: 'feature/ui/login', oid: 'f1' },
			{ name: 'chore/deps', oid: 'c1' },
			{ name: 'zeta', oid: 'z1' },
		],
		remotes: [
			{ remote: 'origin', name: 'HEAD', oid: 'm1' },
			{ remote: 'origin', name: 'main', oid: 'm1' },
			{ remote: 'origin', name: 'dev', oid: 'd1' },
			{ remote: 'origin', name: 'bugfix/x', oid: 'b0' },
		],
		tags: [
			{ name: 'v2', oid: 'm1' },
			{ name: 'v1', oid: 'c1' },
			{ name: 'blobtag' },
		],
		worktrees: [
			{ path: '/work/app', branch: 'main', oid: 'm1', current: true },
			{ path: '/work/app-fix', branch: 'bugfix/x', oid: 'b1', current: false },
		],
		...over,
	});
}

const labels = (rows: PanelRow[]) => rows.map((r) => `${'  '.repeat(r.kind === 'worktree' ? 0 : r.depth)}${r.kind === 'folder' ? r.label + '/' : r.label}`);
const section = (id: string, over: Partial<PanelInput> = {}, refs = repo()) => panelModel(refs, input(over)).find((s) => s.id === id)!;

describe('panelModel', () => {
	// Decision (item 7): local branches are a tree of folders by prefix; folders before branches, then by name, case-insensitive.
	it('puts local branches in folders by prefix', () => {
		expect(labels(section('local').rows)).toEqual([
			'bugfix/',
			'  x',
			'  y',
			'chore/',
			'  deps',
			'feature/',
			'  ui/',
			'    login',
			'main',
			'zeta',
		]);
	});

	// Decision (item 7): favourites come first at their level, and a folder holding one comes before the others.
	it('lists starred favourites first', () => {
		const rows = section('local', { favourites: new Set(['zeta', 'feature/ui/login']) }).rows;
		expect(labels(rows)).toEqual(['feature/', '  ui/', '    login', 'zeta', 'bugfix/', '  x', '  y', 'chore/', '  deps', 'main']);
		const zeta = rows.find((r) => r.kind === 'ref' && r.name === 'zeta');
		expect(zeta?.kind === 'ref' && zeta.starred).toBe(true);
		const main = rows.find((r) => r.kind === 'ref' && r.name === 'main');
		expect(main?.kind === 'ref' && main.starred).toBe(false);
		// A favourite that is not a branch (it was deleted) lists nothing.
		expect(section('local', { favourites: new Set(['gone']) }).count).toBe(6);
	});

	// Decision (item 4): the current branch is marked; a detached HEAD marks none.
	it('marks the current branch', () => {
		const cur = section('local').rows.filter((r) => r.kind === 'ref' && r.current);
		expect(cur.map((r) => r.kind === 'ref' && r.ref)).toEqual(['refs/heads/main']);
		const detached = repo({ head: { oid: 'm1', detached: true, upstreamGone: false, ahead: 0, behind: 0 } });
		expect(section('local', {}, detached).rows.some((r) => r.kind === 'ref' && r.current)).toBe(false);
	});

	// Decision (item 4): each section carries its count, origin/HEAD is not a branch, and the tag on a tree is listed.
	it('counts each section and leaves origin/HEAD out', () => {
		const counts = Object.fromEntries(panelModel(repo(), input()).map((s) => [s.id, s.count]));
		expect(counts).toEqual({ local: 6, remotes: 3, tags: 3, worktrees: 2 });
		const remotes = section('remotes');
		expect(labels(remotes.rows)).toEqual(['origin/', '  bugfix/', '    x', '  dev', '  main']);
		expect(remotes.rows.some((r) => r.kind === 'ref' && r.name.endsWith('/HEAD'))).toBe(false);
		expect(refNames(repo()).has('refs/remotes/origin/HEAD')).toBe(false);
	});

	// Decision: a folder carries the number of refs below it, and a closed one hides them; a filter opens every folder.
	it('closes folders, and a filter opens them', () => {
		const closed = section('local', { collapsed: new Set(['local:bugfix', 'local:feature']) });
		expect(labels(closed.rows)).toEqual(['bugfix/', 'chore/', '  deps', 'feature/', 'main', 'zeta']);
		const bugfix = closed.rows[0];
		expect(bugfix.kind === 'folder' && bugfix.count).toBe(2);
		expect(bugfix.kind === 'folder' && bugfix.expanded).toBe(false);
		expect(closed.count).toBe(6); // the count is of refs, folders closed or not
		const found = section('local', { collapsed: new Set(['local:feature', 'local:feature/ui']), filter: 'LOGIN' });
		expect(labels(found.rows)).toEqual(['feature/', '  ui/', '    login']);
	});

	// Decision (item 4): the filter is case-insensitive over branches, remotes, tags and work trees, and counts what it lists.
	it('filters branches, tags and work trees case-insensitively', () => {
		const model = panelModel(repo(), input({ filter: ' Bugfix ' }));
		expect(model.map((s) => [s.id, s.count])).toEqual([
			['local', 2],
			['remotes', 1],
			['tags', 0],
			['worktrees', 1],
		]);
		expect(labels(model[0].rows)).toEqual(['bugfix/', '  x', '  y']);
		expect(panelModel(repo(), input({ filter: 'V1' })).find((s) => s.id === 'tags')?.rows.map((r) => r.kind === 'ref' && r.name)).toEqual(['v1']);
		expect(panelModel(repo(), input({ filter: 'app-fix' })).find((s) => s.id === 'worktrees')?.count).toBe(1);
		expect(panelModel(repo(), input({ filter: 'nothing like this' })).every((s) => s.count === 0)).toBe(true);
	});

	// Decision: the hooks name the full ref, so a local branch, a remote branch and a tag of one name cannot be confused.
	it('names each ref in full', () => {
		const refs = refsOf(['a'], {
			branches: [{ name: 'v1', oid: 'a' }],
			remotes: [{ remote: 'origin', name: 'v1', oid: 'a' }],
			tags: [{ name: 'v1', oid: 'a' }],
		});
		const names = panelModel(refs, input()).flatMap((s) => s.rows.map((r) => (r.kind === 'ref' ? r.ref : '')));
		expect(names.filter(Boolean)).toEqual(['refs/heads/v1', 'refs/remotes/origin/v1', 'refs/tags/v1']);
	});

	it('shows a work tree by its folder name, with its branch', () => {
		const wt = section('worktrees').rows[0];
		expect(wt.kind === 'worktree' && wt.label).toBe('app');
		expect(wt.kind === 'worktree' && wt.branch).toBe('main');
		expect(wt.kind === 'worktree' && wt.current).toBe(true);
	});
});

describe('badgesByOid', () => {
	// Decision (item 10): a local branch and its remote at one commit are ONE badge, "main | origin"; origin/HEAD is not a badge.
	it('joins a local branch with its remote at the same commit', () => {
		const m = badgesByOid(repo()).get('m1')!;
		expect(m.map((b) => [b.kind, b.label, b.current])).toEqual([
			['branch', 'main | origin', true],
			['tag', 'v2', false],
		]);
		expect(m[0].remotes).toEqual(['origin']);
		expect(m.some((b) => b.label.includes('HEAD'))).toBe(false);
	});

	// Decision: a remote branch that is not at its local branch's commit is a badge of its own, where it points.
	it('keeps a remote branch that points elsewhere as its own badge', () => {
		const map = badgesByOid(repo());
		expect(map.get('b0')!.map((b) => [b.kind, b.label])).toEqual([['remote', 'origin/bugfix/x']]);
		expect(map.get('b1')!.map((b) => [b.kind, b.label])).toEqual([['branch', 'bugfix/x']]);
		expect(map.get('d1')!.map((b) => [b.kind, b.label])).toEqual([['remote', 'origin/dev']]);
	});

	// Decision (item 10): a tag has its own badge kind (the panel draws the tag icon); a tag on a tree has no commit to sit on.
	it('gives a tag a badge of its own and skips a tag with no commit', () => {
		const map = badgesByOid(repo());
		expect(map.get('c1')!.map((b) => [b.kind, b.label])).toEqual([
			['branch', 'chore/deps'],
			['tag', 'v1'],
		]);
		expect([...map.values()].flat().some((b) => b.label === 'blobtag')).toBe(false);
	});

	// Decision: a remote joins by the branch's configured upstream even under another name.
	it('joins by the configured upstream', () => {
		const refs = refsOf(['a'], {
			branches: [{ name: 'local-name', oid: 'a', upstream: 'fork/other' }],
			remotes: [{ remote: 'fork', name: 'other', oid: 'a' }],
		});
		expect(badgesByOid(refs).get('a')!.map((b) => b.label)).toEqual(['local-name | fork']);
	});

	// Decision: a detached HEAD is a badge on its commit, before the others.
	it('marks a detached HEAD', () => {
		const refs = refsOf(['a'], { head: { oid: 'a', detached: true, upstreamGone: false, ahead: 0, behind: 0 }, branches: [{ name: 'main', oid: 'a' }] });
		expect(badgesByOid(refs).get('a')!.map((b) => [b.kind, b.label])).toEqual([
			['head', 'HEAD'],
			['branch', 'main'],
		]);
	});
});

describe('headDisplay', () => {
	it('names the branch with its ahead and behind, and a detached HEAD by its commit', () => {
		const d = headDisplay(repo().head);
		expect(d).toMatchObject({ name: 'main', detached: false, upstream: 'origin/main', ahead: 2, behind: 1, upstreamGone: false });
		expect(headDisplay({ oid: 'abcdef0123', detached: true, upstreamGone: false, ahead: 0, behind: 0 }).name).toBe('HEAD detached at abcdef0');
		expect(headDisplay({ branch: 'new', detached: false, upstreamGone: false, ahead: 0, behind: 0 }).name).toBe('new');
	});
});

describe('favourites', () => {
	class Mem implements KeyValueStorage {
		data = new Map<string, string>();
		getItem(k: string) {
			return this.data.get(k) ?? null;
		}
		setItem(k: string, v: string) {
			this.data.set(k, v);
		}
	}

	// Decision: favourites are per browser AND project.
	it('remembers them per project', () => {
		const s = new Mem();
		saveFavourites(s, 'p1', new Set(['main', 'feature/a']));
		expect([...loadFavourites(s, 'p1')]).toEqual(['main', 'feature/a']);
		expect(loadFavourites(s, 'p2').size).toBe(0);
		expect([...toggled(new Set(['a']), 'b')]).toEqual(['a', 'b']);
		expect([...toggled(new Set(['a', 'b']), 'a')]).toEqual(['b']);
	});

	// Decision (as tree.ts's prefs): what is stored is checked on the way in, and a storage that throws is not an error.
	it('checks what it reads and survives a storage that throws', () => {
		const s = new Mem();
		for (const bad of ['not json', 'null', '7', '{"branches":"main"}', '{"branches":[1,null,{},""]}']) {
			s.data.set(FAVOURITES_PREFIX + 'p', bad);
			expect(loadFavourites(s, 'p').size).toBe(0);
		}
		s.data.set(FAVOURITES_PREFIX + 'p', JSON.stringify({ branches: ['ok', 7, 'x'.repeat(2000)] }));
		expect([...loadFavourites(s, 'p')]).toEqual(['ok']);
		const boom: KeyValueStorage = {
			getItem() {
				throw new Error('blocked');
			},
			setItem() {
				throw new Error('blocked');
			},
		};
		expect(loadFavourites(boom, 'p').size).toBe(0);
		expect(() => saveFavourites(boom, 'p', new Set(['a']))).not.toThrow();
		expect(loadFavourites(null, 'p').size).toBe(0);
		expect(() => saveFavourites(null, 'p', new Set(['a']))).not.toThrow();
	});
});

describe('the folders of the panel (R1)', () => {
	const current = (branch: string) => repo({ head: { oid: 'm1', branch, detached: false, upstreamGone: false, ahead: 0, behind: 0 } });
	const NONE = { opened: new Set<string>(), closed: new Set<string>() };

	// Decision (R1): branch folders start closed, except those along the current branch's path.
	it('closes every folder except the ones that hold the current branch', () => {
		const refs = current('feature/ui/login');
		const collapsed = collapsedFolders(refs, NONE);
		expect(collapsed.has('local:bugfix')).toBe(true);
		expect(collapsed.has('local:chore')).toBe(true);
		expect(collapsed.has('remotes:origin')).toBe(true);
		expect(collapsed.has('local:feature')).toBe(false);
		expect(collapsed.has('local:feature/ui')).toBe(false);
		const rows = labels(panelModel(refs, { filter: '', favourites: new Set(), collapsed }).find((s) => s.id === 'local')!.rows);
		expect(rows).toEqual(['bugfix/', 'chore/', 'feature/', '  ui/', '    login', 'main', 'zeta']);
	});

	// Decision (R1): a detached HEAD, or a branch outside any folder, opens nothing.
	it('opens nothing for a detached HEAD or a branch with no folder', () => {
		const detached = repo({ head: { oid: 'm1', detached: true, upstreamGone: false, ahead: 0, behind: 0 } });
		expect(currentBranchFolders(detached).size).toBe(0);
		expect(collapsedFolders(detached, NONE).has('local:feature')).toBe(true);
		expect(collapsedFolders(current('main'), NONE).has('local:feature')).toBe(true);
	});

	// Decision (R1): the person's own toggles keep working: a folder opened stays open, and the default one closed stays closed.
	it('keeps the person\'s opening and closing over the default', () => {
		const refs = current('feature/ui/login');
		let choice = toggledFolder(NONE, 'local:bugfix', false); // a click on a closed folder opens it
		expect(collapsedFolders(refs, choice).has('local:bugfix')).toBe(false);
		choice = toggledFolder(choice, 'local:feature', true); // a click on an open folder closes it
		expect(collapsedFolders(refs, choice).has('local:feature')).toBe(true);
		choice = toggledFolder(choice, 'local:bugfix', true);
		expect(collapsedFolders(refs, choice).has('local:bugfix')).toBe(true);
	});

	// Decision (R1): Navigate to HEAD opens the folders of the branch it selects, even ones the person closed.
	it('opens the folders along a ref, over a closing', () => {
		const refs = current('main');
		const closed = toggledFolder(NONE, 'local:feature/ui', true);
		const choice = withOpened(closed, localFoldersOf('refs/heads/feature/ui/login'));
		const collapsed = collapsedFolders(refs, choice);
		expect(collapsed.has('local:feature')).toBe(false);
		expect(collapsed.has('local:feature/ui')).toBe(false);
		expect(localFoldersOf('refs/remotes/origin/dev')).toEqual([]);
	});

	// Decision (R1): a branch that is also a folder (a, a/b) is listed when it is current.
	it('opens a current branch that is also a folder', () => {
		const refs = repo({
			head: { oid: 'm1', branch: 'a', detached: false, upstreamGone: false, ahead: 0, behind: 0 },
			branches: [
				{ name: 'a', oid: 'm1' },
				{ name: 'a/b', oid: 'b1' },
			],
		});
		expect(collapsedFolders(refs, NONE).has('local:a')).toBe(false);
	});
});
