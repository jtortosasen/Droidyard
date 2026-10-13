// The History tab's store (plan m2n step 4; ADR-0069 §1, §2, §6; ADR-0070 §1–§3): one per
// project tab. It reads `git.log` and keeps the rows the panel draws:
//   - the first page is called without `from`; its `tips` come back and every later page passes
//     them with `skip` = the commits received, so the pages stay one walk while refs move;
//   - `Lanes` is extended page by page (ADR-0070 §1);
//   - nothing is read before the Git watch has a status (the project is open and is a
//     repository); `retry()` reads again after a failure.
// When it reloads (ADR-0070 §3): the history starts again from its first page, the rows being
// replaced when the page arrives (what is shown stays until then). It does so when the watch's
// `head.oid` changes and when the refs it holds differ in their `tips` from those it started
// from. The selection stays when its key is among the new rows, else it clears; `restore`
// names the key the scroll returns to (the one at the top of the view when still loaded) or
// null for the top. A reload overtakes a page in flight; its answer is dropped.
// The refs (`git.refs`, step 6) are read with the first status, again when `head.oid` changes,
// when the tab is shown (`setVisible`) and when the window regains focus (`focused`). A refs
// answer whose tips differ from the history's own reloads the history, unless a read of the
// history is on its way (it asks git afresh).
// The Uncommitted Changes row is the watch's entries (none: no row).
//
// One log for every root (plan m2n P7; ADR-0071 §7): the roots are the workspace `.` and the
// initialised submodules the shell names (`setRoots`). Each root is its own stream: its tips, the
// commits received, pages with their own `from` and `skip`, and a `root` parameter for a
// submodule (the workspace's calls are the same as without submodules). `Merge` (mixed.ts) turns the
// streams into one list by commit time and lets a row out only when no root could still put an
// older page above it; a scroll to the end reads the next page of every root the merge waits for.
// Rows, selection, expansion, anchor and scroll targets are keyed by `rowKey(root, oid)`, which
// is the bare oid for the workspace. The reload rules of ADR-0070 §3 run per root: a root whose
// tips differ from the ones it started from reloads from its first page, and the merge is built
// again from the streams held, the rows shown being replaced when that page arrives. The root
// filter keeps the rows of the roots chosen and lays the lanes out again from them.
import type { Params, Result } from '../api/capabilities.gen';
import type { CallOptions } from '../api/client';
import { failure, failureText } from '../build/failure';
import { Lanes, type LaneRow } from './lanes';
import { Merge, WORKSPACE_ROOT, filterRows, laneInput, rowKey, toggleRoot, type MixedRow } from './mixed';
import { localRef, refNames } from './refs';
import type { Show } from './files';
import type { GitView } from './watch';

export type Commit = Result<'git.log'>['commits'][number];

export interface HistoryClient {
	call(name: 'git.log', params: Params<'git.log'>, opts?: CallOptions): Promise<Result<'git.log'>>;
	call(name: 'git.refs', params: Params<'git.refs'>, opts?: CallOptions): Promise<Result<'git.refs'>>;
	call(name: 'git.show', params: Params<'git.show'>, opts?: CallOptions): Promise<Result<'git.show'>>;
}

/** The commit opened under its row (plan m2n step 8). */
export interface ExpandedCommit {
	/** `rowKey(root, oid)`. */
	key: string;
	/** The commit's own object id. */
	oid: string;
	/** The root it belongs to: `.` or a submodule path. */
	root: string;
	state: 'loading' | 'ready' | 'error';
	show: Show | null;
	problem: string | null;
}

/** How many more pages a click on a ref may load to find its commit. */
export const REVEAL_PAGES = 5;
/** What is said when the commit is not among the loaded ones. */
export const NOT_LOADED = 'not in the loaded history; scroll to load more';

export interface HistoryWorkspace {
	readonly root: string;
}

/** What the store reads of the Git watch. */
export interface HistoryWatch {
	readonly view: GitView;
	subscribe(listener: () => void): () => void;
}

export interface HistoryRow {
	/** `rowKey(root, commit.oid)`: the row's identity. */
	key: string;
	/** The root the commit belongs to: `.` or a submodule path. */
	root: string;
	commit: Commit;
	lane: LaneRow;
}

export interface HistoryView {
	/** `idle`: the watch has no status yet; `loading`: the first page is on its way and nothing is shown. */
	state: 'idle' | 'loading' | 'ready' | 'error';
	/** The reason of `error`, as text. */
	problem: string | null;
	rows: readonly HistoryRow[];
	/** The walk goes on past the rows held. */
	more: boolean;
	loadingMore: boolean;
	/** A page after the first failed (the rows held stay); the next scroll to the end tries again. */
	moreProblem: string | null;
	/** The selected commit's key, when it is among the rows. */
	selected: string | null;
	/** Paths that differ from HEAD; 0 hides the Uncommitted Changes row. */
	uncommitted: number;
	/** The status holds more paths than the watch lists. */
	uncommittedTruncated: boolean;
	/** HEAD's oid, empty without a commit. */
	headOid: string;
	/** The widest row so far, in lane columns. */
	width: number;
	/** The refs the side panel and the badges show; null until the first read arrives. */
	refs: Result<'git.refs'> | null;
	/** `idle` until the first read; `loading` while the first is on its way; `error`: the first read failed (a later failure keeps `refs`). */
	refsState: 'idle' | 'loading' | 'ready' | 'error';
	/** The reason of a failed refs read, as text. */
	refsProblem: string | null;
	/** The ref picked in the side panel (its full name, refs/heads/x), or null. */
	selectedRef: string | null;
	/** A request to bring a row into view; `seq` makes each one new. */
	scrollTo: { seq: number; oid: string } | null;
	/** Why a click on a ref did not select a commit, as text. */
	notice: string | null;
	/** Set by each reload that finished: where the scroll goes (a key, or null for the top). `seq` makes each one a new request. */
	restore: { seq: number; oid: string | null } | null;
	/** The commit opened under its row, or null. At most one. */
	expanded: ExpandedCommit | null;
	/** The roots merged into the log, workspace first; a root's position is its stripe's colour. */
	roots: readonly string[];
	/** The roots the log is filtered to, or null for every root. */
	filter: readonly string[] | null;
	/** The refs of each submodule root (the workspace's are `refs`), for the badges of its own rows. */
	rootRefs: ReadonlyMap<string, Result<'git.refs'>>;
}

const EMPTY: HistoryView = {
	state: 'idle',
	problem: null,
	rows: [],
	more: false,
	loadingMore: false,
	moreProblem: null,
	selected: null,
	uncommitted: 0,
	uncommittedTruncated: false,
	headOid: '',
	width: 0,
	refs: null,
	refsState: 'idle',
	refsProblem: null,
	selectedRef: null,
	scrollTo: null,
	notice: null,
	restore: null,
	expanded: null,
	roots: [WORKSPACE_ROOT],
	filter: null,
	rootRefs: new Map(),
};

/** One root's pages: what was received and where its walk goes on. */
interface RootStream {
	commits: Commit[];
	tips: string[];
	more: boolean;
	loading: boolean;
}

/** The `root` parameter of a call: absent for the workspace, so its calls are the same as without submodules. */
const rootArg = (root: string): { root?: string } => (root === WORKSPACE_ROOT ? {} : { root });

type First = { ok: true; r: Result<'git.log'> } | { ok: false; e: unknown };

export class HistoryStore {
	#client: HistoryClient;
	#ws: HistoryWorkspace;
	#watch: HistoryWatch;
	#listeners = new Set<() => void>();
	#off: () => void;
	#view: HistoryView = EMPTY;
	#lanes = new Lanes();
	/** Key of each row shown to its index. */
	#index = new Map<string, number>();
	/** The roots wanted: the workspace, then the submodules the shell named. */
	#wanted: string[] = [WORKSPACE_ROOT];
	/** The roots whose first page is merged in, in `#wanted` order. */
	#streams = new Map<string, RootStream>();
	#merge = new Merge<Commit>([WORKSPACE_ROOT]);
	/** Every row the merge let out, in order, before the filter. */
	#emitted: MixedRow<Commit>[] = [];
	#filter: ReadonlySet<string> | null = null;
	/** Bumped by every full reload: an answer of an older generation is dropped. */
	#gen = 0;
	/** Per root, bumped by every read of its first page: an older answer is dropped. */
	#rgen = new Map<string, number>();
	/** Per root, bumped by every refs read of it. */
	#rrefsGen = new Map<string, number>();
	#started = false;
	#anchor: string | null = null;
	#seenOid: string | undefined;
	#restoreSeq = 0;
	#scrollSeq = 0;
	#disposed = false;
	/** Bumped by every refs read: an answer of an older read is dropped. */
	#refsGen = 0;
	/** First pages in flight. */
	#reading = 0;
	#visible = false;
	#pageP: { gen: number; done: Promise<void> } | null = null;
	/** `git.show` answers kept while their row stays loaded: one call per commit opened. */
	#shows = new Map<string, Show>();
	/** Bumped by every expansion: an answer of an older one is dropped. */
	#showGen = 0;

	constructor(client: HistoryClient, ws: HistoryWorkspace, watch: HistoryWatch) {
		this.#client = client;
		this.#ws = ws;
		this.#watch = watch;
		this.#seenOid = watch.view.head === null ? undefined : (watch.view.head.oid ?? '');
		this.#off = watch.subscribe(() => this.#watched());
		this.#watched();
	}

	get view(): HistoryView {
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** The row index of a key among the rows shown, or -1. */
	indexOf(key: string): number {
		return this.#index.get(key) ?? -1;
	}

	/** The row at the top of the view (its key), which a reload scrolls back to. Not reactive. */
	setAnchor(key: string | null): void {
		this.#anchor = key;
	}

	get anchor(): string | null {
		return this.#anchor;
	}

	select(key: string | null): void {
		if (key !== null && !this.#index.has(key)) return;
		this.#set({ selected: key, notice: null });
	}

	/** Opens the commit under its row (and selects it): one `git.show`, kept while the row stays loaded. */
	expand(key: string): void {
		const at = this.#index.get(key);
		if (this.#disposed || at === undefined) return;
		const row = this.#view.rows[at];
		const oid = row.commit.oid;
		const root = row.root;
		const gen = ++this.#showGen;
		const cached = this.#shows.get(key);
		if (cached) {
			this.#set({ selected: key, notice: null, expanded: { key, oid, root, state: 'ready', show: cached, problem: null } });
			return;
		}
		this.#set({ selected: key, notice: null, expanded: { key, oid, root, state: 'loading', show: null, problem: null } });
		this.#client.call('git.show', { oid, ...rootArg(root) }, { workspace: this.#ws.root.trim() }).then(
			(r) => {
				if (gen !== this.#showGen || this.#disposed) return;
				if (this.#index.has(key)) this.#shows.set(key, r);
				this.#set({ expanded: { key, oid, root, state: 'ready', show: r, problem: null } });
			},
			(e: unknown) => {
				if (gen !== this.#showGen || this.#disposed) return;
				this.#set({ expanded: { key, oid, root, state: 'error', show: null, problem: failureText(failure(e)) } });
			},
		);
	}

	/** Closes the opened commit. */
	collapse(): void {
		this.#showGen++;
		this.#set({ expanded: null });
	}

	/** A click on a row: opens the commit, or closes it when it is the one open. */
	toggleExpand(key: string): void {
		if (this.#view.expanded?.key === key) {
			this.collapse();
			this.select(key);
		} else this.expand(key);
	}

	/** A parent clicked in the opened commit (an oid of its root): revealed (more pages loaded to find it) and opened. */
	async openParent(oid: string): Promise<void> {
		const key = rowKey(this.#view.expanded?.root ?? WORKSPACE_ROOT, oid);
		if (await this.reveal(key)) this.expand(key);
	}

	/** Selects a loaded commit and asks the table to bring its row into view. */
	show(key: string): boolean {
		if (!this.#index.has(key)) return false;
		this.#set({ selected: key, notice: null, scrollTo: { seq: ++this.#scrollSeq, oid: key } });
		return true;
	}

	/**
	 * Selects the commit `key` and shows it, loading at most REVEAL_PAGES more pages to find it;
	 * else says NOT_LOADED. Resolves to whether the commit was selected.
	 */
	async reveal(key: string): Promise<boolean> {
		this.#set({ notice: null });
		const gen = this.#gen;
		for (let pages = 0; ; pages++) {
			if (this.#disposed || gen !== this.#gen) return false;
			if (this.show(key)) return true;
			const v = this.#view;
			if (pages >= REVEAL_PAGES || v.state !== 'ready' || !(v.more || v.loadingMore)) break;
			await this.#nextPage();
			if (this.#view.moreProblem) break;
		}
		this.#set({ notice: NOT_LOADED });
		return false;
	}

	/** A ref picked in the side panel (the workspace's): it is the selected one, and its commit is revealed (a tag on a tree has none). */
	async selectRef(ref: string, oid: string | undefined): Promise<boolean> {
		this.#set({ selectedRef: ref, notice: null });
		if (oid === undefined) return false;
		return this.reveal(rowKey(WORKSPACE_ROOT, oid));
	}

	/** Navigate to HEAD: the current branch is the selected ref and HEAD's commit is revealed. */
	async navigateToHead(): Promise<boolean> {
		const head = this.#view.refs?.head;
		const oid = head?.oid ?? (this.#view.headOid || undefined);
		if (oid === undefined) return false;
		this.#set({ selectedRef: head && !head.detached && head.branch !== undefined ? localRef(head.branch) : null });
		return this.reveal(rowKey(WORKSPACE_ROOT, oid));
	}

	/** The History tab is shown (true) or hidden: showing it reads the refs again (ADR-0070 §3). */
	setVisible(visible: boolean): void {
		if (visible === this.#visible) return;
		this.#visible = visible;
		if (visible) this.refreshRefs();
	}

	/** The window regained focus: the refs are read again (ADR-0070 §3). */
	focused(): void {
		this.refreshRefs();
	}

	/**
	 * The roots of the log: the workspace and these submodule paths (the initialised ones, which the shell reads
	 * from `git.submodules`). A root added is read from its first page and merged in when that page arrives; one
	 * removed leaves the log at once.
	 */
	setRoots(paths: readonly string[]): void {
		const next = [WORKSPACE_ROOT, ...paths.filter((p, i) => p !== WORKSPACE_ROOT && paths.indexOf(p) === i)];
		if (sameList(next, this.#wanted)) return;
		this.#wanted = next;
		this.#sync();
	}

	/** Chooses the roots the log shows (null or all of them: every root); the lanes are laid out again from those rows. */
	setFilter(roots: readonly string[] | null): void {
		const loaded = this.#view.roots;
		const chosen = roots === null ? [] : loaded.filter((r) => roots.includes(r));
		const next = chosen.length === 0 || chosen.length >= loaded.length ? null : chosen;
		const cur = this.#view.filter;
		if (next === null ? cur === null : cur !== null && sameList(next, cur)) return;
		this.#filter = next === null ? null : new Set(next);
		const keepScroll = this.#anchor;
		const rows = this.#relay();
		const selected = this.#view.selected !== null && this.#index.has(this.#view.selected) ? this.#view.selected : null;
		const open = this.#view.expanded;
		const expanded = open !== null && this.#index.has(open.key) ? open : null;
		if (expanded === null && open !== null) this.#showGen++;
		this.#set({
			filter: next,
			rows,
			selected,
			expanded,
			width: this.#lanes.width,
			restore: { seq: ++this.#restoreSeq, oid: keepScroll !== null && this.#index.has(keepScroll) ? keepScroll : null },
		});
	}

	/** A click on a root of the filter. */
	toggleRoot(root: string): void {
		this.setFilter(toggleRoot(this.#view.filter, root, this.#view.roots));
	}

	/** Reads the refs, unless the watch has no status yet (not a repository, or not read). */
	refreshRefs(): void {
		if (this.#disposed || this.#watch.view.head === null) return;
		const gen = ++this.#refsGen;
		if (this.#view.refs === null) this.#set({ refsState: 'loading', refsProblem: null });
		this.#client.call('git.refs', {}, { workspace: this.#ws.root.trim() }).then(
			(r) => {
				if (gen !== this.#refsGen || this.#disposed) return;
				const names = refNames(r);
				const sel = this.#view.selectedRef;
				this.#set({ refs: r, refsState: 'ready', refsProblem: null, selectedRef: sel !== null && names.has(sel) ? sel : null });
				// The tips the history started from are no longer the refs': it starts again (ADR-0070 §3).
				if (this.#view.state === 'ready' && this.#reading === 0 && !sameList(r.tips, this.#streams.get(WORKSPACE_ROOT)?.tips ?? [])) void this.#reloadRoot(WORKSPACE_ROOT);
			},
			(e: unknown) => {
				if (gen !== this.#refsGen || this.#disposed) return;
				// What was shown stays when a later read fails.
				if (this.#view.refs === null) this.#set({ refsState: 'error', refsProblem: failureText(failure(e)) });
			},
		);
		for (const root of this.#streams.keys()) if (root !== WORKSPACE_ROOT) this.#readRootRefs(root);
	}

	/** A submodule root's refs: for the badges of its rows, and the same reload rule on its own tips. A failure keeps what was held. */
	#readRootRefs(root: string): void {
		const gen = (this.#rrefsGen.get(root) ?? 0) + 1;
		this.#rrefsGen.set(root, gen);
		this.#client.call('git.refs', { root }, { workspace: this.#ws.root.trim() }).then(
			(r) => {
				if (gen !== this.#rrefsGen.get(root) || this.#disposed || !this.#streams.has(root)) return;
				const held = new Map(this.#view.rootRefs);
				held.set(root, r);
				this.#set({ rootRefs: held });
				if (this.#view.state === 'ready' && this.#reading === 0 && !sameList(r.tips, this.#streams.get(root)?.tips ?? [])) void this.#reloadRoot(root);
			},
			() => {},
		);
	}

	/** The next page, when there is one and none is on its way. */
	loadMore(): void {
		void this.#nextPage();
	}

	/** The roots the log waits for: the merge's blocked ones, and wanted roots whose first read failed. */
	#waiting(): { pages: string[]; missing: string[] } {
		return { pages: this.#merge.blocked().filter((r) => this.#streams.get(r)?.more === true), missing: this.#wanted.filter((r) => !this.#streams.has(r)) };
	}

	#more(): boolean {
		const w = this.#waiting();
		return w.pages.length > 0 || w.missing.length > 0;
	}

	/** The page in flight, else a new one for every root the merge waits for; resolves when read or failed. */
	#nextPage(): Promise<void> {
		if (this.#pageP && this.#pageP.gen === this.#gen) return this.#pageP.done;
		const v = this.#view;
		if (this.#disposed || v.state !== 'ready' || v.loadingMore) return Promise.resolve();
		const { pages, missing } = this.#waiting();
		if (pages.length + missing.length === 0) return Promise.resolve();
		const gen = this.#gen;
		this.#set({ loadingMore: true, moreProblem: null });
		const done: Promise<void> = Promise.all([...pages.map((r) => this.#pageOf(r)), ...missing.map((r) => this.#reloadRoot(r))])
			.then((problems) => {
				if (gen !== this.#gen) return;
				this.#set({ loadingMore: false, moreProblem: problems.find((p) => p !== null) ?? null, more: this.#more() });
			})
			.finally(() => {
				if (this.#pageP?.done === done) this.#pageP = null;
			});
		this.#pageP = { gen, done };
		return done;
	}

	/** The next page of one root, merged in when it arrives; resolves to the reason when it failed. */
	#pageOf(root: string): Promise<string | null> {
		const s = this.#streams.get(root);
		if (!s || s.loading || !s.more || s.tips.length === 0) return Promise.resolve(null);
		const gen = this.#gen;
		const token = this.#rgen.get(root);
		s.loading = true;
		return this.#client.call('git.log', { from: s.tips, skip: s.commits.length, ...rootArg(root) }, { workspace: this.#ws.root.trim() }).then(
			(r) => {
				if (gen !== this.#gen || token !== this.#rgen.get(root) || this.#streams.get(root) !== s) return null;
				s.loading = false;
				for (const c of r.commits) s.commits.push(c);
				s.more = r.more;
				this.#extend(root, r.commits, r.more);
				return null;
			},
			(e: unknown) => {
				if (gen !== this.#gen || token !== this.#rgen.get(root) || this.#streams.get(root) !== s) return null;
				s.loading = false;
				return failureText(failure(e));
			},
		);
	}

	/** Reads the history again from its first page, every root (the Retry button, the first read, a root list that changed meanwhile). */
	reload(): void {
		if (this.#disposed || this.#watch.view.head === null) return;
		this.#started = true;
		const gen = ++this.#gen;
		const wanted = [...this.#wanted];
		for (const r of wanted) this.#bump(r);
		if (this.#view.state !== 'ready') this.#set({ state: 'loading', problem: null });
		const tokens = wanted.map((r) => this.#rgen.get(r));
		Promise.all(wanted.map((r) => this.#readFirst(r))).then((answers) => {
			if (gen !== this.#gen || this.#disposed) return;
			if (wanted.some((r, i) => tokens[i] !== this.#rgen.get(r))) return;
			const first = answers[0];
			if (!first.ok) {
				// What was shown stays when a reload fails; a first read shows the reason.
				const text = failureText(failure(first.e));
				if (this.#view.state === 'ready') this.#set({ moreProblem: text, loadingMore: false });
				else this.#set({ state: 'error', problem: text });
				return;
			}
			const streams = new Map<string, RootStream>();
			let problem: string | null = null;
			wanted.forEach((r, i) => {
				const a = answers[i];
				if (a.ok) streams.set(r, { commits: [...a.r.commits], tips: a.r.tips, more: a.r.more, loading: false });
				else problem ??= `${r}: ${failureText(failure(a.e))}`;
			});
			this.#install(streams, problem);
			if (!sameList(wanted, this.#wanted)) this.#sync();
			for (const r of streams.keys()) if (r !== WORKSPACE_ROOT && !this.#view.rootRefs.has(r)) this.#readRootRefs(r);
		});
	}

	/**
	 * One root read again from its first page (ADR-0070 §3, per root): the merge is built again from the streams held
	 * when the page arrives. A log not shown yet starts afresh instead. Resolves to the reason when the read failed.
	 */
	#reloadRoot(root: string): Promise<string | null> {
		if (this.#disposed || this.#watch.view.head === null) return Promise.resolve(null);
		if (this.#view.state !== 'ready') {
			this.reload();
			return Promise.resolve(null);
		}
		const gen = this.#gen;
		const token = this.#bump(root);
		return this.#readFirst(root).then((a) => {
			if (gen !== this.#gen || token !== this.#rgen.get(root) || this.#disposed) return null;
			if (!a.ok) {
				// What was shown stays when a reload fails.
				const text = failureText(failure(a.e));
				this.#set({ moreProblem: text });
				return text;
			}
			if (!this.#wanted.includes(root)) return null;
			const held = new Map(this.#streams);
			held.set(root, { commits: [...a.r.commits], tips: a.r.tips, more: a.r.more, loading: false });
			this.#install(new Map(this.#wanted.filter((r) => held.has(r)).map((r) => [r, held.get(r)!])), null);
			if (root !== WORKSPACE_ROOT && !this.#view.rootRefs.has(root)) this.#readRootRefs(root);
			return null;
		});
	}

	#bump(root: string): number {
		const n = (this.#rgen.get(root) ?? 0) + 1;
		this.#rgen.set(root, n);
		return n;
	}

	#readFirst(root: string): Promise<First> {
		this.#reading++;
		return this.#client.call('git.log', { ...rootArg(root) }, { workspace: this.#ws.root.trim() }).then(
			(r): First => ({ ok: true, r }),
			(e: unknown): First => ({ ok: false, e }),
		).finally(() => {
			this.#reading--;
		});
	}

	/** The root list changed: roots removed leave the log, roots added are read. */
	#sync(): void {
		if (this.#disposed || !this.#started || this.#view.state !== 'ready') return;
		const kept = new Map([...this.#streams].filter(([r]) => this.#wanted.includes(r)));
		if (kept.size !== this.#streams.size) {
			const rootRefs = new Map([...this.#view.rootRefs].filter(([r]) => kept.has(r)));
			this.#install(kept, null);
			this.#set({ rootRefs });
		}
		for (const r of this.#wanted) if (!this.#streams.has(r)) void this.#reloadRoot(r);
	}

	/**
	 * Makes `streams` the log: the merge, the lanes and the rows are built again from what the streams hold; the selection,
	 * the opened commit and the scroll stay on their rows when those are still there.
	 */
	#install(streams: Map<string, RootStream>, problem: string | null): void {
		const keepScroll = this.#anchor;
		const keepSelected = this.#view.selected;
		this.#streams = streams;
		const roots = [...streams.keys()];
		this.#merge = new Merge<Commit>(roots);
		this.#emitted = [];
		for (const r of roots) {
			const s = streams.get(r)!;
			for (const m of this.#merge.push(r, s.commits, s.more)) this.#emitted.push(m);
		}
		if (this.#filter !== null) {
			const kept = roots.filter((r) => this.#filter!.has(r));
			this.#filter = kept.length === 0 || kept.length >= roots.length ? null : new Set(kept);
		}
		const rows = this.#relay();
		for (const k of [...this.#shows.keys()]) if (!this.#index.has(k)) this.#shows.delete(k);
		const open = this.#view.expanded;
		const expanded = open !== null && this.#index.has(open.key) ? open : null;
		if (expanded === null && open !== null) this.#showGen++;
		const selected = keepSelected !== null && this.#index.has(keepSelected) ? keepSelected : null;
		const oid = keepScroll !== null && this.#index.has(keepScroll) ? keepScroll : null;
		this.#set({
			state: 'ready',
			problem: null,
			rows,
			roots,
			filter: this.#filter === null ? null : roots.filter((r) => this.#filter!.has(r)),
			more: this.#more(),
			loadingMore: this.#pageP !== null && this.#pageP.gen === this.#gen,
			moreProblem: problem,
			selected,
			expanded,
			width: this.#lanes.width,
			restore: { seq: ++this.#restoreSeq, oid },
		});
	}

	/** The rows shown laid out again from what the merge let out, through the filter. */
	#relay(): HistoryRow[] {
		this.#lanes = new Lanes();
		this.#index = new Map();
		return this.#lay(filterRows(this.#emitted, this.#filter));
	}

	/** A page of `root` arrived: the merge lets out what it can and the rows shown grow by those of the roots chosen. */
	#extend(root: string, commits: readonly Commit[], more: boolean): void {
		const out = this.#merge.push(root, commits, more);
		for (const m of out) this.#emitted.push(m);
		const shown = this.#lay(filterRows(out, this.#filter));
		if (shown.length > 0) this.#set({ rows: [...this.#view.rows, ...shown], more: this.#more(), width: this.#lanes.width });
		else this.#set({ more: this.#more() });
	}

	dispose(): void {
		this.#disposed = true;
		this.#gen++;
		this.#refsGen++;
		this.#showGen++;
		this.#off();
		this.#listeners.clear();
	}

	#lay(mixed: readonly MixedRow<Commit>[]): HistoryRow[] {
		const base = this.#index.size;
		const lanes = this.#lanes.extend(mixed.map(laneInput));
		const out: HistoryRow[] = new Array(mixed.length);
		for (let i = 0; i < mixed.length; i++) {
			this.#index.set(mixed[i].key, base + i);
			out[i] = { key: mixed[i].key, root: mixed[i].root, commit: mixed[i].commit, lane: lanes[i] };
		}
		return out;
	}

	/** The watch changed: the Uncommitted row, the first read, a new HEAD. */
	#watched(): void {
		if (this.#disposed) return;
		const w = this.#watch.view;
		const headOid = w.head === null ? this.#view.headOid : (w.head.oid ?? '');
		this.#set({ uncommitted: w.head === null ? 0 : w.entries.length, uncommittedTruncated: w.truncated, headOid });
		if (w.head === null) return;
		const moved = this.#seenOid !== undefined && this.#seenOid !== headOid;
		this.#seenOid = headOid;
		if (!this.#started) {
			this.reload();
			this.refreshRefs();
		} else if (moved) {
			void this.#reloadRoot(WORKSPACE_ROOT);
			this.refreshRefs();
		}
	}

	#set(change: Partial<HistoryView>): void {
		const v = this.#view;
		let same = true;
		for (const k of Object.keys(change) as (keyof HistoryView)[]) {
			if (v[k] !== change[k]) {
				same = false;
				break;
			}
		}
		if (same) return;
		this.#view = { ...v, ...change };
		for (const l of [...this.#listeners]) l();
	}
}

function sameList(a: readonly string[], b: readonly string[]): boolean {
	return a.length === b.length && a.every((x, i) => x === b[i]);
}

/** "5 min ago", "3 h ago"... for a commit time in Unix seconds; `nowMs` is the clock in ms. */
export function relativeTime(unixSeconds: number, nowMs: number): string {
	const s = Math.max(0, Math.floor(nowMs / 1000 - unixSeconds));
	if (s < 45) return 'just now';
	if (s < 3600) return `${Math.max(1, Math.round(s / 60))} min ago`;
	if (s < 86400) return `${Math.round(s / 3600)} h ago`;
	if (s < 7 * 86400) return `${Math.round(s / 86400)} d ago`;
	if (s < 30 * 86400) return `${Math.round(s / (7 * 86400))} w ago`;
	if (s < 365 * 86400) return `${Math.round(s / (30 * 86400))} mo ago`;
	return `${Math.round(s / (365 * 86400))} y ago`;
}

/** The avatar's letters: the first letters of the first and last word of the name (code points), else of the e-mail. */
export function initials(name: string, email: string): string {
	const words = name.trim().split(/\s+/).filter(Boolean);
	const pick = (w: string) => Array.from(w)[0] ?? '';
	let text = words.length === 0 ? pick(email.trim()) : words.length === 1 ? pick(words[0]) : pick(words[0]) + pick(words[words.length - 1]);
	text = text.toUpperCase();
	return text === '' ? '?' : text;
}

/** A hue, 0–359, from the e-mail (case-insensitive): the same person gets the same colour. */
export function avatarHue(email: string): number {
	let h = 0;
	for (const ch of email.trim().toLowerCase()) h = (h * 31 + (ch.codePointAt(0) ?? 0)) >>> 0;
	return h % 360;
}
