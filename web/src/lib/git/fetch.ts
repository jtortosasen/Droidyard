// The History tab's Fetch (plan m2n P2; ADR-0071 §1–§4): one model per project tab, shared by the
// toolbar's Fetch (every remote) and the Fetch of each remote in the side panel's Remotes section.
//   - `run` calls `git.fetch` once, with the remote's name or none for all of them; a second
//     fetch is ignored while one is in flight (the daemon would queue it behind the first: one
//     write at a time per repository, and the buttons say so by being disabled);
//   - it is `queued` or `running` as the activity feed (`operation.watch`) says of the
//     operation the call started, `running` until the feed has heard of it;
//   - `cancel` is `operation.cancel` on that operation, sent when its id is known; a cancelled
//     fetch is not a failure;
//   - success reloads the refs (`onFetched`): that follows the UI's own write and not "another
//     git", so it adds no signal (ADR-0070 §3);
//   - a failure is kept, as the daemon said it (code, message, next step), until the next run
//     or `dismiss`. An untrusted root is one: the button stays enabled and the refusal says why
//     (ADR-0071 §2).
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError, type CallOptions } from '../api/client';
import { failure } from '../build/failure';

type Calls = 'git.fetch' | 'operation.cancel';

export interface FetchClient {
	call<N extends Calls>(name: N, params: Params<N>, opts?: CallOptions): Promise<Result<N>>;
}

export interface FetchWorkspace {
	readonly root: string;
}

/** What the activity feed knows of an operation; undefined until it has heard of it. */
export interface FetchActivity {
	stateOf(operationId: string): string | undefined;
	subscribe(listener: () => void): () => void;
}

export type FetchState = 'idle' | 'queued' | 'running' | 'failed';

/** What the daemon said of a failed fetch: git's own words are the cause. */
export interface FetchFailure {
	code?: string;
	message: string;
	/** git's stderr from its first fatal or error line (ADR-0071 §5). */
	cause?: string;
	nextStep?: string;
}

function describe(e: unknown): FetchFailure {
	const f = failure(e);
	return { ...f, ...(e instanceof CapabilityError && e.causeText ? { cause: e.causeText } : {}) };
}

export interface FetchView {
	state: FetchState;
	/** The remote being fetched while `queued` or `running`; null: every remote. */
	remote: string | null;
	/** In flight: `queued` or `running`. */
	busy: boolean;
	/** The last fetch's failure while `failed`. */
	failure: FetchFailure | null;
	/** A cancel was asked for and the fetch has not ended yet. */
	cancelling: boolean;
}

export class FetchModel {
	#client: FetchClient;
	#ws: FetchWorkspace;
	#onFetched: () => void;
	#activity: FetchActivity | null;
	#listeners = new Set<() => void>();
	#view: FetchView | null = null;
	#state: FetchState = 'idle';
	#remote: string | null = null;
	#failure: FetchFailure | null = null;
	#opId: string | null = null;
	#cancelRequested = false;
	#off: (() => void) | null = null;
	#disposed = false;

	constructor(client: FetchClient, ws: FetchWorkspace, onFetched: () => void, activity: FetchActivity | null = null) {
		this.#client = client;
		this.#ws = ws;
		this.#onFetched = onFetched;
		this.#activity = activity;
	}

	get view(): FetchView {
		this.#view ??= {
			state: this.#state,
			remote: this.#remote,
			busy: this.#state === 'queued' || this.#state === 'running',
			failure: this.#failure,
			cancelling: this.#cancelRequested,
		};
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** Fetches `remote`, or every remote when it is absent. Ignored while a fetch is in flight. */
	async run(remote?: string): Promise<void> {
		if (this.#disposed || this.view.busy) return;
		this.#remote = remote ?? null;
		this.#failure = null;
		this.#opId = null;
		this.#cancelRequested = false;
		this.#set('running');
		this.#off = this.#activity?.subscribe(() => this.#sync()) ?? null;
		const onOperation = (id: string) => {
			this.#opId = id;
			if (this.#cancelRequested) void this.#sendCancel(id);
			this.#sync();
		};
		try {
			await this.#client.call('git.fetch', remote === undefined ? {} : { remote }, { workspace: this.#ws.root.trim(), onOperation });
		} catch (e) {
			this.#end();
			if (this.#disposed) return;
			// A cancel the user asked for is not a failure.
			if (this.#cancelRequested) return this.#finish('idle', null);
			return this.#finish('failed', describe(e));
		}
		this.#end();
		if (this.#disposed) return;
		this.#finish('idle', null);
		this.#onFetched();
	}

	/** Cancels the fetch in flight: `operation.cancel` on its operation, sent when the id is known. */
	cancel(): void {
		if (this.#disposed || !this.view.busy || this.#cancelRequested) return;
		this.#cancelRequested = true;
		this.#changed();
		if (this.#opId !== null) void this.#sendCancel(this.#opId);
	}

	/** Forgets a failure. */
	dismiss(): void {
		if (this.#state !== 'failed') return;
		this.#failure = null;
		this.#set('idle');
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
			// The operation ended first, or the connection went; the fetch's own answer settles it.
		}
	}

	/** `queued` when the activity feed says the daemon holds the operation behind another write. */
	#sync(): void {
		if (this.#disposed || this.#opId === null || (this.#state !== 'queued' && this.#state !== 'running')) return;
		const s = this.#activity?.stateOf(this.#opId);
		const next: FetchState = s === 'queued' ? 'queued' : 'running';
		if (next !== this.#state) this.#set(next);
	}

	#end(): void {
		this.#off?.();
		this.#off = null;
	}

	#finish(state: FetchState, f: FetchFailure | null): void {
		this.#failure = f;
		this.#opId = null;
		this.#cancelRequested = false;
		this.#set(state);
	}

	#set(state: FetchState): void {
		this.#state = state;
		this.#changed();
	}

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}
