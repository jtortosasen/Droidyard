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
// from. The selection stays when its oid is among the new rows, else it clears; `restore`
// names the oid the scroll returns to (the one at the top of the view when still loaded) or
// null for the top. A reload overtakes a page in flight; its answer is dropped.
// The refs (`git.refs`, step 6) are read with the first status, again when `head.oid` changes,
// when the tab is shown (`setVisible`) and when the window regains focus (`focused`). A refs
// answer whose tips differ from the history's own reloads the history, unless a read of the
// history is on its way (it asks git afresh).
// The Uncommitted Changes row is the watch's entries (none: no row).
import type { Params, Result } from '../api/capabilities.gen';
import type { CallOptions } from '../api/client';
import { failure, failureText } from '../build/failure';
import { Lanes, type LaneRow } from './lanes';
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
	oid: string;
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
	/** The selected commit, when it is among the rows. */
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
	/** Set by each reload that finished: where the scroll goes (an oid, or null for the top). `seq` makes each one a new request. */
	restore: { seq: number; oid: string | null } | null;
	/** The commit opened under its row, or null. At most one. */
	expanded: ExpandedCommit | null;
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
};

export class HistoryStore {
	#client: HistoryClient;
	#ws: HistoryWorkspace;
	#watch: HistoryWatch;
	#listeners = new Set<() => void>();
	#off: () => void;
	#view: HistoryView = EMPTY;
	#lanes = new Lanes();
	#index = new Map<string, number>();
	#tips: string[] = [];
	/** Bumped by every reload: an answer of an older generation is dropped. */
	#gen = 0;
	#started = false;
	#anchor: string | null = null;
	#seenOid: string | undefined;
	#restoreSeq = 0;
	#scrollSeq = 0;
	#disposed = false;
	/** Bumped by every refs read: an answer of an older read is dropped. */
	#refsGen = 0;
	/** The first page of a reload is on its way. */
	#reading = false;
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

	/** The row index of an oid among the commits held, or -1. */
	indexOf(oid: string): number {
		return this.#index.get(oid) ?? -1;
	}

	/** The commit at the top of the view, which a reload scrolls back to. Not reactive. */
	setAnchor(oid: string | null): void {
		this.#anchor = oid;
	}

	get anchor(): string | null {
		return this.#anchor;
	}

	select(oid: string | null): void {
		if (oid !== null && !this.#index.has(oid)) return;
		this.#set({ selected: oid, notice: null });
	}

	/** Opens the commit under its row (and selects it): one `git.show`, kept while the row stays loaded. */
	expand(oid: string): void {
		if (this.#disposed || !this.#index.has(oid)) return;
		const gen = ++this.#showGen;
		const cached = this.#shows.get(oid);
		if (cached) {
			this.#set({ selected: oid, notice: null, expanded: { oid, state: 'ready', show: cached, problem: null } });
			return;
		}
		this.#set({ selected: oid, notice: null, expanded: { oid, state: 'loading', show: null, problem: null } });
		this.#client.call('git.show', { oid }, { workspace: this.#ws.root.trim() }).then(
			(r) => {
				if (gen !== this.#showGen || this.#disposed) return;
				if (this.#index.has(oid)) this.#shows.set(oid, r);
				this.#set({ expanded: { oid, state: 'ready', show: r, problem: null } });
			},
			(e: unknown) => {
				if (gen !== this.#showGen || this.#disposed) return;
				this.#set({ expanded: { oid, state: 'error', show: null, problem: failureText(failure(e)) } });
			},
		);
	}

	/** Closes the opened commit. */
	collapse(): void {
		this.#showGen++;
		this.#set({ expanded: null });
	}

	/** A click on a row: opens the commit, or closes it when it is the one open. */
	toggleExpand(oid: string): void {
		if (this.#view.expanded?.oid === oid) {
			this.collapse();
			this.select(oid);
		} else this.expand(oid);
	}

	/** A parent clicked in the opened commit: revealed (more pages loaded to find it) and opened. */
	async openParent(oid: string): Promise<void> {
		if (await this.reveal(oid)) this.expand(oid);
	}

	/** Selects a loaded commit and asks the table to bring its row into view. */
	show(oid: string): boolean {
		if (!this.#index.has(oid)) return false;
		this.#set({ selected: oid, notice: null, scrollTo: { seq: ++this.#scrollSeq, oid } });
		return true;
	}

	/**
	 * Selects the commit `oid` and shows it, loading at most REVEAL_PAGES more pages to find it;
	 * else says NOT_LOADED. Resolves to whether the commit was selected.
	 */
	async reveal(oid: string): Promise<boolean> {
		this.#set({ notice: null });
		const gen = this.#gen;
		for (let pages = 0; ; pages++) {
			if (this.#disposed || gen !== this.#gen) return false;
			if (this.show(oid)) return true;
			const v = this.#view;
			if (pages >= REVEAL_PAGES || v.state !== 'ready' || !(v.more || v.loadingMore)) break;
			await this.#nextPage();
			if (this.#view.moreProblem) break;
		}
		this.#set({ notice: NOT_LOADED });
		return false;
	}

	/** A ref picked in the side panel: it is the selected one, and its commit is revealed (a tag on a tree has none). */
	async selectRef(ref: string, oid: string | undefined): Promise<boolean> {
		this.#set({ selectedRef: ref, notice: null });
		if (oid === undefined) return false;
		return this.reveal(oid);
	}

	/** Navigate to HEAD: the current branch is the selected ref and HEAD's commit is revealed. */
	async navigateToHead(): Promise<boolean> {
		const head = this.#view.refs?.head;
		const oid = head?.oid ?? (this.#view.headOid || undefined);
		if (oid === undefined) return false;
		this.#set({ selectedRef: head && !head.detached && head.branch !== undefined ? localRef(head.branch) : null });
		return this.reveal(oid);
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
				if (this.#view.state === 'ready' && !this.#reading && !sameList(r.tips, this.#tips)) this.reload();
			},
			(e: unknown) => {
				if (gen !== this.#refsGen || this.#disposed) return;
				// What was shown stays when a later read fails.
				if (this.#view.refs === null) this.#set({ refsState: 'error', refsProblem: failureText(failure(e)) });
			},
		);
	}

	/** The next page, when there is one and none is on its way. */
	loadMore(): void {
		void this.#nextPage();
	}

	/** The page in flight, else a new one when there is one to read; resolves when it has been read or has failed. */
	#nextPage(): Promise<void> {
		if (this.#pageP && this.#pageP.gen === this.#gen) return this.#pageP.done;
		const v = this.#view;
		if (this.#disposed || v.state !== 'ready' || !v.more || v.loadingMore || this.#tips.length === 0) return Promise.resolve();
		const gen = this.#gen;
		this.#set({ loadingMore: true, moreProblem: null });
		const done: Promise<void> = this.#client
			.call('git.log', { from: this.#tips, skip: v.rows.length }, { workspace: this.#ws.root.trim() })
			.then(
				(r) => {
					if (gen !== this.#gen) return;
					const rows = [...this.#view.rows, ...this.#lay(r.commits)];
					this.#set({ rows, more: r.more, loadingMore: false, width: this.#lanes.width });
				},
				(e: unknown) => {
					if (gen !== this.#gen) return;
					this.#set({ loadingMore: false, moreProblem: failureText(failure(e)) });
				},
			)
			.finally(() => {
				if (this.#pageP?.done === done) this.#pageP = null;
			});
		this.#pageP = { gen, done };
		return done;
	}

	/** Reads the history again from its first page (the Retry button, and the reload rule). */
	reload(): void {
		if (this.#disposed || this.#watch.view.head === null) return;
		this.#started = true;
		const gen = ++this.#gen;
		this.#reading = true;
		if (this.#view.state !== 'ready') this.#set({ state: 'loading', problem: null });
		this.#client.call('git.log', {}, { workspace: this.#ws.root.trim() }).then(
			(r) => {
				if (gen !== this.#gen) return;
				this.#reading = false;
				const keepScroll = this.#anchor;
				const keepSelected = this.#view.selected;
				this.#lanes = new Lanes();
				this.#index = new Map();
				this.#tips = r.tips;
				const rows = this.#lay(r.commits);
				for (const k of [...this.#shows.keys()]) if (!this.#index.has(k)) this.#shows.delete(k);
				const open = this.#view.expanded;
				const expanded = open !== null && this.#index.has(open.oid) ? open : null;
				if (expanded === null && open !== null) this.#showGen++;
				const selected = keepSelected !== null && this.#index.has(keepSelected) ? keepSelected : null;
				const oid = keepScroll !== null && this.#index.has(keepScroll) ? keepScroll : null;
				this.#set({
					state: 'ready',
					problem: null,
					rows,
					more: r.more,
					loadingMore: false,
					moreProblem: null,
					selected,
					expanded,
					width: this.#lanes.width,
					restore: { seq: ++this.#restoreSeq, oid },
				});
			},
			(e: unknown) => {
				if (gen !== this.#gen) return;
				this.#reading = false;
				// What was shown stays when a reload fails; a first read shows the reason.
				const text = failureText(failure(e));
				if (this.#view.state === 'ready') this.#set({ moreProblem: text, loadingMore: false });
				else this.#set({ state: 'error', problem: text });
			},
		);
	}

	dispose(): void {
		this.#disposed = true;
		this.#gen++;
		this.#refsGen++;
		this.#showGen++;
		this.#off();
		this.#listeners.clear();
	}

	#lay(commits: readonly Commit[]): HistoryRow[] {
		const base = this.#index.size;
		const lanes = this.#lanes.extend(commits);
		const out: HistoryRow[] = new Array(commits.length);
		for (let i = 0; i < commits.length; i++) {
			this.#index.set(commits[i].oid, base + i);
			out[i] = { commit: commits[i], lane: lanes[i] };
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
		if (!this.#started || moved) {
			this.reload();
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
