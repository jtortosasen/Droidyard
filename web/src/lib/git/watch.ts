// The Git status behind the Git tree and the Diff tab (plan m2l step 5; ADR-0066 §1, ADR-0067
// §5). One `git.watch` of the project, called without parameters: its answer carries the
// status (head, entries, truncated) and every `git.changes` `status` event carries the whole
// status again, so each one REPLACES the last. There are no changes to merge.
//
// When the watch starts again (a fresh status replaces the old one):
//   - after a lost connection, once the session is open;
//   - 2 s after a `stopped` event with reason `failed` (git could not report; it may be back);
//   - when the page is shown again (the watch ends while it is hidden, as FileWatch does);
//   - on `retry()`, which the Git tree offers.
// A `stopped` event with `workspaceClosed` ends it. A start the daemon refuses (not a
// repository, no git) is shown as text and is NOT retried on its own: only `retry()` tries
// again, so a project outside a repository does not poll the daemon.
// Every start ends the previous subscription with `events.unsubscribe` first.
import { ConnectionError, type CallOptions, type ConnectionState, type EventFrame, type SubscribeHandlers, type Subscription } from '../api/client';
import type { EventPayload, Params, Result } from '../api/capabilities.gen';
import { failure, failureText } from '../build/failure';
import type { Entry } from './changes';

/** How long after `stopped {failed}` the watch starts again. */
export const RESTART_MS = 2000;

export type GitState = 'idle' | 'starting' | 'watching' | 'waiting' | 'paused' | 'error' | 'ended';

export type Head = Result<'git.watch'>['head'];

export interface GitTimers {
	setTimeout(fn: () => void, ms: number): unknown;
	clearTimeout(handle: unknown): void;
}

const realTimers: GitTimers = {
	setTimeout: (fn, ms) => setTimeout(fn, ms),
	clearTimeout: (h) => clearTimeout(h as ReturnType<typeof setTimeout>),
};

export interface GitClient {
	call(name: 'events.unsubscribe', params: Params<'events.unsubscribe'>, opts?: CallOptions): Promise<Result<'events.unsubscribe'>>;
	subscribe(
		name: 'git.watch',
		params: Params<'git.watch'>,
		handlers: SubscribeHandlers,
		opts?: CallOptions,
	): Promise<Subscription & { readonly result: Result<'git.watch'> }>;
}

export interface GitWorkspace {
	readonly id: string | null;
	readonly root: string;
	subscribe(listener: () => void): () => void;
}

/** Why git cannot be watched, as text (never markup). */
export interface GitProblem {
	/** The daemon's code, when it gave one (GIT_NOT_A_REPOSITORY, GIT_NOT_FOUND...). */
	code?: string;
	text: string;
}

export interface GitView {
	state: GitState;
	/** The reason the watch is in `error`, or `waiting` after a lost connection or a failed status. */
	problem: GitProblem | null;
	/** Null until the first status arrives (and after a refusal). */
	head: Head | null;
	/** The paths that differ from HEAD, sorted by path. */
	entries: readonly Entry[];
	truncated: boolean;
	/** Some submodule could not be read and no entry says which (ADR-0125 §4); absent otherwise. */
	submodulesUnread?: boolean;
}

const EMPTY: GitView = { state: 'idle', problem: null, head: null, entries: [], truncated: false };

export class GitWatch {
	#client: GitClient;
	#ws: GitWorkspace;
	#timers: GitTimers;
	#listeners = new Set<() => void>();
	#offs: (() => void)[] = [];
	#view: GitView = EMPTY;
	#wsId: string | null = null;
	#run: object | null = null;
	#sub: Subscription | null = null;
	#early: EventFrame[] = [];
	#timer: unknown = null;
	#hidden = false;
	#waiting = false;
	#disposed = false;

	constructor(client: GitClient, ws: GitWorkspace, timers: GitTimers = realTimers) {
		this.#client = client;
		this.#ws = ws;
		this.#timers = timers;
		this.#offs.push(ws.subscribe(() => this.#workspaceChanged()));
		this.#workspaceChanged();
	}

	get view(): GitView {
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** The page is hidden or shown: the watch ends while hidden and starts again, with a new status, when shown. */
	setHidden(hidden: boolean): void {
		if (this.#disposed || hidden === this.#hidden) return;
		this.#hidden = hidden;
		if (hidden) {
			this.#run = null;
			this.#waiting = false;
			this.#stop();
			if (this.#ws.id !== null) this.#set({ state: 'paused', problem: null });
		} else if (this.#ws.id !== null) {
			this.#begin();
		}
	}

	/** The connection's state, from the client. */
	onConnection(state: ConnectionState): void {
		if (this.#disposed) return;
		if (state === 'open' && this.#waiting) {
			this.#waiting = false;
			this.#begin();
		} else if ((state === 'unauthorized' || state === 'closed') && this.#run !== null) {
			this.#waiting = false;
			this.#set({ state: 'ended', problem: { text: state === 'unauthorized' ? 'the daemon refused the token' : 'the connection was closed' } });
		}
	}

	/** Tries again after a refusal (the Retry button). */
	retry(): void {
		if (this.#disposed || this.#hidden || this.#ws.id === null) return;
		if (this.#view.state === 'error' || this.#view.state === 'ended') this.#begin();
	}

	dispose(): void {
		this.#disposed = true;
		this.#listeners.clear();
		for (const off of this.#offs) off();
		this.#offs = [];
		this.#run = null;
		this.#stop();
	}

	#workspaceChanged(): void {
		const id = this.#ws.id;
		if (id === this.#wsId) return;
		this.#wsId = id;
		this.#run = null;
		this.#waiting = false;
		this.#stop();
		this.#view = EMPTY;
		this.#changed();
		if (id !== null && !this.#hidden) this.#begin();
	}

	#begin(): void {
		if (this.#disposed || this.#hidden || this.#ws.id === null) return;
		const run = {};
		this.#run = run;
		this.#waiting = false;
		this.#stop();
		this.#set({ state: 'starting', problem: null });
		const handlers: SubscribeHandlers = {
			onEvent: (frame) => {
				if (this.#run !== run) return;
				if (this.#sub === null) this.#early.push(frame);
				else this.#event(frame);
			},
			onEnd: (why) => {
				if (this.#run !== run) return;
				this.#sub = null;
				this.#early = [];
				if (why.kind === 'lost') {
					this.#waiting = true;
					this.#set({ state: 'waiting', problem: { text: 'the connection was lost' } });
				} else {
					this.#set({ state: 'ended', problem: { text: why.message } });
				}
			},
		};
		this.#client.subscribe('git.watch', {}, handlers, { workspace: this.#ws.root.trim() }).then(
			(sub) => {
				if (this.#run !== run) {
					this.#unsubscribe(sub); // replaced, hidden or disposed while the answer was on its way
					return;
				}
				this.#sub = sub;
				const r = sub.result;
				this.#view = { state: 'watching', problem: null, head: r.head, entries: r.entries, truncated: r.truncated, ...(r.submodulesUnread ? { submodulesUnread: true } : {}) };
				this.#changed();
				const early = this.#early;
				this.#early = [];
				for (const f of early) {
					if (this.#run !== run) break;
					this.#event(f);
				}
			},
			(err: unknown) => {
				if (this.#run !== run) return;
				if (err instanceof ConnectionError && err.kind === 'lost') {
					this.#waiting = true;
					this.#set({ state: 'waiting', problem: { text: 'the connection was lost' } });
				} else if (err instanceof ConnectionError) {
					this.#set({ state: 'ended', problem: { text: err.message } });
				} else {
					// GIT_NOT_A_REPOSITORY, GIT_NOT_FOUND and the rest: text, and no retry but the user's.
					const f = failure(err);
					this.#view = { ...EMPTY, state: 'error', problem: { ...(f.code ? { code: f.code } : {}), text: failureText(f) } };
					this.#changed();
				}
			},
		);
	}

	#event(frame: EventFrame): void {
		const ev = frame.event;
		if (ev.type === 'status') {
			const p = ev.payload as EventPayload<'git.watch', 'status'>;
			this.#view = { state: 'watching', problem: null, head: p.head, entries: p.entries, truncated: p.truncated, ...(p.submodulesUnread ? { submodulesUnread: true } : {}) };
			this.#changed();
		} else if (ev.type === 'stopped') {
			const reason = (ev.payload as EventPayload<'git.watch', 'stopped'>).reason;
			this.#stop();
			if (reason === 'workspaceClosed') {
				this.#run = null;
				this.#set({ state: 'ended', problem: { text: 'the workspace was closed' } });
				return;
			}
			// failed: what is shown stays, and the watch starts again once git may be back.
			const run = this.#run;
			this.#set({ state: 'waiting', problem: { text: 'git could not report the status' } });
			this.#timer = this.#timers.setTimeout(() => {
				this.#timer = null;
				if (this.#run === run && !this.#disposed && !this.#hidden) this.#begin();
			}, RESTART_MS);
		}
	}

	/** Ends the subscription held, in the daemon too, and any restart that was waiting. */
	#stop(): void {
		this.#early = [];
		if (this.#timer !== null) this.#timers.clearTimeout(this.#timer);
		this.#timer = null;
		const sub = this.#sub;
		this.#sub = null;
		if (sub) this.#unsubscribe(sub);
	}

	#unsubscribe(sub: Subscription): void {
		sub.stop();
		this.#client.call('events.unsubscribe', { subscriptionId: sub.subscriptionId }).catch(() => {
			// The session may be gone, and then so is the subscription.
		});
	}

	#set(change: Partial<GitView>): void {
		this.#view = { ...this.#view, ...change };
		this.#changed();
	}

	#changed(): void {
		for (const l of [...this.#listeners]) l();
	}
}
