// The side panel's model and the ref badges (plan m2n step 6; the owner's items 4, 7, 10),
// pure over `git.refs`' result:
//   - the four sections (Local branches, Remotes, Tags, Worktrees) with their counts, the
//     branches in folders by `/`, starred favourites first, a case-insensitive filter;
//   - the badges each commit row carries: a local branch joined with its remote when both
//     point at it ("main | origin"), the other remote branches, the tags, HEAD when detached;
//   - the favourites, remembered per browser and project, every read and write in try/catch
//     and checked on the way in (as tree.ts's prefs are).
// A remote's `HEAD` pointer (origin/HEAD) is not a branch: it is in neither the panel nor the badges.
// Every name here is git's text; nothing becomes markup.
import type { Result } from '../api/capabilities.gen';
import { rootKey } from '../projects/projects';
import type { KeyValueStorage } from '../ui/layout';
import { firstHolder, inWorkspace, type BranchUnion, type Holder } from './branchroots';

export type RefsResult = Result<'git.refs'>;
export type RefsHead = RefsResult['head'];

export type SectionId = 'local' | 'remotes' | 'tags' | 'worktrees';

export const SECTION_TITLES: Readonly<Record<SectionId, string>> = {
	local: 'LOCAL BRANCHES',
	remotes: 'REMOTES',
	tags: 'TAGS',
	worktrees: 'WORKTREES',
};

export const SECTIONS: readonly SectionId[] = ['local', 'remotes', 'tags', 'worktrees'];

/** The full ref names the panel and the table's hooks use. */
export const localRef = (name: string) => `refs/heads/${name}`;
export const remoteRef = (remote: string, name: string) => `refs/remotes/${remote}/${name}`;
export const tagRef = (name: string) => `refs/tags/${name}`;

export interface FolderRow {
	kind: 'folder';
	/** Unique within the panel; what the collapsed set holds. */
	key: string;
	label: string;
	depth: number;
	expanded: boolean;
	/** Refs below it. */
	count: number;
}

export interface RefRow {
	kind: 'ref';
	key: string;
	/** The full ref name (refs/heads/x). */
	ref: string;
	/** The last path segment in a folder tree, the whole name in a flat list. */
	label: string;
	/** The whole name, for the title. */
	name: string;
	depth: number;
	/** The commit it points at; absent for a tag on a tree or a blob. */
	oid?: string;
	/** Remote-tracking branches only: the remote's name and the branch's name on it (`name` is both, joined). */
	remote?: string;
	branch?: string;
	/** Local branches only. */
	starred?: boolean;
	current?: boolean;
	upstream?: string;
	upstreamGone?: boolean;
	/**
	 * With more than one root (plan m2n R2): the roots that have the branch, for the coloured marks; absent with one root
	 * and for tags. When the workspace lacks the branch, `oid` is the first of those roots' and `root` names it.
	 */
	holders?: readonly Holder[];
	/** The root whose commit `oid` is, when it is not the workspace's. */
	root?: string;
}

export interface WorktreeRow {
	kind: 'worktree';
	key: string;
	path: string;
	label: string;
	branch?: string;
	oid?: string;
	current: boolean;
	/** The sessions whose workspace root is this path (ADR-0083 §4): names only, no action. */
	sessions: readonly string[];
}

export type PanelRow = FolderRow | RefRow | WorktreeRow;

export interface Section {
	id: SectionId;
	title: string;
	/** The refs listed (those that pass the filter). */
	count: number;
	rows: PanelRow[];
}

export interface PanelInput {
	/** Text typed in the filter box; empty shows everything. */
	filter: string;
	/** Names of the starred local branches. */
	favourites: ReadonlySet<string>;
	/** Keys of the folders the person closed. A filter opens every folder. */
	collapsed: ReadonlySet<string>;
	/** The branches of every root (branchroots.ts), when the log has more than one: the lists show their union with a mark per root. */
	union?: BranchUnion;
	/** The labels of the sessions at each worktree root (projects.ts sessionsByRoot), keyed by the root without a trailing separator. */
	worktreeSessions?: ReadonlyMap<string, readonly string[]>;
}

interface Leaf<T> {
	name: string;
	item: T;
	starred: boolean;
}

interface Node<T> {
	segment: string;
	path: string;
	kids: Map<string, Node<T>>;
	leaf?: Leaf<T>;
	leaves: number;
	starred: boolean;
}

const lower = (s: string) => s.toLowerCase();
const byName = (a: string, b: string) => {
	const x = lower(a);
	const y = lower(b);
	return x < y ? -1 : x > y ? 1 : a < b ? -1 : a > b ? 1 : 0;
};

/** Folders by `/`: a folder holding a starred branch, and starred branches, come first at each level; folders before branches; then by name. */
function fold<T>(
	leaves: readonly Leaf<T>[],
	section: SectionId,
	input: PanelInput,
	row: (leaf: Leaf<T>, label: string, depth: number) => RefRow,
): PanelRow[] {
	const root: Node<T> = { segment: '', path: '', kids: new Map(), leaves: 0, starred: false };
	for (const leaf of leaves) {
		let at = root;
		at.leaves++;
		at.starred ||= leaf.starred;
		let path = '';
		for (const segment of leaf.name.split('/')) {
			path = path === '' ? segment : `${path}/${segment}`;
			let next = at.kids.get(segment);
			if (next === undefined) {
				next = { segment, path, kids: new Map(), leaves: 0, starred: false };
				at.kids.set(segment, next);
			}
			next.leaves++;
			next.starred ||= leaf.starred;
			at = next;
		}
		at.leaf = leaf;
	}
	const open = input.filter.trim() !== '';
	const out: PanelRow[] = [];
	const visit = (node: Node<T>, depth: number) => {
		const kids = [...node.kids.values()].sort((a, b) => {
			const fa = a.kids.size > 0 ? 0 : 1;
			const fb = b.kids.size > 0 ? 0 : 1;
			// Starred first; among the starred and among the rest, folders then branches, then by name.
			return Number(b.starred) - Number(a.starred) || fa - fb || byName(a.segment, b.segment);
		});
		for (const kid of kids) {
			if (kid.kids.size === 0 && kid.leaf) {
				out.push(row(kid.leaf, kid.segment, depth));
				continue;
			}
			const key = `${section}:${kid.path}`;
			const expanded = open || !input.collapsed.has(key);
			out.push({ kind: 'folder', key, label: kid.segment, depth, expanded, count: kid.leaves });
			if (expanded) {
				if (kid.leaf) out.push(row(kid.leaf, kid.segment, depth + 1));
				visit(kid, depth + 1);
			}
		}
	};
	visit(root, 0);
	return out;
}

/** The sections the side panel shows for `refs` under the filter, favourites and closed folders in `input`. */
export function panelModel(refs: RefsResult, input: PanelInput): Section[] {
	const q = lower(input.filter.trim());
	const matches = (...texts: (string | undefined)[]) => q === '' || texts.some((t) => t !== undefined && lower(t).includes(q));
	const current = refs.head.detached ? undefined : refs.head.branch;

	const union = input.union;
	type Local = RefsResult['branches'][number];
	const locals: Leaf<Local>[] = refs.branches
		.filter((b) => matches(b.name))
		.map((b) => ({ name: b.name, item: b, starred: input.favourites.has(b.name) }));
	// A branch only a submodule has is listed as well (plan m2n R2), at that root's commit.
	for (const entry of union?.locals.values() ?? []) {
		if (inWorkspace(entry) || !matches(entry.name)) continue;
		locals.push({ name: entry.name, item: { name: entry.name, oid: firstHolder(entry).oid }, starred: input.favourites.has(entry.name) });
	}
	const localRows = fold(locals, 'local', input, (leaf, label, depth) => {
		const b = leaf.item;
		const entry = union?.locals.get(b.name);
		const away = entry !== undefined && !inWorkspace(entry) ? firstHolder(entry) : undefined;
		return {
			kind: 'ref',
			key: `local:${b.name}`,
			ref: localRef(b.name),
			label,
			name: b.name,
			depth,
			oid: b.oid,
			starred: leaf.starred,
			current: away === undefined && b.name === current,
			...(b.upstream !== undefined ? { upstream: b.upstream } : {}),
			...(b.upstreamGone ? { upstreamGone: true } : {}),
			...(entry !== undefined ? { holders: entry.holders } : {}),
			...(away !== undefined ? { root: away.root } : {}),
		};
	});

	type Remote = RefsResult['remotes'][number];
	const remotes: Leaf<Remote>[] = refs.remotes
		.filter((r) => r.name !== 'HEAD')
		.filter((r) => matches(`${r.remote}/${r.name}`))
		.map((r) => ({ name: `${r.remote}/${r.name}`, item: r, starred: false }));
	for (const entry of union?.remotes.values() ?? []) {
		if (inWorkspace(entry) || !matches(entry.name)) continue;
		remotes.push({ name: entry.name, item: { remote: entry.remote!, name: entry.branch!, oid: firstHolder(entry).oid }, starred: false });
	}
	const remoteRows = fold(remotes, 'remotes', input, (leaf, label, depth) => {
		const entry = union?.remotes.get(leaf.name);
		const away = entry !== undefined && !inWorkspace(entry) ? firstHolder(entry) : undefined;
		return {
			kind: 'ref',
			key: `remotes:${leaf.name}`,
			ref: remoteRef(leaf.item.remote, leaf.item.name),
			label,
			name: leaf.name,
			depth,
			oid: leaf.item.oid,
			remote: leaf.item.remote,
			branch: leaf.item.name,
			...(entry !== undefined ? { holders: entry.holders } : {}),
			...(away !== undefined ? { root: away.root } : {}),
		};
	});

	const tagRows: PanelRow[] = refs.tags
		.filter((t) => matches(t.name))
		.map((t) => ({
			kind: 'ref',
			key: `tags:${t.name}`,
			ref: tagRef(t.name),
			label: t.name,
			name: t.name,
			depth: 0,
			...(t.oid !== undefined ? { oid: t.oid } : {}),
		}));

	const worktreeRows: PanelRow[] = refs.worktrees
		.filter((w) => matches(w.path, w.branch))
		.map((w) => ({
			kind: 'worktree',
			key: `worktrees:${w.path}`,
			path: w.path,
			label: w.path.replace(/[\\/]+$/, '').split(/[\\/]/).pop() || w.path,
			...(w.branch !== undefined ? { branch: w.branch } : {}),
			...(w.oid !== undefined ? { oid: w.oid } : {}),
			current: w.current,
			sessions: input.worktreeSessions?.get(rootKey(w.path)) ?? [],
		}));

	return [
		{ id: 'local', title: SECTION_TITLES.local, count: locals.length, rows: localRows },
		{ id: 'remotes', title: SECTION_TITLES.remotes, count: remotes.length, rows: remoteRows },
		{ id: 'tags', title: SECTION_TITLES.tags, count: tagRows.length, rows: tagRows },
		{ id: 'worktrees', title: SECTION_TITLES.worktrees, count: worktreeRows.length, rows: worktreeRows },
	];
}

/** Every full ref name the panel can select, to drop a selection whose ref has gone. */
export function refNames(refs: RefsResult): Set<string> {
	const out = new Set<string>();
	for (const b of refs.branches) out.add(localRef(b.name));
	for (const r of refs.remotes) if (r.name !== 'HEAD') out.add(remoteRef(r.remote, r.name));
	for (const t of refs.tags) out.add(tagRef(t.name));
	return out;
}

// --- the badges on the commit rows ------------------------------------------------------

export type BadgeKind = 'head' | 'branch' | 'remote' | 'tag';

export interface Badge {
	kind: BadgeKind;
	/** "main | origin" for a branch joined with its remote, "origin/dev" for a remote alone, a tag's name, "HEAD". */
	label: string;
	/** The checked-out branch (or HEAD). */
	current: boolean;
	/** The full ref name; a joined badge names its local branch. */
	ref: string;
	/** The remotes a joined branch carries. */
	remotes?: string[];
}

const KIND_ORDER: Record<BadgeKind, number> = { head: 0, branch: 1, remote: 2, tag: 3 };

/**
 * The badges per commit, by oid. A remote-tracking branch joins the local branch at the same
 * commit when it is that branch's upstream or has its name; it is then not a badge of its own.
 */
export function badgesByOid(refs: RefsResult): Map<string, Badge[]> {
	const out = new Map<string, Badge[]>();
	const add = (oid: string, badge: Badge) => {
		const list = out.get(oid);
		if (list) list.push(badge);
		else out.set(oid, [badge]);
	};
	const remotes = refs.remotes.filter((r) => r.name !== 'HEAD');
	const joined = new Set<(typeof remotes)[number]>();
	const current = refs.head.detached ? undefined : refs.head.branch;

	if (refs.head.detached && refs.head.oid !== undefined) {
		add(refs.head.oid, { kind: 'head', label: 'HEAD', current: true, ref: 'HEAD' });
	}
	for (const b of refs.branches) {
		const mine = remotes.filter((r) => r.oid === b.oid && (`${r.remote}/${r.name}` === b.upstream || r.name === b.name));
		for (const r of mine) joined.add(r);
		const names = [...new Set(mine.map((r) => r.remote))];
		add(b.oid, {
			kind: 'branch',
			label: names.length === 0 ? b.name : `${b.name} | ${names.join(', ')}`,
			current: b.name === current,
			ref: localRef(b.name),
			...(names.length > 0 ? { remotes: names } : {}),
		});
	}
	for (const r of remotes) {
		if (joined.has(r)) continue;
		add(r.oid, { kind: 'remote', label: `${r.remote}/${r.name}`, current: false, ref: remoteRef(r.remote, r.name) });
	}
	for (const t of refs.tags) {
		if (t.oid === undefined) continue;
		add(t.oid, { kind: 'tag', label: t.name, current: false, ref: tagRef(t.name) });
	}
	for (const list of out.values()) {
		// Stable: the current branch before the other branches, then the order given (names, remotes, newest tags).
		list.sort((a, b) => KIND_ORDER[a.kind] - KIND_ORDER[b.kind] || Number(b.current) - Number(a.current));
	}
	return out;
}

// --- the current branch's display ---------------------------------------------------------

export interface HeadDisplay {
	/** The branch's name, or "HEAD detached at abc1234". */
	name: string;
	detached: boolean;
	upstream?: string;
	upstreamGone: boolean;
	ahead: number;
	behind: number;
}

export function headDisplay(head: RefsHead): HeadDisplay {
	const name = head.detached ? `HEAD detached at ${(head.oid ?? '').slice(0, 7)}`.trimEnd() : (head.branch ?? 'HEAD');
	return {
		name,
		detached: head.detached,
		...(head.upstream !== undefined ? { upstream: head.upstream } : {}),
		upstreamGone: head.upstreamGone,
		ahead: head.ahead,
		behind: head.behind,
	};
}

// --- the favourites -----------------------------------------------------------------------

export const FAVOURITES_PREFIX = 'droidyard.ui.gitfavs.';
/** What is read back: a stored list longer than this, or a name longer than `MAX_NAME`, is not ours. */
const MAX_FAVOURITES = 5000;
const MAX_NAME = 1024;

/** The starred local branches stored for this project; anything that is not a list of short strings is none. */
export function loadFavourites(storage: KeyValueStorage | null, project: string): Set<string> {
	if (storage === null) return new Set();
	try {
		const raw = storage.getItem(FAVOURITES_PREFIX + project);
		if (raw === null) return new Set();
		const v: unknown = JSON.parse(raw);
		if (typeof v !== 'object' || v === null) return new Set();
		const list = (v as Record<string, unknown>).branches;
		if (!Array.isArray(list)) return new Set();
		const out = new Set<string>();
		for (const name of list.slice(0, MAX_FAVOURITES)) {
			if (typeof name === 'string' && name !== '' && name.length <= MAX_NAME) out.add(name);
		}
		return out;
	} catch {
		return new Set();
	}
}

export function saveFavourites(storage: KeyValueStorage | null, project: string, favourites: ReadonlySet<string>): void {
	if (storage === null) return;
	try {
		storage.setItem(FAVOURITES_PREFIX + project, JSON.stringify({ branches: [...favourites].slice(0, MAX_FAVOURITES) }));
	} catch {
		// Not remembered; what is in memory is still right.
	}
}

/** A star toggled: a new set. */
export function toggled(favourites: ReadonlySet<string>, name: string): Set<string> {
	const next = new Set(favourites);
	if (!next.delete(name)) next.add(name);
	return next;
}

// --- the folders of the side panel ----------------------------------------------------------

/** The person's own opening and closing of folders; a folder in neither follows the default. */
export interface FolderChoice {
	opened: ReadonlySet<string>;
	closed: ReadonlySet<string>;
}

export const NO_FOLDER_CHOICE: FolderChoice = { opened: new Set(), closed: new Set() };

/** Every folder key a path can make: `section:a`, `section:a/b` for `a/b/c` (the last segment is the branch, not a folder). */
function folderKeysOf(section: SectionId, name: string, into: Set<string>): void {
	const parts = name.split('/');
	let path = '';
	for (let i = 0; i < parts.length - 1; i++) {
		path = path === '' ? parts[i] : `${path}/${parts[i]}`;
		into.add(`${section}:${path}`);
	}
}

/** The folders that hold the current branch (local section): open by default. A detached HEAD has none. */
export function currentBranchFolders(refs: RefsResult): Set<string> {
	const out = new Set<string>();
	if (!refs.head.detached && refs.head.branch !== undefined) {
		folderKeysOf('local', refs.head.branch, out);
		// A branch that is also a folder (`a` beside `a/b`) is listed inside it.
		out.add(`local:${refs.head.branch}`);
	}
	return out;
}

/**
 * The folders to show closed: every folder of the local and remote branches, except those that hold the current
 * branch, and except those the person opened; and those the person closed, wherever they are (plan m2n R1).
 */
export function collapsedFolders(refs: RefsResult, choice: FolderChoice, union?: BranchUnion): Set<string> {
	const all = new Set<string>();
	for (const b of refs.branches) folderKeysOf('local', b.name, all);
	for (const r of refs.remotes) if (r.name !== 'HEAD') folderKeysOf('remotes', `${r.remote}/${r.name}`, all);
	// The branches only a submodule has sit in folders too (plan m2n R2).
	for (const name of union?.locals.keys() ?? []) folderKeysOf('local', name, all);
	for (const name of union?.remotes.keys() ?? []) folderKeysOf('remotes', name, all);
	const initial = currentBranchFolders(refs);
	const out = new Set<string>();
	for (const key of all) if (choice.closed.has(key) || !(choice.opened.has(key) || initial.has(key))) out.add(key);
	return out;
}

/** The choice after a click on a folder that is `open` now. */
export function toggledFolder(choice: FolderChoice, key: string, open: boolean): FolderChoice {
	const opened = new Set(choice.opened);
	const closed = new Set(choice.closed);
	if (open) {
		opened.delete(key);
		closed.add(key);
	} else {
		closed.delete(key);
		opened.add(key);
	}
	return { opened, closed };
}

/** The choice with these folders opened (Navigate to HEAD opens what it needs). */
export function withOpened(choice: FolderChoice, keys: Iterable<string>): FolderChoice {
	const opened = new Set(choice.opened);
	const closed = new Set(choice.closed);
	for (const k of keys) {
		closed.delete(k);
		opened.add(k);
	}
	return { opened, closed };
}

/** The folders a ref `refs/heads/...` is inside, as keys. */
export function localFoldersOf(ref: string): string[] {
	const prefix = 'refs/heads/';
	if (!ref.startsWith(prefix)) return [];
	const out = new Set<string>();
	folderKeysOf('local', ref.slice(prefix.length), out);
	return [...out];
}
