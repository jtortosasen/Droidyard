// The file tree's model (plan m2d step 5; ADR-0049; D26-D28). It holds the project's
// directories as the root watch reports them (watch.ts is its sink) and turns the expanded
// part into the flat rows the component draws.
//
// Where the children of a folder come from:
//   - a complete baseline: from the baseline, kept current by the watch's events. Folders
//     the watch leaves out (build output, .git...) are not in it; those "hidden" entries
//     are whatever `file.list` returns that the baseline lacks (D27: no parsing of
//     `excluded`, no copy of the daemon's rule). They are listed only while the toggle is
//     on, on each expand, and are not live.
//   - an incomplete baseline (the walk hit its cap): from `file.list`, when a folder is
//     expanded, and again for every expanded folder when the watch says its events were
//     cut. Events are applied only to the folders listed.
// A folder is marked as a module when it holds build.gradle(.kts) (D28), which needs no
// trust. Rows: folders first, then names in numeric order ("file2" before "file10");
// a chain of folders with one folder each is one row ("src/main/java").
import type { Params, Result } from '../api/capabilities.gen';
import type { CallOptions } from '../api/client';
import { failure, failureText } from '../build/failure';
import { Selection } from './selection';
import type { Baseline, Change, WatchSink, WatchState } from './watch';

/** The most `file.list` returns (the capability's largest limit). */
const LIST_LIMIT = 2000;
/** Type-ahead forgets what was typed after this long. */
const TYPE_AHEAD_MS = 700;

export type EntryType = 'file' | 'dir' | 'symlink' | 'other';

interface Entry {
	name: string;
	path: string;
	type: EntryType;
}

export type IconKind = 'folder' | 'folder-open' | 'module' | 'code' | 'json' | 'text' | 'image' | 'file';

export interface Row {
	/** The row's folder (the end of a compacted chain) or file, relative to the root. */
	path: string;
	/** The first folder of a compacted chain; equal to `path` otherwise. */
	head: string;
	label: string;
	/** 1 for the root's children. */
	depth: number;
	dir: boolean;
	expanded: boolean;
	module: boolean;
	icon: IconKind;
}

export interface TreeView {
	rows: readonly Row[];
	selected: string | null;
	/** Every selected path (D51), the focused row among them; operations act on `operands()`. */
	selection: ReadonlySet<string>;
	filter: string;
	showHidden: boolean;
	/** A folder or the watch could not be read; text, never markup. */
	error: string | null;
	/** Some folder has more entries than `file.list` returns. */
	more: boolean;
	watch: { state: WatchState; detail: string };
	/** Counts up each time the component should scroll the selected row into view. */
	revealSeq: number;
}

export interface TreeClient {
	call(name: 'file.list', params: Params<'file.list'>, opts?: CallOptions): Promise<Result<'file.list'>>;
}

export interface TreeWorkspace {
	readonly id: string | null;
	readonly root: string;
}

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });

function compare(a: Entry, b: Entry): number {
	const da = a.type === 'dir';
	const db = b.type === 'dir';
	if (da !== db) return da ? -1 : 1;
	return collator.compare(a.name, b.name) || (a.name < b.name ? -1 : a.name > b.name ? 1 : 0);
}

function parentOf(path: string): string {
	const i = path.lastIndexOf('/');
	return i < 0 ? '' : path.slice(0, i);
}

function nameOf(path: string): string {
	return path.slice(path.lastIndexOf('/') + 1);
}

function join(dir: string, name: string): string {
	return dir === '' ? name : `${dir}/${name}`;
}

const CODE = new Set(['kt', 'java', 'kts', 'gradle', 'groovy', 'js', 'ts', 'py', 'sh', 'c', 'cpp', 'h', 'go', 'rs', 'swift']);
const IMAGES = new Set(['png', 'jpg', 'jpeg', 'webp', 'gif', 'svg', 'bmp', 'ico']);

/** The icon of a file by its name and place (a drawable resource is an image). */
export function fileIcon(path: string): IconKind {
	const name = nameOf(path);
	const dot = name.lastIndexOf('.');
	const ext = dot > 0 ? name.slice(dot + 1).toLowerCase() : '';
	if (CODE.has(ext)) return 'code';
	if (ext === 'json') return 'json';
	if (IMAGES.has(ext)) return 'image';
	if (ext === 'xml' && /(^|\/)res\/(drawable|mipmap)[^/]*\//.test(path)) return 'image';
	if (ext === 'md' || ext === 'txt' || ext === 'properties' || ext === 'xml' || ext === 'toml' || ext === 'yml' || ext === 'yaml') return 'text';
	return 'file';
}

export class FileTreeModel {
	#client: TreeClient;
	#ws: TreeWorkspace;
	#listeners = new Set<() => void>();
	#view: TreeView | null = null;
	#rows: Row[] | null = null;

	/** Directory path ('' is the root) to its entries by name. A directory absent here is not known. */
	#kids = new Map<string, Map<string, Entry>>();
	/** Entries `file.list` returned that the baseline lacks, per directory (D27). */
	#hidden = new Map<string, Map<string, Entry>>();
	#sorted = new Map<string, Entry[]>();
	/** Incomplete baseline: the directories listed so far. */
	#listed = new Set<string>();
	#complete = true;
	#expanded = new Set<string>();
	#selected: string | null = null;
	/** The selected set beside the focused row (D51). */
	readonly #multi = new Selection();
	#filter = '';
	#showHidden = false;
	#error: string | null = null;
	#more = new Set<string>();
	#watch: { state: WatchState; detail: string } = { state: 'idle', detail: '' };
	#revealSeq = 0;
	#generation = 0;
	#typed = '';
	#typedAt = 0;
	#following: { path: string; expand: boolean } | null = null;

	constructor(client: TreeClient, ws: TreeWorkspace) {
		this.#client = client;
		this.#ws = ws;
	}

	/** What the root watch feeds. */
	readonly sink: WatchSink = {
		reset: () => this.#reset(),
		baseline: (entries, complete) => this.#baseline(entries, complete),
		changes: (list) => this.applyChanges(list),
		cut: () => this.#relistExpanded(),
		status: (state, detail) => {
			this.#watch = { state, detail };
			this.#changed(false);
		},
	};

	get view(): TreeView {
		this.#view ??= {
			rows: this.rows,
			selected: this.#selected === null ? null : (this.rows.find((r) => this.#covers(r, this.#selected!))?.path ?? this.#selected),
			selection: new Set(this.#multi.paths),
			filter: this.#filter,
			showHidden: this.#showHidden,
			error: this.#error,
			more: this.#more.size > 0,
			watch: this.#watch,
			revealSeq: this.#revealSeq,
		};
		return this.#view;
	}

	get rows(): readonly Row[] {
		this.#rows ??= this.#build();
		return this.#rows;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	dispose(): void {
		this.#generation++;
		this.#listeners.clear();
	}

	/** The path as the file system sees it: the project root and the relative path. */
	absolutePath(path: string): string {
		const root = this.#ws.root.trim().replace(/\/+$/, '');
		return path === '' ? root : `${root}/${path}`;
	}

	relativePath(path: string): string {
		return path === '' ? '.' : path;
	}

	// --- the sink ---------------------------------------------------------------------

	#reset(): void {
		this.#generation++;
		this.#kids.clear();
		this.#hidden.clear();
		this.#sorted.clear();
		this.#listed.clear();
		this.#more.clear();
		this.#expanded.clear();
		this.#selected = null;
		this.#multi.click(null);
		this.#error = null;
		this.#complete = true;
		this.#changed();
	}

	#baseline(entries: Baseline, complete: boolean): void {
		this.#generation++;
		this.#kids.clear();
		this.#sorted.clear();
		this.#listed.clear();
		this.#more.clear();
		this.#complete = complete;
		this.#error = null;
		if (complete) {
			this.#kids.set('', new Map());
			for (const e of entries) this.#add({ name: nameOf(e.path), path: e.path, type: e.type });
		}
		this.#hidden.clear();
		this.#changed();
		// The root, and what was open, are read again: from the listing when the baseline
		// is partial, and for the hidden entries when they are shown.
		if (!complete || this.#showHidden) this.#relistExpanded();
	}

	/** The watch's differences, applied together (one notification). */
	applyChanges(list: readonly Change[]): void {
		const before = this.rows;
		const at = this.#selected === null ? -1 : before.findIndex((r) => this.#covers(r, this.#selected!));
		let arrived: { path: string; expand: boolean } | null = null;
		for (const c of list) {
			const path = c.path;
			if (c.kind === 'deleted') {
				this.#remove(path);
			} else if (c.kind === 'created') {
				// (#add skips a folder a partial baseline never listed.)
				this.#add({ name: nameOf(path), path, type: c.type ?? 'file' });
				if (this.#following?.path === path) arrived = this.#following;
			}
		}
		this.#changed();
		if (arrived !== null) {
			this.#following = null;
			void this.#show(arrived.path, arrived.expand);
		}
		if (this.#selected !== null && at >= 0 && !this.rows.some((r) => this.#covers(r, this.#selected!))) {
			// The selected row went: its neighbour in the old order that is still there.
			const now = new Set(this.rows.map((r) => r.path));
			const pick = (r: Row | undefined) => (r !== undefined && now.has(r.path) ? r.path : null);
			let next: string | null = null;
			for (let i = at + 1; i < before.length && next === null; i++) next = pick(before[i]);
			for (let i = at - 1; i >= 0 && next === null; i--) next = pick(before[i]);
			// Others still selected keep the selection; the focus moves to one of them.
			if (this.#multi.paths.size > 0) this.#selected = [...this.#multi.paths].at(-1)!;
			else {
				this.#selected = next;
				this.#multi.click(next);
			}
			this.#revealSeq++;
			this.#changed();
		}
	}

	#add(e: Entry): void {
		const parent = parentOf(e.path);
		let dir = this.#kids.get(parent);
		if (dir === undefined) {
			if (!this.#complete && !this.#listed.has(parent)) return;
			dir = new Map();
			this.#kids.set(parent, dir);
		}
		dir.set(e.name, e);
		this.#sorted.delete(parent);
		this.#hidden.get(parent)?.delete(e.name);
		if (e.type === 'dir' && this.#complete && !this.#kids.has(e.path)) this.#kids.set(e.path, new Map());
	}

	#remove(path: string): void {
		const parent = parentOf(path);
		const dir = this.#kids.get(parent);
		if (dir?.delete(nameOf(path))) this.#sorted.delete(parent);
		this.#hidden.get(parent)?.delete(nameOf(path));
		const under = `${path}/`;
		for (const map of [this.#kids, this.#hidden, this.#sorted]) {
			for (const k of [...map.keys()]) if (k === path || k.startsWith(under)) map.delete(k);
		}
		for (const k of [...this.#listed]) if (k === path || k.startsWith(under)) this.#listed.delete(k);
		for (const k of [...this.#expanded]) if (k === path || k.startsWith(under)) this.#expanded.delete(k);
	}

	// --- listing ----------------------------------------------------------------------

	/** The directories `file.list` is read for: all that are open, and the root. */
	#relistExpanded(): void {
		const gen = this.#generation;
		const dirs = ['', ...[...this.#expanded].filter((d) => this.#known(d) || !this.#complete)];
		void Promise.all(dirs.map((d) => this.#list(d, gen))).then(() => this.#changed());
	}

	/** Reads one directory; a stale answer (the baseline was replaced meanwhile) is dropped. */
	async #list(dir: string, gen: number): Promise<void> {
		let r: Result<'file.list'>;
		try {
			r = await this.#client.call('file.list', dir === '' ? { limit: LIST_LIMIT } : { path: dir, limit: LIST_LIMIT }, {
				workspace: this.#ws.root.trim(),
			});
		} catch (e) {
			if (gen === this.#generation) {
				this.#error = failureText(failure(e));
				this.#changed();
			}
			return;
		}
		if (gen !== this.#generation) return;
		this.#error = null;
		if (r.truncated) this.#more.add(dir);
		else this.#more.delete(dir);
		const entries = r.entries.map((e): Entry => ({ name: e.name, path: join(dir, e.name), type: e.type }));
		if (this.#complete) {
			const known = this.#kids.get(dir);
			const extra = new Map<string, Entry>();
			for (const e of entries) if (!known?.has(e.name)) extra.set(e.name, e);
			this.#hidden.set(dir, extra);
			this.#sorted.delete(dir);
		} else {
			this.#kids.set(dir, new Map(entries.map((e) => [e.name, e])));
			this.#listed.add(dir);
			this.#sorted.delete(dir);
			// A folder with a single folder is one row, and expanding the row opens the chain.
			const only = this.#visible(dir);
			if (this.#expanded.has(dir) && only.length === 1 && only[0]!.type === 'dir' && !this.#expanded.has(only[0]!.path)) {
				this.#expanded.add(only[0]!.path);
				await this.#list(only[0]!.path, gen);
			}
		}
	}

	#exists(path: string): boolean {
		const dir = parentOf(path);
		const name = nameOf(path);
		if (!this.#known(dir)) return true; // not read yet: cannot tell it went
		return this.#kids.get(dir)?.has(name) === true || (this.#showHidden && this.#hidden.get(dir)?.has(name) === true);
	}

	#known(dir: string): boolean {
		return this.#complete ? this.#kids.has(dir) : this.#listed.has(dir);
	}

	// --- the user ---------------------------------------------------------------------

	async expand(path: string): Promise<void> {
		if (this.#expanded.has(path) && this.#known(path)) return;
		this.#expanded.add(path);
		this.#changed();
		// A partial baseline reads the folder; a complete one only for hidden entries.
		if (!this.#complete ? !this.#listed.has(path) : this.#showHidden) {
			await this.#list(path, this.#generation);
			this.#changed();
		}
	}

	collapse(path: string): void {
		if (!this.#expanded.delete(path)) return;
		const sel = this.#selected;
		if (sel !== null && sel.startsWith(`${path}/`)) this.#selected = path;
		this.#changed();
	}

	toggle(path: string): Promise<void> {
		if (this.#expanded.has(path)) {
			this.collapse(path);
			return Promise.resolve();
		}
		return this.expand(path);
	}

	select(path: string | null): void {
		if (this.#selected === path && this.#multi.paths.size === (path === null ? 0 : 1)) return;
		this.#selected = path;
		this.#multi.click(path);
		this.#changed();
	}

	/** ⌘-click (D51): the row joins or leaves the selection and takes the focus. */
	toggleSelect(path: string): void {
		this.#multi.toggle(path);
		this.#selected = path;
		this.#changed();
	}

	/** ⇧-click (D51): the rows from the anchor to `path`, in row order. */
	selectRange(path: string): void {
		this.#multi.range(path, this.rows.map((r) => r.path));
		this.#selected = path;
		this.#changed();
	}

	/** ⇧↑ / ⇧↓ (D51): grows or shrinks the range by one row, and scrolls to it. */
	extendSelection(delta: -1 | 1): void {
		if (this.#multi.anchor === null && this.#selected !== null) this.#multi.click(this.#selected);
		const to = this.#multi.extend(delta, this.rows.map((r) => r.path));
		if (to === null) return;
		this.#selected = to;
		this.#revealSeq++;
		this.#changed();
	}

	/** What an operation acts on (D51): the top-most selected paths, in row order. */
	operands(): string[] {
		const top = new Set(this.#multi.topmost());
		const ordered = this.rows.map((r) => r.path).filter((p) => top.has(p));
		// A selected path no row shows (inside a collapsed folder) still counts.
		return [...ordered, ...[...top].filter((p) => !ordered.includes(p))];
	}

	setFilter(text: string): void {
		if (text === this.#filter) return;
		this.#filter = text;
		this.#changed();
	}

	setShowHidden(on: boolean): void {
		if (on === this.#showHidden) return;
		this.#showHidden = on;
		this.#sorted.clear();
		this.#changed();
		if (on && this.#complete) this.#relistExpanded();
	}

	/**
	 * Selects and reveals `path` (and opens it when `expand`) once the watch reports it (D58);
	 * at once when the tree already holds it. A path the watch never reports is not shown.
	 */
	follow(path: string, expand = false): void {
		if (this.#kids.get(parentOf(path))?.has(nameOf(path))) {
			this.#following = null;
			void this.#show(path, expand);
		} else {
			this.#following = { path, expand };
		}
	}

	async #show(path: string, expand: boolean): Promise<void> {
		await this.reveal(path);
		if (expand) await this.expand(path);
	}

	/** Opens the folders above `path` and selects it; the component scrolls to it. */
	async reveal(path: string): Promise<void> {
		const parts = path.split('/');
		let dir = '';
		for (let i = 0; i < parts.length - 1; i++) {
			dir = join(dir, parts[i]!);
			await this.expand(dir);
		}
		this.#filter = '';
		this.#selected = path;
		this.#multi.click(path);
		this.#revealSeq++;
		this.#changed();
	}

	/**
	 * A key on the tree. Returns whether it was handled and, for Enter on a file, the path to
	 * open. Printable keys are type-ahead: the typed text jumps to the next row starting with it.
	 */
	key(key: string, now: number = Date.now()): { handled: boolean; open?: string } {
		const rows = this.rows;
		if (rows.length === 0) return { handled: false };
		const i = this.#selected === null ? -1 : rows.findIndex((r) => this.#covers(r, this.#selected!));
		const cur = i >= 0 ? rows[i]! : null;
		const go = (j: number) => {
			this.#selected = rows[Math.min(rows.length - 1, Math.max(0, j))]!.path;
			this.#multi.click(this.#selected);
			this.#revealSeq++;
			this.#changed();
		};
		switch (key) {
			case 'ArrowDown':
				go(i + 1);
				return { handled: true };
			case 'ArrowUp':
				go(i < 0 ? 0 : i - 1);
				return { handled: true };
			case 'Home':
				go(0);
				return { handled: true };
			case 'End':
				go(rows.length - 1);
				return { handled: true };
			case 'ArrowRight':
				if (cur?.dir) {
					if (!cur.expanded) void this.expand(cur.path);
					else if (rows[i + 1] && rows[i + 1]!.depth > cur.depth) go(i + 1);
				}
				return { handled: true };
			case 'ArrowLeft':
				if (cur !== null) {
					if (cur.dir && cur.expanded) {
						this.collapse(cur.path);
					} else {
						for (let j = i - 1; j >= 0; j--) {
							if (rows[j]!.depth < cur.depth) {
								go(j);
								break;
							}
						}
					}
				}
				return { handled: true };
			case 'Enter':
			case ' ':
				if (cur === null) return { handled: true };
				if (cur.dir) void this.toggle(cur.path);
				else return { handled: true, open: cur.path };
				return { handled: true };
		}
		if (key.length === 1 && key !== ' ') {
			this.#typed = now - this.#typedAt > TYPE_AHEAD_MS ? key : this.#typed + key;
			this.#typedAt = now;
			const text = this.#typed.toLowerCase();
			// One letter typed again and again steps through the rows that start with it;
			// a longer text narrows from the current row.
			const repeated = text.length > 1 && [...text].every((c) => c === text[0]);
			const needle = repeated ? text[0]! : text;
			const from = text.length > 1 && !repeated ? Math.max(i, 0) : i + 1;
			for (let n = 0; n < rows.length; n++) {
				const j = (from + n) % rows.length;
				if (rows[j]!.label.toLowerCase().startsWith(needle)) {
					go(j);
					break;
				}
			}
			return { handled: true };
		}
		return { handled: false };
	}

	// --- the rows ---------------------------------------------------------------------

	/** Whether the row stands for `path`: the row itself, or a folder inside its compacted chain. */
	#covers(r: Row, path: string): boolean {
		if (r.path === path) return true;
		return (path === r.head || path.startsWith(`${r.head}/`)) && r.path.startsWith(`${path}/`);
	}

	/** A directory's entries in display order, hidden ones included while the toggle is on. */
	#children(dir: string): Entry[] {
		let list = this.#sorted.get(dir);
		if (list === undefined) {
			const all = new Map(this.#kids.get(dir));
			if (this.#showHidden) for (const [n, e] of this.#hidden.get(dir) ?? []) if (!all.has(n)) all.set(n, e);
			list = [...all.values()].sort(compare);
			this.#sorted.set(dir, list);
		}
		return list;
	}

	/** The children the filter lets through: matches and the folders above matches. */
	#visible(dir: string, memo?: Map<string, boolean>): Entry[] {
		const f = this.#filter.trim().toLowerCase();
		const all = this.#children(dir);
		if (f === '') return all;
		const m = memo ?? new Map<string, boolean>();
		const has = (d: string): boolean => {
			let v = m.get(d);
			if (v === undefined) {
				v = this.#children(d).some((e) => e.name.toLowerCase().includes(f) || (e.type === 'dir' && has(e.path)));
				m.set(d, v);
			}
			return v;
		};
		return all.filter((e) => e.name.toLowerCase().includes(f) || (e.type === 'dir' && has(e.path)));
	}

	#build(): Row[] {
		const out: Row[] = [];
		const filtering = this.#filter.trim() !== '';
		const memo = new Map<string, boolean>();
		const visit = (dir: string, depth: number) => {
			for (const e of this.#visible(dir, memo)) {
				if (e.type !== 'dir') {
					out.push({ path: e.path, head: e.path, label: e.name, depth, dir: false, expanded: false, module: false, icon: fileIcon(e.path) });
					continue;
				}
				let end = e;
				let label = e.name;
				for (;;) {
					const kids = this.#known(end.path) ? this.#visible(end.path, memo) : [];
					if (kids.length !== 1 || kids[0]!.type !== 'dir') break;
					end = kids[0]!;
					label += `/${end.name}`;
				}
				const expanded = this.#expanded.has(end.path) || (filtering && this.#visible(end.path, memo).length > 0);
				const module = this.#isModule(end.path);
				out.push({
					path: end.path,
					head: e.path,
					label,
					depth,
					dir: true,
					expanded,
					module,
					icon: module ? 'module' : expanded ? 'folder-open' : 'folder',
				});
				if (expanded) visit(end.path, depth + 1);
			}
		};
		visit('', 1);
		return out;
	}

	#isModule(dir: string): boolean {
		const kids = this.#kids.get(dir);
		return kids !== undefined && (kids.get('build.gradle')?.type === 'file' || kids.get('build.gradle.kts')?.type === 'file');
	}

	#changed(rows = true): void {
		if (rows) {
			this.#rows = null;
			// Rows that went take their selection with them (D51).
			this.#multi.prune((p) => this.#exists(p));
		}
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}
