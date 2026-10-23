// The Git tree's Tree view (plan m2l step 8; ADR-0067 §5): the whole project as the file watch
// lists it, plus the status paths the watch does not (deleted files, anything under `.idea/`),
// with only the folders holding changes open at first.
//
// It is a second READER of the file watch (`FileWatch.listen`, no second `file.watch`) and of
// the git status (`setStatus`). Its expansion set is its own, in memory: opening a folder here
// never touches the Editor's `FileTreeModel`. The rules for that set:
//   - the ancestors of every changed path are open at first;
//   - a path that BECOMES changed opens its ancestors once; a folder the user closed stays
//     closed until a new change lands beneath it.
// The rows are not compacted ("src/main/java" is three rows here): a chain row would make
// "this folder is open" mean something else than it does in the status.
//
// Also here: the remembered view and grouping, per browser and project, every storage access in
// try/catch and the stored value checked on the way in.
import type { Baseline, Change, WatchListener } from '../files/watch';
import type { KeyValueStorage } from '../ui/layout';
import { LETTERS, type Entry, type Letter, type Mark } from './changes';

export interface TreeRow {
	kind: 'dir' | 'file';
	key: string;
	path: string;
	label: string;
	/** 1 for the root's children. */
	depth: number;
	/** A folder: whether its children are shown. */
	expanded: boolean;
	/** A file: it differs from HEAD. A folder: something beneath it does. */
	changed: boolean;
	mark?: Mark;
	letter?: Letter;
	deleted: boolean;
	origPath?: string;
}

type Kind = 'file' | 'dir';

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });
const order = (a: string, b: string): number => collator.compare(a, b) || (a < b ? -1 : a > b ? 1 : 0);
const nameOf = (path: string): string => path.slice(path.lastIndexOf('/') + 1);
const parentOf = (path: string): string => (path.includes('/') ? path.slice(0, path.lastIndexOf('/')) : '');

function* ancestors(path: string): Generator<string> {
	let at = parentOf(path);
	while (at !== '') {
		yield at;
		at = parentOf(at);
	}
}

export class GitTreeModel implements WatchListener {
	#listeners = new Set<() => void>();
	/** What the file watch lists: path to its kind. */
	#watched = new Map<string, Kind>();
	/** The status: the paths that differ from HEAD. */
	#status = new Map<string, Entry>();
	/** This model's own expansion set; nothing else reads or writes it. */
	#expanded = new Set<string>();
	#kids: Map<string, Map<string, Kind>> | null = null;
	#changedDirs: Set<string> | null = null;
	#rows: readonly TreeRow[] | null = null;

	get rows(): readonly TreeRow[] {
		this.#rows ??= this.#build();
		return this.#rows;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** The git status, whole: it replaces the last. A path that was not changed before opens its ancestors. */
	setStatus(entries: readonly Entry[]): void {
		const next = new Map<string, Entry>();
		for (const e of entries) next.set(e.path, e);
		for (const path of next.keys()) {
			if (this.#status.has(path)) continue;
			for (const dir of ancestors(path)) this.#expanded.add(dir);
		}
		this.#status = next;
		this.#data();
	}

	/** Opens a closed folder and closes an open one. */
	toggle(path: string): void {
		if (!this.#expanded.delete(path)) this.#expanded.add(path);
		this.#rows = null;
		this.#notify();
	}

	// --- the file watch's listener ------------------------------------------------------

	baseline(entries: Baseline): void {
		this.#watched.clear();
		for (const e of entries) this.#watched.set(e.path, e.type === 'dir' ? 'dir' : 'file');
		this.#data();
	}

	changes(list: Change[]): void {
		for (const c of list) {
			if (c.kind === 'deleted') {
				const under = `${c.path}/`;
				for (const p of [...this.#watched.keys()]) if (p === c.path || p.startsWith(under)) this.#watched.delete(p);
			} else {
				this.#watched.set(c.path, c.type === 'dir' ? 'dir' : 'file');
			}
		}
		this.#data();
	}

	/** A baseline (or a restart) is what this reader already took in `baseline`. */
	resync(): void {}

	// --- rows ---------------------------------------------------------------------------

	#data(): void {
		this.#kids = null;
		this.#changedDirs = null;
		this.#rows = null;
		this.#notify();
	}

	#notify(): void {
		for (const l of [...this.#listeners]) l();
	}

	#build(): TreeRow[] {
		if (this.#kids === null) {
			// The watched paths and the status paths together; a folder a path needs is a folder.
			const kids = new Map<string, Map<string, Kind>>();
			const put = (path: string, kind: Kind) => {
				const parent = parentOf(path);
				let dir = kids.get(parent);
				if (dir === undefined) kids.set(parent, (dir = new Map()));
				if (kind === 'file' && dir.get(path) === 'dir') return;
				dir.set(path, kind);
			};
			// A gitlink entry is the submodule's folder, never a file row.
			const all: [string, Kind][] = [...this.#watched, ...[...this.#status].map(([p, e]): [string, Kind] => [p, e.submodule !== undefined ? 'dir' : 'file'])];
			for (const [path, kind] of all) {
				put(path, kind);
				for (const dir of ancestors(path)) put(dir, 'dir');
			}
			this.#kids = kids;
		}
		if (this.#changedDirs === null) {
			this.#changedDirs = new Set();
			for (const [path, e] of this.#status) {
				if (e.submodule !== undefined) this.#changedDirs.add(path);
				for (const dir of ancestors(path)) this.#changedDirs.add(dir);
			}
		}
		const kids = this.#kids;
		const changedDirs = this.#changedDirs;
		const out: TreeRow[] = [];
		const visit = (dir: string, depth: number) => {
			const children = kids.get(dir);
			if (children === undefined) return;
			const sorted = [...children].sort(([pa, ka], [pb, kb]) => (ka === kb ? 0 : ka === 'dir' ? -1 : 1) || order(nameOf(pa), nameOf(pb)) || order(pa, pb));
			for (const [path, kind] of sorted) {
				if (kind === 'dir') {
					const expanded = this.#expanded.has(path);
					out.push({ kind, key: `d:${path}`, path, label: nameOf(path), depth, expanded, changed: changedDirs.has(path), deleted: false });
					if (expanded) visit(path, depth + 1);
				} else {
					const e = this.#status.get(path);
					out.push({
						kind,
						key: `f:${path}`,
						path,
						label: nameOf(path),
						depth,
						expanded: false,
						changed: e !== undefined,
						deleted: e?.mark === 'deleted',
						...(e !== undefined ? { mark: e.mark, letter: LETTERS[e.mark] } : {}),
						...(e?.origPath !== undefined ? { origPath: e.origPath } : {}),
					});
				}
			}
		};
		visit('', 1);
		return out;
	}
}

// --- the remembered view and grouping ---------------------------------------------------

export type GitTreeView = 'tree' | 'changes';
export type GitGroup = 'grouped' | 'flat';
export interface GitTreePrefs {
	view: GitTreeView;
	group: GitGroup;
}

export const PREFS_PREFIX = 'droidyard.ui.gittree.';
export const DEFAULT_PREFS: Readonly<GitTreePrefs> = { view: 'changes', group: 'grouped' };

/** What was stored for this project; each field that is not one of its words is the default. */
export function loadPrefs(storage: KeyValueStorage | null, project: string): GitTreePrefs {
	if (storage === null) return { ...DEFAULT_PREFS };
	try {
		const raw = storage.getItem(PREFS_PREFIX + project);
		if (raw === null) return { ...DEFAULT_PREFS };
		const v: unknown = JSON.parse(raw);
		if (typeof v !== 'object' || v === null) return { ...DEFAULT_PREFS };
		const o = v as Record<string, unknown>;
		return {
			view: o.view === 'tree' || o.view === 'changes' ? o.view : DEFAULT_PREFS.view,
			group: o.group === 'grouped' || o.group === 'flat' ? o.group : DEFAULT_PREFS.group,
		};
	} catch {
		return { ...DEFAULT_PREFS };
	}
}

export function savePrefs(storage: KeyValueStorage | null, project: string, prefs: GitTreePrefs): void {
	if (storage === null) return;
	try {
		storage.setItem(PREFS_PREFIX + project, JSON.stringify({ view: prefs.view, group: prefs.group }));
	} catch {
		// Not remembered; what is in memory is still right.
	}
}
