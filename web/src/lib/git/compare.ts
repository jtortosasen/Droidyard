// Compare with current (plan m2n P9; ADR-0072), one model per project tab: the view the branch menu's
// entry opens in the bottom Git tab, with three reads and nothing written.
//   - "In <branch>, not in <current>": `git.log {from: [branch tip], exclude: [HEAD]}`, paged by `skip`;
//   - "In <current>, not in <branch>": the reverse, `{from: [HEAD], exclude: [branch tip]}`;
//   - the files between the two tips: one `git.show {oid: branch tip, base: HEAD}`, whose files are what
//     differs from HEAD to the tip (a file opens in the Diff tab as `compareFileOf`, HEAD's text against the tip's).
// The tips are read once, when the view opens: a HEAD that moves afterwards does not change the view; the
// user closes it and opens it again. Every answer of an older opening is dropped. The branch and the commits'
// texts are git's: the view builds parameters and messages, never markup.
import type { Params, Result } from '../api/capabilities.gen';
import type { CallOptions } from '../api/client';
import { failure, failureText } from '../build/failure';
import type { Show } from './files';
import type { Commit } from './history';

export interface CompareClient {
	call(name: 'git.log', params: Params<'git.log'>, opts?: CallOptions): Promise<Result<'git.log'>>;
	call(name: 'git.show', params: Params<'git.show'>, opts?: CallOptions): Promise<Result<'git.show'>>;
}

export interface CompareWorkspace {
	readonly root: string;
}

/** The commits one way, a page at a time. */
export interface CompareList {
	state: 'loading' | 'ready' | 'error';
	commits: readonly Commit[];
	more: boolean;
	loadingMore: boolean;
	/** The reason of a failed read (the first page, or a later one: the commits held stay), as text. */
	problem: string | null;
}

export interface CompareFiles {
	state: 'loading' | 'ready' | 'error';
	show: Show | null;
	problem: string | null;
}

export type CompareDirection = 'ahead' | 'behind';

export interface CompareView {
	/** False until a branch is compared and after the view is closed. */
	open: boolean;
	/** The branch compared, as the list shows it (feature/x, origin/feature/x). */
	branch: string;
	/** The current branch, or "HEAD" when it is detached. */
	current: string;
	/** The branch's tip, in full. */
	tip: string;
	/** HEAD's commit, in full. */
	head: string;
	/** In the branch, not in the current one. */
	ahead: CompareList;
	/** In the current one, not in the branch. */
	behind: CompareList;
	files: CompareFiles;
}

/** What opens the view. */
export interface CompareTarget {
	branch: string;
	current: string;
	tip: string;
	head: string;
}

/** How many commits one page asks for. */
export const COMPARE_PAGE = 200;

const LOADING_LIST: CompareList = { state: 'loading', commits: [], more: false, loadingMore: false, problem: null };
const CLOSED: CompareView = {
	open: false,
	branch: '',
	current: '',
	tip: '',
	head: '',
	ahead: LOADING_LIST,
	behind: LOADING_LIST,
	files: { state: 'loading', show: null, problem: null },
};

export const aheadTitle = (branch: string, current: string): string => `In ${branch}, not in ${current}`;
export const behindTitle = (branch: string, current: string): string => `In ${current}, not in ${branch}`;

export class CompareModel {
	#client: CompareClient;
	#ws: CompareWorkspace;
	#listeners = new Set<() => void>();
	#view: CompareView = CLOSED;
	/** Bumped by every opening and close: an answer of an older one is dropped. */
	#gen = 0;
	#disposed = false;

	constructor(client: CompareClient, ws: CompareWorkspace) {
		this.#client = client;
		this.#ws = ws;
	}

	get view(): CompareView {
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** Opens the view on `target` and reads the three answers; opening again replaces the view. */
	open(target: CompareTarget): void {
		if (this.#disposed) return;
		const gen = ++this.#gen;
		this.#view = {
			open: true,
			...target,
			ahead: LOADING_LIST,
			behind: LOADING_LIST,
			files: { state: 'loading', show: null, problem: null },
		};
		this.#emit();
		this.#page('ahead', gen);
		this.#page('behind', gen);
		this.#readFiles(gen);
	}

	close(): void {
		if (!this.#view.open) return;
		this.#gen++;
		this.#view = CLOSED;
		this.#emit();
	}

	/** The next page of one list, when there is one and none is on its way; also the retry of a failed one. */
	loadMore(direction: CompareDirection): void {
		const list = this.#view[direction];
		if (!this.#view.open || list.state !== 'ready' || !list.more || list.loadingMore) return;
		this.#page(direction, this.#gen);
	}

	/** Reads a list again from its first page after its first read failed. */
	retry(direction: CompareDirection): void {
		const v = this.#view;
		if (!v.open || v[direction].state !== 'error') return;
		this.#set(direction, LOADING_LIST);
		this.#page(direction, this.#gen);
	}

	retryFiles(): void {
		if (!this.#view.open || this.#view.files.state !== 'error') return;
		this.#view = { ...this.#view, files: { state: 'loading', show: null, problem: null } };
		this.#emit();
		this.#readFiles(this.#gen);
	}

	dispose(): void {
		this.#disposed = true;
		this.#gen++;
		this.#listeners.clear();
	}

	#params(direction: CompareDirection, skip: number): Params<'git.log'> {
		const v = this.#view;
		const [from, exclude] = direction === 'ahead' ? [v.tip, v.head] : [v.head, v.tip];
		return { from: [from], exclude: [exclude], skip, limit: COMPARE_PAGE };
	}

	#page(direction: CompareDirection, gen: number): void {
		const list = this.#view[direction];
		const first = list.state !== 'ready';
		if (!first) this.#set(direction, { ...list, loadingMore: true, problem: null });
		const held = first ? [] : list.commits;
		this.#client.call('git.log', this.#params(direction, held.length), { workspace: this.#ws.root.trim() }).then(
			(r) => {
				if (gen !== this.#gen) return;
				this.#set(direction, { state: 'ready', commits: [...held, ...r.commits], more: r.more, loadingMore: false, problem: null });
			},
			(e: unknown) => {
				if (gen !== this.#gen) return;
				const problem = failureText(failure(e));
				this.#set(direction, first ? { ...LOADING_LIST, state: 'error', problem } : { ...list, loadingMore: false, problem });
			},
		);
	}

	#readFiles(gen: number): void {
		const { tip, head } = this.#view;
		this.#client.call('git.show', { oid: tip, base: head }, { workspace: this.#ws.root.trim() }).then(
			(show) => {
				if (gen !== this.#gen) return;
				this.#view = { ...this.#view, files: { state: 'ready', show, problem: null } };
				this.#emit();
			},
			(e: unknown) => {
				if (gen !== this.#gen) return;
				this.#view = { ...this.#view, files: { state: 'error', show: null, problem: failureText(failure(e)) } };
				this.#emit();
			},
		);
	}

	#set(direction: CompareDirection, list: CompareList): void {
		this.#view = { ...this.#view, [direction]: list };
		this.#emit();
	}

	#emit(): void {
		for (const l of [...this.#listeners]) l();
	}
}
