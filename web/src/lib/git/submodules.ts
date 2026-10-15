// The History tab's Submodules section (plan m2n P4; ADR-0071 §1, §7): one model per project tab.
//   - `load` calls `git.submodules` and keeps its list; `follow` calls it whenever the History store
//     has a new answer of the refs (the first status, a new HEAD, the tab shown, the window focused,
//     and after a write of the UI's own, all of which read the refs again), so the section is as
//     fresh as the branches beside it;
//   - one read at a time: a `load` while one is in flight marks "again" and one more read runs when it
//     ends, so repeated triggers never stack walks of the daemon; a read that fails keeps what was shown
//     (a first read that fails shows its reason, and Retry reads again);
//   - the rows are pure over the list and the filter box: the path is matched as text,
//     case-insensitively, and a row says its state and its commit, both the recorded and the
//     checked-out one when they differ.
// Every path and oid here is git's text; nothing becomes markup.
import type { Params, Result } from '../api/capabilities.gen';
import type { CallOptions } from '../api/client';
import { failure, failureText } from '../build/failure';
import { rowKey } from './mixed';
import type { RefsResult } from './refs';

export type SubmodulesResult = Result<'git.submodules'>;
export type Submodule = SubmodulesResult['submodules'][number];
export type SubmoduleState = Submodule['state'];

export interface SubmodulesClient {
	call(name: 'git.submodules', params: Params<'git.submodules'>, opts?: CallOptions): Promise<Result<'git.submodules'>>;
}

export interface SubmodulesWorkspace {
	readonly root: string;
}

/** What `follow` reads of the History store. */
export interface RefsSource {
	readonly view: { readonly refs: unknown };
	subscribe(listener: () => void): () => void;
}

export interface SubmodulesView {
	/** `idle`: never read; `loading`: the first read is on its way; `error`: the first read failed (a later failure keeps the list). */
	state: 'idle' | 'loading' | 'ready' | 'error';
	/** The reason of `error`, as text. */
	problem: string | null;
	list: readonly Submodule[];
	/** The repository has more submodules than the daemon lists. */
	truncated: boolean;
}

const EMPTY: SubmodulesView = { state: 'idle', problem: null, list: [], truncated: false };

export class SubmodulesModel {
	#client: SubmodulesClient;
	#ws: SubmodulesWorkspace;
	#listeners = new Set<() => void>();
	#view: SubmodulesView = EMPTY;
	/** The reading in flight, with the reads that followed it. */
	#running: Promise<void> | null = null;
	/** A call came while a read was in flight: one more read is wanted when it ends. */
	#again = false;
	#disposed = false;

	constructor(client: SubmodulesClient, ws: SubmodulesWorkspace) {
		this.#client = client;
		this.#ws = ws;
	}

	get view(): SubmodulesView {
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/**
	 * Reads the submodules; resolves when the reading it asked for has ended. One read runs at a time
	 * (the daemon walks every submodule, and repeated focus would stack walks): a call made while one is
	 * in flight marks that another is wanted, and exactly one more read runs when this one ends.
	 */
	load(): Promise<void> {
		if (this.#disposed) return Promise.resolve();
		if (this.#running !== null) {
			this.#again = true;
			return this.#running;
		}
		this.#running = this.#drain();
		return this.#running;
	}

	async #drain(): Promise<void> {
		try {
			do {
				this.#again = false;
				await this.#read();
			} while (this.#again && !this.#disposed);
		} finally {
			this.#running = null;
		}
	}

	async #read(): Promise<void> {
		if (this.#view.state === 'idle' || this.#view.state === 'error') this.#set({ state: 'loading', problem: null });
		try {
			const r = await this.#client.call('git.submodules', {}, { workspace: this.#ws.root.trim() });
			if (this.#disposed) return;
			this.#set({ state: 'ready', problem: null, list: r.submodules, truncated: r.truncated });
		} catch (e) {
			if (this.#disposed) return;
			// What was shown stays when a later read fails.
			if (this.#view.state !== 'ready') this.#set({ state: 'error', problem: failureText(failure(e)) });
		}
	}

	/** Loads now if the refs are there, and again whenever the store holds a new answer of them. Returns the unsubscribe. */
	follow(source: RefsSource): () => void {
		let last = source.view.refs;
		if (last !== null) void this.load();
		return source.subscribe(() => {
			const now = source.view.refs;
			if (now === last) return;
			last = now;
			if (now !== null) void this.load();
		});
	}

	dispose(): void {
		this.#disposed = true;
		this.#listeners.clear();
	}

	#set(change: Partial<SubmodulesView>): void {
		this.#view = { ...this.#view, ...change };
		for (const l of [...this.#listeners]) l();
	}
}

/** The oid as the table's SHA column shows it. */
export const shortOid = (oid: string): string => oid.slice(0, 7);

export interface SubmoduleRow {
	path: string;
	/** What the panel calls it (names.ts): the last path segment, more of the path only when another root has the same. */
	name: string;
	state: SubmoduleState;
	recorded: string;
	checkedOut?: string;
	/** The short oid(s): the checked-out one then the recorded one when they differ ("abc1234 → def5678"), else the one commit. */
	oids: string;
	/** The branch checked out, or "detached" with the branches at its commit; absent while its refs are not known (or are older than the commit shown). */
	branch?: string;
	/** The whole branch text, for the title. */
	branchTitle?: string;
	/** The subject of the commit checked out, once read. */
	subject?: string;
	/** What the row's title says, in words. */
	title: string;
}

const STATE_TEXT: Readonly<Record<SubmoduleState, string>> = {
	uninitialised: 'not initialised',
	clean: 'at the recorded commit',
	outdated: 'checked out at another commit than the recorded one',
	dirty: 'has uncommitted changes',
};

/** How many branches a detached HEAD names in the row; the title lists them all. */
const DETACHED_SHOWN = 3;

/**
 * The branch a submodule shows (plan m2n R1) from its own refs: the current branch's name; for a detached HEAD
 * "detached" and the local and remote-tracking branches whose tip is that commit. Nothing when the refs are not
 * known or describe another commit than the one the row shows (they are read after the list, and may be older).
 */
export function submoduleBranch(refs: RefsResult | undefined, checkedOut: string | undefined): { text: string; title: string } | undefined {
	if (refs === undefined || checkedOut === undefined || refs.head.oid !== checkedOut) return undefined;
	if (!refs.head.detached) return refs.head.branch === undefined ? undefined : { text: refs.head.branch, title: refs.head.branch };
	const at = [
		...refs.branches.filter((b) => b.oid === checkedOut).map((b) => b.name),
		...refs.remotes.filter((r) => r.name !== 'HEAD' && r.oid === checkedOut).map((r) => `${r.remote}/${r.name}`),
	];
	if (at.length === 0) return { text: 'detached', title: 'detached' };
	const more = at.length - DETACHED_SHOWN;
	return { text: `detached (${at.slice(0, DETACHED_SHOWN).join(', ')}${more > 0 ? `, +${more}` : ''})`, title: `detached at ${at.join(', ')}` };
}

/** What the rows read besides the list: the display names, each root's refs, and the subject of each HEAD (by `rowKey`). */
export interface RowContext {
	names?: ReadonlyMap<string, string>;
	refs?: ReadonlyMap<string, RefsResult>;
	subjects?: ReadonlyMap<string, string>;
}

/** The rows the section shows for `list` under the filter: those whose path contains it. */
export function submoduleRows(list: readonly Submodule[], filter: string, ctx: RowContext = {}): SubmoduleRow[] {
	const q = filter.trim().toLowerCase();
	const rows: SubmoduleRow[] = [];
	for (const s of list) {
		if (q !== '' && !s.path.toLowerCase().includes(q)) continue;
		const differs = s.checkedOut !== undefined && s.checkedOut !== s.recorded;
		const branch = submoduleBranch(ctx.refs?.get(s.path), s.checkedOut);
		const subject = s.checkedOut === undefined ? undefined : ctx.subjects?.get(rowKey(s.path, s.checkedOut));
		rows.push({
			path: s.path,
			name: ctx.names?.get(s.path) ?? s.path,
			state: s.state,
			recorded: s.recorded,
			...(s.checkedOut !== undefined ? { checkedOut: s.checkedOut } : {}),
			oids: differs ? `${shortOid(s.checkedOut ?? '')} → ${shortOid(s.recorded)}` : shortOid(s.recorded),
			...(branch ? { branch: branch.text, branchTitle: branch.title } : {}),
			...(subject !== undefined ? { subject } : {}),
			title: `${s.path}: ${STATE_TEXT[s.state]}${differs ? ` (checked out ${shortOid(s.checkedOut ?? '')}, recorded ${shortOid(s.recorded)})` : ''}`,
		});
	}
	return rows;
}

/** The roots of the log besides the workspace (plan m2n P7): the submodules that are checked out, by path. */
export function initialisedRoots(list: readonly Submodule[]): string[] {
	return list.filter((s) => s.state !== 'uninitialised').map((s) => s.path);
}
