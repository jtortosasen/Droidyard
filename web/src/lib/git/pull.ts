// The History tab's Pull (plan m2n R3; ADR-0074 §1, §2), a fast-forward of the current branch to its
// upstream, after fetch.ts's model: one per project tab, behind the toolbar's Pull.
//   - `run` first asks the editor for unsaved files, as a checkout does (branchops.ts's gate): a pull that
//     moves the work tree would overwrite them, so with any it calls nothing and keeps the files to name
//     (`blocked`); otherwise it calls `git.pull` (no parameter); a second pull is ignored while one is in
//     flight (the daemon would queue it: one write at a time per repository);
//   - it is `queued` or `running` as the activity feed (`operation.watch`) says of the operation the call
//     started, `running` until the feed has heard of it;
//   - `cancel` is `operation.cancel` on that operation, sent when its id is known; a cancelled pull is not
//     a failure;
//   - success keeps what the daemon answered (`result`: up to date, or the two ids) until the next run or
//     `dismiss`, and reloads the refs (`onPulled`); the log follows because HEAD moved (the status watch);
//   - a failure is kept as the daemon said it (code, message, cause, next step) until the next run or
//     `dismiss`: a detached HEAD, a branch with no upstream, a branch that cannot be fast-forwarded and local
//     changes in the way arrive as the daemon's GIT_FAILED with git's words (ADR-0074 §2).
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError, type CallOptions } from '../api/client';
import { failure } from '../build/failure';
import type { DirtySource } from './branchops';

type Calls = 'git.pull' | 'operation.cancel';

export interface PullClient {
	call<N extends Calls>(name: N, params: Params<N>, opts?: CallOptions): Promise<Result<N>>;
}

export interface PullWorkspace {
	readonly root: string;
}

/** What the activity feed knows of an operation; undefined until it has heard of it. */
export interface PullActivity {
	stateOf(operationId: string): string | undefined;
	subscribe(listener: () => void): () => void;
}

export type PullState = 'idle' | 'queued' | 'running' | 'failed';

/** What the daemon said of a failed pull: git's own words are the cause. */
export interface PullFailure {
	code?: string;
	message: string;
	/** git's stderr from its first fatal or error line (ADR-0071 §5). */
	cause?: string;
	nextStep?: string;
}

/** What a pull did: nothing to bring in, or the branch moved from one commit to another. */
export interface PullSummary {
	branch: string;
	upToDate: boolean;
	/** The short ids the branch moved between; both empty when it was up to date. */
	from: string;
	to: string;
}

const SHORT = 7;

/** The summary of a `git.pull` answer: equal ids mean it was already up to date (ADR-0074 §1). */
export function summarise(r: { branch: string; before: string; after: string }): PullSummary {
	const upToDate = r.before === r.after;
	return { branch: r.branch, upToDate, from: upToDate ? '' : r.before.slice(0, SHORT), to: upToDate ? '' : r.after.slice(0, SHORT) };
}

/** The summary as the toolbar shows it: "Up to date", or `a1b2c3d..e4f5g6h` for the commits brought in. */
export function summaryText(s: PullSummary): string {
	return s.upToDate ? `${s.branch} is up to date` : `${s.branch} ${s.from}..${s.to}`;
}

/** The refusal the dirty-buffer gate shows. */
export function pullBlockText(files: readonly string[]): string {
	return `Save or close ${files.length === 1 ? 'this file' : 'these files'} first: ${files.length} ${files.length === 1 ? 'has' : 'have'} unsaved changes that the pull would overwrite.`;
}

function describe(e: unknown): PullFailure {
	const f = failure(e);
	return { ...f, ...(e instanceof CapabilityError && e.causeText ? { cause: e.causeText } : {}) };
}

export interface PullView {
	state: PullState;
	/** In flight: `queued` or `running`. */
	busy: boolean;
	/** The last pull's failure while `failed`. */
	failure: PullFailure | null;
	/** A cancel was asked for and the pull has not ended yet. */
	cancelling: boolean;
	/** What the last pull did, until the next run or a dismiss; null when it failed or none ran. */
	result: PullSummary | null;
	/** The unsaved files that stopped the last pull before it was called; empty when none did. */
	blocked: readonly string[];
	/** `blocked` as the refusal's text; empty when nothing was blocked. */
	blockedText: string;
}

export class PullModel {
	#client: PullClient;
	#ws: PullWorkspace;
	#dirty: DirtySource;
	#onPulled: () => void;
	#activity: PullActivity | null;
	#listeners = new Set<() => void>();
	#view: PullView | null = null;
	#state: PullState = 'idle';
	#failure: PullFailure | null = null;
	#result: PullSummary | null = null;
	#blocked: readonly string[] = [];
	#opId: string | null = null;
	#cancelRequested = false;
	#off: (() => void) | null = null;
	#disposed = false;

	constructor(client: PullClient, ws: PullWorkspace, dirty: DirtySource, onPulled: () => void, activity: PullActivity | null = null) {
		this.#client = client;
		this.#ws = ws;
		this.#dirty = dirty;
		this.#onPulled = onPulled;
		this.#activity = activity;
	}

	get view(): PullView {
		this.#view ??= {
			state: this.#state,
			busy: this.#state === 'queued' || this.#state === 'running',
			failure: this.#failure,
			cancelling: this.#cancelRequested,
			result: this.#result,
			blocked: this.#blocked,
			blockedText: this.#blocked.length === 0 ? '' : pullBlockText(this.#blocked),
		};
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** Pulls the current branch, unless an unsaved buffer stops it. Ignored while a pull is in flight. */
	async run(): Promise<void> {
		if (this.#disposed || this.view.busy) return;
		this.#failure = null;
		this.#result = null;
		const blocked = [...this.#dirty.dirtyPaths()];
		this.#blocked = blocked;
		if (blocked.length > 0) {
			this.#set('idle');
			return;
		}
		this.#opId = null;
		this.#cancelRequested = false;
		this.#set('running');
		this.#off = this.#activity?.subscribe(() => this.#sync()) ?? null;
		const onOperation = (id: string) => {
			this.#opId = id;
			if (this.#cancelRequested) void this.#sendCancel(id);
			this.#sync();
		};
		let res: Result<'git.pull'>;
		try {
			res = await this.#client.call('git.pull', {}, { workspace: this.#ws.root.trim(), onOperation });
		} catch (e) {
			this.#end();
			if (this.#disposed) return;
			// A cancel the user asked for is not a failure.
			if (this.#cancelRequested) return this.#finish('idle', null, null);
			return this.#finish('failed', describe(e), null);
		}
		this.#end();
		if (this.#disposed) return;
		this.#finish('idle', null, summarise(res));
		this.#onPulled();
	}

	/** Cancels the pull in flight: `operation.cancel` on its operation, sent when the id is known. */
	cancel(): void {
		if (this.#disposed || !this.view.busy || this.#cancelRequested) return;
		this.#cancelRequested = true;
		this.#changed();
		if (this.#opId !== null) void this.#sendCancel(this.#opId);
	}

	/** Forgets a failure, a block or the last result. */
	dismiss(): void {
		if (this.#failure === null && this.#result === null && this.#blocked.length === 0) return;
		this.#failure = null;
		this.#result = null;
		this.#blocked = [];
		this.#set(this.#state === 'failed' ? 'idle' : this.#state);
	}

	dispose(): void {
		this.#disposed = true;
		this.#end();
		this.#listeners.clear();
	}

	async #sendCancel(operationId: string): Promise<void> {
		try {
			await this.#client.call('operation.cancel', { operationId });
		} catch {
			// The operation ended first, or the connection went; the pull's own answer settles it.
		}
	}

	/** `queued` when the activity feed says the daemon holds the operation behind another write. */
	#sync(): void {
		if (this.#disposed || this.#opId === null || (this.#state !== 'queued' && this.#state !== 'running')) return;
		const s = this.#activity?.stateOf(this.#opId);
		const next: PullState = s === 'queued' ? 'queued' : 'running';
		if (next !== this.#state) this.#set(next);
	}

	#end(): void {
		this.#off?.();
		this.#off = null;
	}

	#finish(state: PullState, f: PullFailure | null, result: PullSummary | null): void {
		this.#failure = f;
		this.#result = result;
		this.#opId = null;
		this.#cancelRequested = false;
		this.#set(state);
	}

	#set(state: PullState): void {
		this.#state = state;
		this.#changed();
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}
