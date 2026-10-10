// The session's one `events.subscribe` for `workspace.lifecycle` and `operation.progress`
// (plan m2b D10, D12). It hands lifecycle events to the workspace model and keeps the
// latest progress of each operation for the mirrored controls, which look it up by
// operation id: an operation of another workspace or client is simply never asked for.
//
// A subscription belongs to the session: after a lost connection it starts again when the
// connection is open, and `events.gap` does the same. Either way the listeners of
// `onResync` are told, because a transition may have been missed and a panel must read
// the daemon's state again instead of trusting its copy.
import type { OperationProgress, Params, Result } from '../api/capabilities.gen';
import {
	ConnectionError,
	type ConnectionState,
	type EventFrame,
	type SubscribeHandlers,
	type Subscription,
} from '../api/client';
import { failure, failureText } from './failure';

/** Working default: how many operations' latest progress the hub keeps. */
export const MAX_PROGRESS = 100;

export interface HubClient {
	subscribe(
		name: 'events.subscribe',
		params: Params<'events.subscribe'>,
		handlers: SubscribeHandlers,
	): Promise<Subscription>;
	call(name: 'events.unsubscribe', params: Params<'events.unsubscribe'>): Promise<Result<'events.unsubscribe'>>;
}

/** A workspace.lifecycle event: the event's type is the new state (workspace-state, §Observability). */
export interface LifecycleEvent {
	/** The workspace id. */
	workspace: string;
	state: string;
}

export type HubState = 'idle' | 'starting' | 'watching' | 'waiting' | 'error' | 'ended';

// The topics are the contract's; the names below are the events' own (internal/workspace,
// internal/capability), which the schema does not type.
const LIFECYCLE = 'workspace.lifecycle';
const PROGRESS = 'operation.progress';

export class EventsHub {
	#client: HubClient;
	#progress = new Map<string, OperationProgress>();
	#listeners = new Set<() => void>();
	#lifecycle = new Set<(e: LifecycleEvent) => void>();
	#resync = new Set<() => void>();
	#run: object | null = null;
	#sub: Subscription | null = null;
	#waiting = false;
	#everWatched = false;
	#state: HubState = 'idle';
	#detail = '';

	constructor(client: HubClient) {
		this.#client = client;
	}

	get status(): { state: HubState; detail: string } {
		return { state: this.#state, detail: this.#detail };
	}

	/** The latest progress the operation reported, if any. */
	progress(operationId: string): OperationProgress | undefined {
		return this.#progress.get(operationId);
	}

	/** Calls `listener` after every progress or status change. */
	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	onLifecycle(fn: (e: LifecycleEvent) => void): () => void {
		this.#lifecycle.add(fn);
		return () => {
			this.#lifecycle.delete(fn);
		};
	}

	/** Called when events may have been missed: a subscription started again, or `events.gap`. */
	onResync(fn: () => void): () => void {
		this.#resync.add(fn);
		return () => {
			this.#resync.delete(fn);
		};
	}

	start(): void {
		this.#begin();
	}

	stop(): void {
		this.#run = null;
		this.#waiting = false;
		this.#release();
		this.#set('ended', 'stopped');
	}

	onConnection(state: ConnectionState): void {
		if (state === 'open' && this.#waiting) {
			this.#waiting = false;
			this.#begin();
		} else if ((state === 'unauthorized' || state === 'closed') && this.#run !== null) {
			this.#waiting = false;
			this.#set('ended', state === 'unauthorized' ? 'the daemon refused the token' : 'the connection was closed');
		}
	}

	#begin(): void {
		const run = {};
		this.#run = run;
		this.#waiting = false;
		this.#release();
		this.#set('starting', '');
		const handlers: SubscribeHandlers = {
			onEvent: (frame) => {
				if (this.#run === run) this.#event(frame);
			},
			onEnd: (why) => {
				if (this.#run !== run) return;
				this.#sub = null;
				if (why.kind === 'lost') {
					this.#waiting = true;
					this.#set('waiting', 'the connection was lost');
				} else {
					this.#set('ended', why.message);
				}
			},
		};
		this.#client.subscribe('events.subscribe', { topics: [LIFECYCLE, PROGRESS] }, handlers).then(
			(sub) => {
				if (this.#run !== run) {
					this.#unsubscribe(sub);
					return;
				}
				this.#sub = sub;
				const again = this.#everWatched;
				this.#everWatched = true;
				this.#set('watching', '');
				if (again) this.#fireResync();
			},
			(err: unknown) => {
				if (this.#run !== run) return;
				if (err instanceof ConnectionError && err.kind === 'lost') {
					this.#waiting = true;
					this.#set('waiting', 'the connection was lost');
				} else {
					this.#set(err instanceof ConnectionError ? 'ended' : 'error', failureText(failure(err)));
				}
			},
		);
	}

	#event(frame: EventFrame): void {
		const ev = frame.event;
		if (ev.topic === 'events.gap') {
			// Events were lost: the panels read the daemon's state again.
			this.#fireResync();
			return;
		}
		if (ev.topic === LIFECYCLE && ev.workspace !== null) {
			for (const fn of [...this.#lifecycle]) fn({ workspace: ev.workspace, state: ev.type });
			return;
		}
		if (ev.topic === PROGRESS && ev.operation !== undefined) {
			this.#progress.delete(ev.operation); // re-inserted last: the map's order is recency
			this.#progress.set(ev.operation, ev.payload as OperationProgress);
			while (this.#progress.size > MAX_PROGRESS) {
				const oldest = this.#progress.keys().next().value;
				if (oldest === undefined) break;
				this.#progress.delete(oldest);
			}
			this.#changed();
		}
	}

	#fireResync(): void {
		for (const fn of [...this.#resync]) fn();
	}

	#set(state: HubState, detail: string): void {
		this.#state = state;
		this.#detail = detail;
		this.#changed();
	}

	#changed(): void {
		for (const l of [...this.#listeners]) l();
	}

	#release(): void {
		const sub = this.#sub;
		this.#sub = null;
		if (sub) this.#unsubscribe(sub);
	}

	#unsubscribe(sub: Subscription): void {
		sub.stop();
		this.#client.call('events.unsubscribe', { subscriptionId: sub.subscriptionId }).catch(() => {
			// The session may be gone; its subscriptions went with it.
		});
	}
}
