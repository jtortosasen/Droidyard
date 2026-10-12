// The History tab's store (plan m2n step 4; ADR-0069 §1, §2, §6; ADR-0070 §1–§3): one per
// project tab. It reads `git.log` and keeps the rows the panel draws:
//   - the first page is called without `from`; its `tips` come back and every later page passes
//     them with `skip` = the commits received, so the pages stay one walk while refs move;
//   - `Lanes` is extended page by page (ADR-0070 §1);
//   - nothing is read before the Git watch has a status (the project is open and is a
//     repository); `retry()` reads again after a failure.
// When it reloads (ADR-0070 §3; this step: the watch's `head.oid` changes; step 6 adds the refs'
// tips): the history starts again from its first page, the rows being replaced when the page
// arrives (what is shown stays until then). The selection stays when its oid is among the new
// rows, else it clears; `restore` names the oid the scroll returns to (the one at the top of the
// view when still loaded) or null for the top. A reload overtakes a page in flight; its answer
// is dropped.
// The Uncommitted Changes row is the watch's entries (none: no row).
import type { Params, Result } from '../api/capabilities.gen';
import type { CallOptions } from '../api/client';
import { failure, failureText } from '../build/failure';
import { Lanes, type LaneRow } from './lanes';
import type { GitView } from './watch';

export type Commit = Result<'git.log'>['commits'][number];

export interface HistoryClient {
	call(name: 'git.log', params: Params<'git.log'>, opts?: CallOptions): Promise<Result<'git.log'>>;
}

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
	/** Set by each reload that finished: where the scroll goes (an oid, or null for the top). `seq` makes each one a new request. */
	restore: { seq: number; oid: string | null } | null;
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
	restore: null,
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
	#disposed = false;

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
		if (oid === this.#view.selected) return;
		this.#set({ selected: oid });
	}

	/** The next page, when there is one and none is on its way. */
	loadMore(): void {
		const v = this.#view;
		if (this.#disposed || v.state !== 'ready' || !v.more || v.loadingMore || this.#tips.length === 0) return;
		const gen = this.#gen;
		this.#set({ loadingMore: true, moreProblem: null });
		this.#client.call('git.log', { from: this.#tips, skip: v.rows.length }, { workspace: this.#ws.root.trim() }).then(
			(r) => {
				if (gen !== this.#gen) return;
				const rows = [...this.#view.rows, ...this.#lay(r.commits)];
				this.#set({ rows, more: r.more, loadingMore: false, width: this.#lanes.width });
			},
			(e: unknown) => {
				if (gen !== this.#gen) return;
				this.#set({ loadingMore: false, moreProblem: failureText(failure(e)) });
			},
		);
	}

	/** Reads the history again from its first page (the Retry button, and the reload rule). */
	reload(): void {
		if (this.#disposed || this.#watch.view.head === null) return;
		this.#started = true;
		const gen = ++this.#gen;
		if (this.#view.state !== 'ready') this.#set({ state: 'loading', problem: null });
		this.#client.call('git.log', {}, { workspace: this.#ws.root.trim() }).then(
			(r) => {
				if (gen !== this.#gen) return;
				const keepScroll = this.#anchor;
				const keepSelected = this.#view.selected;
				this.#lanes = new Lanes();
				this.#index = new Map();
				this.#tips = r.tips;
				const rows = this.#lay(r.commits);
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
					width: this.#lanes.width,
					restore: { seq: ++this.#restoreSeq, oid },
				});
			},
			(e: unknown) => {
				if (gen !== this.#gen) return;
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
		if (!this.#started || moved) this.reload();
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
