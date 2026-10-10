// The root watch behind the file tree (plan m2d step 5; ADR-0049, D26). One `file.watch`
// of the project root, called without `waitMs`: its answer carries the baseline walk, and
// `file.changes` events then carry the differences, so the baseline plus the events equal
// the tree. This class owns the subscription and nothing about the tree: what it learns
// goes to a sink.
//
// When the watch starts again (a fresh baseline replaces the old one):
//   - after a lost connection, once the session is open;
//   - on a `stopped` event (except a closed workspace);
//   - on a `truncated` event while the baseline was complete: the daemon cut the changes
//     at 1,000, so the events no longer add up to the tree;
//   - when the tab becomes visible again (D26: the watch ends while the page is hidden).
// Every start ends the previous subscription with `events.unsubscribe` first.
//
// With a baseline that is not complete (the walk hit its cap) every event says `truncated`,
// so that flag tells nothing. Its changes are applied as they are, and only an event that
// holds CUT changes (the daemon's cut, internal/capability/file/watch.go) makes the sink
// re-list: a smaller event is complete.
//
// Changes are handed to the sink at most once per animation frame, in order.
import { ConnectionError, type CallOptions, type ConnectionState, type EventFrame, type SubscribeHandlers, type Subscription } from '../api/client';
import type { EventPayload, Params, Result } from '../api/capabilities.gen';
import { browserFrames, type FrameScheduler } from '../logcat/batcher';
import { failure, failureText } from '../build/failure';

/** The daemon's cut of one tick's changes (file/watch.go). */
export const CUT = 1000;

export type Baseline = Result<'file.watch'>['entries'];
export type Change = EventPayload<'file.watch', 'changes'>['changes'][number];

export type WatchState = 'idle' | 'starting' | 'watching' | 'waiting' | 'paused' | 'error' | 'ended';

export interface WatchSink {
	/** The workspace changed (or went away): forget everything. */
	reset(): void;
	/** A baseline walk; `complete` is false when it hit its cap. It replaces any earlier one. */
	baseline(entries: Baseline, complete: boolean): void;
	/** A frame's changes, in order. */
	changes(list: Change[]): void;
	/** An incomplete baseline's event was cut: re-list what is shown. */
	cut(): void;
	status(state: WatchState, detail: string): void;
}

export interface WatchClient {
	call(name: 'events.unsubscribe', params: Params<'events.unsubscribe'>, opts?: CallOptions): Promise<Result<'events.unsubscribe'>>;
	subscribe(
		name: 'file.watch',
		params: Params<'file.watch'>,
		handlers: SubscribeHandlers,
		opts?: CallOptions,
	): Promise<Subscription & { readonly result: Result<'file.watch'> }>;
}

export interface WatchWorkspace {
	readonly id: string | null;
	readonly root: string;
	subscribe(listener: () => void): () => void;
}

export class FileWatch {
	#client: WatchClient;
	#ws: WatchWorkspace;
	#sink: WatchSink;
	#frames: FrameScheduler;
	#offs: (() => void)[] = [];
	#wsId: string | null = null;
	#run: object | null = null;
	#sub: Subscription | null = null;
	#complete = true;
	#hidden = false;
	#waiting = false;
	#disposed = false;
	#early: EventFrame[] = [];
	#queue: Change[] = [];
	#cut = false;
	#frame: unknown = null;

	constructor(client: WatchClient, ws: WatchWorkspace, sink: WatchSink, frames: FrameScheduler = browserFrames) {
		this.#client = client;
		this.#ws = ws;
		this.#sink = sink;
		this.#frames = frames;
		this.#offs.push(ws.subscribe(() => this.#workspaceChanged()));
		this.#workspaceChanged();
	}

	/** The page is hidden or shown: the watch ends while hidden and starts again, with a new baseline, when shown. */
	setHidden(hidden: boolean): void {
		if (this.#disposed || hidden === this.#hidden) return;
		this.#hidden = hidden;
		if (hidden) {
			this.#run = null;
			this.#waiting = false;
			this.#stop();
			if (this.#ws.id !== null) this.#sink.status('paused', '');
		} else if (this.#ws.id !== null && !this.#waiting) {
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
			this.#sink.status('ended', state === 'unauthorized' ? 'the daemon refused the token' : 'the connection was closed');
		}
	}

	dispose(): void {
		this.#disposed = true;
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
		this.#sink.reset();
		this.#sink.status('idle', '');
		if (id !== null && !this.#hidden) this.#begin();
	}

	#begin(): void {
		if (this.#disposed || this.#hidden || this.#ws.id === null) return;
		const run = {};
		this.#run = run;
		this.#waiting = false;
		this.#stop();
		this.#sink.status('starting', '');
		const handlers: SubscribeHandlers = {
			onEvent: (frame) => {
				if (this.#run !== run) return;
				if (this.#sub === null) this.#early.push(frame);
				else this.#event(frame);
			},
			onEnd: (why) => {
				if (this.#run !== run) return;
				this.#sub = null;
				this.#clearQueue();
				if (why.kind === 'lost') {
					this.#waiting = true;
					this.#sink.status('waiting', 'the connection was lost');
				} else {
					this.#sink.status('ended', why.message);
				}
			},
		};
		this.#client.subscribe('file.watch', {}, handlers, { workspace: this.#ws.root.trim() }).then(
			(sub) => {
				if (this.#run !== run) {
					this.#unsubscribe(sub); // replaced, hidden or disposed while the answer was on its way
					return;
				}
				this.#sub = sub;
				this.#complete = sub.result.truncated !== true;
				this.#sink.status('watching', '');
				this.#sink.baseline(sub.result.entries ?? [], this.#complete);
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
					this.#sink.status('waiting', 'the connection was lost');
				} else if (err instanceof ConnectionError) {
					this.#sink.status('ended', err.message);
				} else {
					this.#sink.status('error', failureText(failure(err)));
				}
			},
		);
	}

	#event(frame: EventFrame): void {
		const ev = frame.event;
		if (ev.type === 'stopped') {
			const reason = (ev.payload as EventPayload<'file.watch', 'stopped'>).reason;
			if (reason === 'workspaceClosed') {
				this.#run = null;
				this.#stop();
				this.#sink.status('ended', 'the workspace was closed');
			} else {
				this.#begin();
			}
			return;
		}
		if (ev.type !== 'changes') return;
		const p = ev.payload as EventPayload<'file.watch', 'changes'>;
		if (this.#complete && p.truncated) {
			this.#begin();
			return;
		}
		for (const c of p.changes) this.#queue.push(c);
		if (p.changes.length >= CUT) this.#cut = true;
		if (this.#frame === null) {
			this.#frame = this.#frames.request(() => {
				this.#frame = null;
				this.#flush();
			});
		}
	}

	#flush(): void {
		const list = this.#queue;
		const cut = this.#cut;
		this.#queue = [];
		this.#cut = false;
		if (list.length > 0) this.#sink.changes(list);
		if (cut) this.#sink.cut();
	}

	#clearQueue(): void {
		this.#queue = [];
		this.#cut = false;
		this.#early = [];
		if (this.#frame !== null) this.#frames.cancel(this.#frame);
		this.#frame = null;
	}

	/** Ends the subscription held, in the daemon too. */
	#stop(): void {
		this.#clearQueue();
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
}
