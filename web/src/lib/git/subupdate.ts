// The Submodules section's Update (plan m2n P5; ADR-0071 §1–§5): one model per project tab, shared by the
// section's Update (every submodule) and the Update of each row.
//   - `update(path)` and `updateAll(listed)` each call `git.updateSubmodules` once, with `{paths: [path]}` or
//     with no paths (the daemon lists them afresh), after the dirty-buffer gate. An update is a checkout in
//     each submodule, so as for a branch checkout (ADR-0071 §5) the editor's unsaved files under the
//     submodules it would touch stop it before anything is called: the files are kept to be named
//     (`blocked`). A buffer outside every submodule does not;
//   - a second update is ignored while one is in flight (the daemon would queue it behind the first: one
//     write at a time per repository, and the buttons say so by being disabled); it is `queued` or `running`
//     as the activity feed (`operation.watch`) says of the operation the call started;
//   - `cancel` is `operation.cancel` on that operation, sent when its id is known; a cancelled update is not
//     a failure;
//   - when the call has ended, however it ended, `onDone` reads the submodules again: a failed or cancelled
//     update may still have moved some of them. That follows the UI's own write and not "another git";
//   - a run that did not fail says what it did (plan m2n R1): `onDone` resolves when the submodules have been read
//     again, and the checked-out commits before and after are compared (`summarise`): the submodules that moved,
//     or that all of them were already at the recorded commits. A read that did not happen (the list is the same
//     array) says nothing rather than something false. It is kept until the next run or `dismiss`;
//   - a failure is kept, as the daemon said it (code, message, git's words as the cause, next step), until
//     the next run or `dismiss`. An untrusted root is one: the button stays enabled and the refusal says
//     why (ADR-0071 §2).
// A clean buffer whose file the update rewrote reloads through the file watch (plan m2i D8); nothing here
// touches the editor. Every path and message here is git's or the daemon's text, never markup.
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError, type CallOptions } from '../api/client';
import { failure } from '../build/failure';
import type { DirtySource } from './branchops';
import { shortOid, type Submodule } from './submodules';

type Calls = 'git.updateSubmodules' | 'operation.cancel';

export interface UpdateClient {
	call<N extends Calls>(name: N, params: Params<N>, opts?: CallOptions): Promise<Result<N>>;
}

export interface UpdateWorkspace {
	readonly root: string;
}

/** What the activity feed knows of an operation; undefined until it has heard of it. */
export interface UpdateActivity {
	stateOf(operationId: string): string | undefined;
	subscribe(listener: () => void): () => void;
}

export type UpdateState = 'idle' | 'queued' | 'running' | 'failed';

/** What the daemon said of a failed update: git's own words are the cause. */
export interface UpdateFailure {
	code?: string;
	message: string;
	/** git's stderr from its first fatal or error line (ADR-0071 §5); text of the repository. */
	cause?: string;
	nextStep?: string;
}

function describe(e: unknown): UpdateFailure {
	const f = failure(e);
	return { ...f, ...(e instanceof CapabilityError && e.causeText ? { cause: e.causeText } : {}) };
}

/**
 * The unsaved files an update of `paths` (submodule paths, relative to the project root) would meet: those
 * in a submodule's directory or below it. A file elsewhere, or one whose name only begins with the same
 * letters ("libx/a" against "lib"), is not.
 */
export function dirtyUnder(source: DirtySource, paths: readonly string[]): string[] {
	return source.dirtyPaths().filter((file) => paths.some((p) => file === p || file.startsWith(`${p}/`)));
}

/** The refusal the block shows, as text. */
export function updateBlockText(files: readonly string[]): string {
	return `Save or close ${files.length === 1 ? 'this file' : 'these files'} first: ${files.length} ${files.length === 1 ? 'has' : 'have'} unsaved changes inside a submodule that the update would check out again.`;
}

/** What an update did: the submodules whose checked-out commit changed. */
export interface UpdateSummary {
	/** The submodule the update was for; null: every one. */
	scope: string | null;
	/** The submodules checked out after it (what "all" counts). */
	total: number;
	/** The ones that moved, with the short commit before (absent: not checked out) and after. */
	moved: readonly { path: string; from?: string; to: string }[];
}

/** The summary of an update from the list before it and the list after it. */
export function summarise(before: readonly Submodule[], after: readonly Submodule[], scope: string | null): UpdateSummary {
	const was = new Map(before.map((s) => [s.path, s.checkedOut]));
	const moved: { path: string; from?: string; to: string }[] = [];
	let total = 0;
	for (const s of after) {
		if (s.checkedOut === undefined) continue;
		total++;
		const from = was.get(s.path);
		if (from !== s.checkedOut) moved.push({ path: s.path, ...(from !== undefined ? { from: shortOid(from) } : {}), to: shortOid(s.checkedOut) });
	}
	return { scope, total, moved };
}

/** How many moved submodules the text names before "and N more". */
const SUMMARY_SHOWN = 6;

/** The summary as text; `name` is what the panel calls a submodule (its path when no name is known). */
export function summaryText(s: UpdateSummary, name: (path: string) => string = (p) => p): string {
	if (s.moved.length === 0) {
		if (s.scope !== null) return `${name(s.scope)} is already at the recorded commit`;
		if (s.total === 0) return 'No submodule is checked out; nothing to update';
		return s.total === 1 ? 'The only submodule is already at the recorded commit' : `All ${s.total} submodules already at the recorded commits`;
	}
	const shown = s.moved.slice(0, SUMMARY_SHOWN).map((m) => `${name(m.path)} (${m.from === undefined ? 'now ' : `${m.from} → `}${m.to})`);
	const more = s.moved.length - shown.length;
	return `Updated ${s.moved.length} submodule${s.moved.length === 1 ? '' : 's'}: ${shown.join(', ')}${more > 0 ? ` and ${more} more` : ''}`;
}

export interface UpdateView {
	state: UpdateState;
	/** The submodule being updated while `queued` or `running`; null: every one (or nothing is in flight). */
	path: string | null;
	/** Whether the update in flight is of every submodule. */
	all: boolean;
	/** In flight: `queued` or `running`. */
	busy: boolean;
	/** The last update's failure while `failed`. */
	failure: UpdateFailure | null;
	/** A cancel was asked for and the update has not ended yet. */
	cancelling: boolean;
	/** The unsaved files that stopped the last update before it was called; empty when none did. */
	blocked: readonly string[];
	/** The message of the block, as text. */
	blockedText: string;
	/** What the last update that did not fail did; null before one, after `dismiss` and while the next runs. */
	summary: UpdateSummary | null;
}

export class SubmoduleUpdateModel {
	#client: UpdateClient;
	#ws: UpdateWorkspace;
	#dirty: DirtySource;
	#onDone: () => void | Promise<void>;
	#list: () => readonly Submodule[];
	#activity: UpdateActivity | null;
	#listeners = new Set<() => void>();
	#view: UpdateView | null = null;
	#state: UpdateState = 'idle';
	#path: string | null = null;
	#all = false;
	#failure: UpdateFailure | null = null;
	#blocked: readonly string[] = [];
	#summary: UpdateSummary | null = null;
	#runs = 0;
	#opId: string | null = null;
	#cancelRequested = false;
	#off: (() => void) | null = null;
	#disposed = false;

	/**
	 * `onDone` reads the submodules again (and may return the promise of that read); `list` is the list held now, which
	 * the summary compares before and after the run (without it an update says nothing of what it did).
	 */
	constructor(
		client: UpdateClient,
		ws: UpdateWorkspace,
		dirty: DirtySource,
		onDone: () => void | Promise<void>,
		activity: UpdateActivity | null = null,
		list: () => readonly Submodule[] = () => [],
	) {
		this.#client = client;
		this.#ws = ws;
		this.#dirty = dirty;
		this.#onDone = onDone;
		this.#list = list;
		this.#activity = activity;
	}

	get view(): UpdateView {
		this.#view ??= {
			state: this.#state,
			path: this.#path,
			all: this.#all,
			busy: this.#state === 'queued' || this.#state === 'running',
			failure: this.#failure,
			cancelling: this.#cancelRequested,
			blocked: this.#blocked,
			blockedText: this.#blocked.length === 0 ? '' : updateBlockText(this.#blocked),
			summary: this.#summary,
		};
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** Updates the submodule at `path` (and what is inside it). Ignored while an update is in flight. */
	update(path: string): Promise<void> {
		return this.#run([path], { paths: [path] }, path, false);
	}

	/**
	 * Updates every submodule; `listed` are the paths the section shows, which the gate checks the buffers
	 * against. The daemon lists them afresh, so a submodule added since the last read is not checked: the
	 * section is as fresh as the refs beside it, and git refuses what it cannot carry over.
	 */
	updateAll(listed: readonly string[]): Promise<void> {
		return this.#run(listed, {}, null, true);
	}

	/** Cancels the update in flight: `operation.cancel` on its operation, sent when the id is known. */
	cancel(): void {
		if (this.#disposed || !this.view.busy || this.#cancelRequested) return;
		this.#cancelRequested = true;
		this.#changed();
		if (this.#opId !== null) void this.#sendCancel(this.#opId);
	}

	/** Forgets a failure, a block or the summary. */
	dismiss(): void {
		if (this.#state !== 'failed' && this.#blocked.length === 0 && this.#summary === null) return;
		this.#summary = null;
		this.#failure = null;
		this.#blocked = [];
		if (this.#state === 'failed') this.#state = 'idle';
		this.#changed();
	}

	dispose(): void {
		this.#disposed = true;
		this.#end();
		this.#listeners.clear();
	}

	async #run(under: readonly string[], params: Params<'git.updateSubmodules'>, path: string | null, all: boolean): Promise<void> {
		if (this.#disposed || this.view.busy) return;
		const blocked = dirtyUnder(this.#dirty, under);
		if (blocked.length > 0) {
			this.#failure = null;
			this.#blocked = blocked;
			this.#path = path;
			this.#all = all;
			this.#state = 'idle';
			this.#changed();
			return;
		}
		this.#blocked = [];
		this.#failure = null;
		this.#summary = null;
		const run = ++this.#runs;
		const before = this.#list();
		this.#path = path;
		this.#all = all;
		this.#opId = null;
		this.#cancelRequested = false;
		this.#set('running');
		this.#off = this.#activity?.subscribe(() => this.#sync()) ?? null;
		const onOperation = (id: string) => {
			this.#opId = id;
			if (this.#cancelRequested) void this.#sendCancel(id);
			this.#sync();
		};
		let failed: UpdateFailure | null = null;
		try {
			await this.#client.call('git.updateSubmodules', params, { workspace: this.#ws.root.trim(), onOperation });
		} catch (e) {
			// A cancel the user asked for is not a failure.
			if (!this.#cancelRequested) failed = describe(e);
		}
		this.#end();
		if (this.#disposed) return;
		const cancelled = this.#cancelRequested;
		this.#failure = failed;
		this.#opId = null;
		this.#cancelRequested = false;
		this.#set(failed === null ? 'idle' : 'failed');
		try {
			await this.#onDone();
		} catch {
			// The read of the list failed: the list is the same array, which says nothing below.
		}
		if (this.#disposed || failed !== null || run !== this.#runs) return;
		const after = this.#list();
		// The list was not read again (the read failed): nothing is said rather than something false.
		if (after === before) return;
		const summary = summarise(before, after, path);
		// A cancelled run that moved nothing did not "find everything already there".
		if (cancelled && summary.moved.length === 0) return;
		this.#summary = summary;
		this.#changed();
	}

	async #sendCancel(operationId: string): Promise<void> {
		try {
			await this.#client.call('operation.cancel', { operationId });
		} catch {
			// The operation ended first, or the connection went; the update's own answer settles it.
		}
	}

	/** `queued` when the activity feed says the daemon holds the operation behind another write. */
	#sync(): void {
		if (this.#disposed || this.#opId === null || (this.#state !== 'queued' && this.#state !== 'running')) return;
		const s = this.#activity?.stateOf(this.#opId);
		const next: UpdateState = s === 'queued' ? 'queued' : 'running';
		if (next !== this.#state) this.#set(next);
	}

	#end(): void {
		this.#off?.();
		this.#off = null;
	}

	#set(state: UpdateState): void {
		this.#state = state;
		this.#changed();
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}
