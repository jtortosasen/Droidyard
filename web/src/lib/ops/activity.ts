// The activity store: every operation any client runs in the daemon, from
// `operation.watch` (ADR-0043; plan m2b step 2). It is the single source the activity
// feed and the mirrored controls read (D11), and the one place that says "an operation of
// capability X ended", so a panel reloads daemon state on that and never polls (D13).
//
// How it stays right:
//   - the snapshot (operations not yet terminal) and the `state` events are merged by
//     operation id, so an operation in both is one row; a state never moves backwards
//     (queued, running, terminal), so a snapshot taken a moment before an event cannot
//     undo it, and a terminal state is never replaced;
//   - a terminal event for an operation never seen starting makes a terminal row;
//   - `events.gap`, a reconnect and a failed start call `operation.watch` again: the old
//     subscription is ended, terminal rows are kept, the new snapshot is merged, and a
//     non-terminal row that neither the snapshot nor a new event confirms is marked
//     `unseen` (it ended while the store could not see), so no mirrored control stays
//     stuck on it;
//   - it keeps MAX_OPERATIONS rows (plan D16), dropping the oldest terminal ones first.
import { eventTopics, type EventPayload, type Params, type Result } from '../api/capabilities.gen';
import {
	CapabilityError,
	ConnectionError,
	type ConnectionState,
	type EventFrame,
	type SubscribeHandlers,
	type Subscription,
} from '../api/client';

/** Working default (plan D16): how many operations the store keeps. */
export const MAX_OPERATIONS = 500;
/** Delay before `operation.watch` is asked again after the daemon refused it, doubling to the cap. */
export const RETRY_START_MS = 1000;
export const RETRY_MAX_MS = 30_000;

/** `unseen`: ended while the store could not see; the outcome is unknown. */
export type OperationState = 'queued' | 'running' | 'completed' | 'failed' | 'cancelled' | 'unseen';

/** The daemon session that made the call (ADR-0048); absent for a call recorded without one. */
export interface OperationSession {
	readonly id: string;
	readonly name?: string;
	readonly workspace?: string;
}

/** What a call acts on (ADR-0076): the caller's own parameters; absent for a capability that declares none. */
export interface OperationTarget {
	readonly device?: string;
	readonly avd?: string;
	readonly module?: string;
	readonly variant?: string;
}

export interface Operation {
	readonly id: string;
	readonly capability: string;
	/** The workspace id; null for a global operation. */
	readonly workspace: string | null;
	/** cli, mcp or ui; undefined for a call recorded without a kind. */
	readonly client: string | undefined;
	readonly session?: OperationSession;
	readonly target?: OperationTarget;
	readonly state: OperationState;
	readonly startedAt: string;
	readonly durationMs?: number;
	readonly code?: string;
	readonly blockedBy?: { operationId?: string; capability?: string; reason?: string };
}

export function isTerminal(state: OperationState): boolean {
	return state !== 'queued' && state !== 'running';
}

function rank(state: OperationState): number {
	return state === 'queued' ? 0 : state === 'running' ? 1 : 2;
}

type Snapshot = Result<'operation.watch'>;
type StateEvent = EventPayload<'operation.watch', 'state'>;

/** The two calls the store makes; the real `Client` satisfies it through a thin wrapper. */
export interface ActivityClient {
	subscribe(
		name: 'operation.watch',
		params: Params<'operation.watch'>,
		handlers: SubscribeHandlers,
	): Promise<Subscription & { readonly result: Snapshot }>;
	call(name: 'events.unsubscribe', params: Params<'events.unsubscribe'>): Promise<Result<'events.unsubscribe'>>;
}

export interface ActivityTimers {
	setTimeout(fn: () => void, ms: number): unknown;
	clearTimeout(handle: unknown): void;
}

export type WatchState = 'idle' | 'starting' | 'watching' | 'waiting' | 'error' | 'ended';

export interface WatchStatus {
	state: WatchState;
	detail: string;
}

interface Row {
	op: Operation;
	order: number; // first-seen order: breaks ties of startedAt, and picks what to evict
	run: number; // the watch run whose events last touched it
}

interface Run {
	n: number;
}

const TOPIC = eventTopics['operation.watch'];

const realTimers: ActivityTimers = {
	setTimeout: (fn, ms) => setTimeout(fn, ms),
	clearTimeout: (h) => clearTimeout(h as ReturnType<typeof setTimeout>),
};

export class ActivityStore {
	#client: ActivityClient;
	#timers: ActivityTimers;
	#max: number;
	#rows = new Map<string, Row>();
	#order = 0;
	#listeners = new Set<() => void>();
	#triggers = new Set<{ capabilities: ReadonlySet<string>; fn: (op: Operation) => void }>();
	#cache: Operation[] | null = null;

	#status: WatchStatus = { state: 'idle', detail: '' };
	#runs = 0;
	#run: Run | null = null;
	#sub: Subscription | null = null;
	#inflight: Promise<void> | null = null;
	#waiting = false;
	#retry: unknown = null;
	#attempt = 0;

	constructor(client: ActivityClient, opts: { max?: number; timers?: ActivityTimers } = {}) {
		this.#client = client;
		this.#max = opts.max ?? MAX_OPERATIONS;
		this.#timers = opts.timers ?? realTimers;
	}

	// What the panels read.

	/** Every kept operation, newest first. A new array after each change. */
	get operations(): readonly Operation[] {
		if (this.#cache === null) {
			this.#cache = [...this.#rows.values()]
				.sort((a, b) =>
					a.op.startedAt === b.op.startedAt ? b.order - a.order : a.op.startedAt < b.op.startedAt ? 1 : -1,
				)
				.map((r) => r.op);
		}
		return this.#cache;
	}

	get status(): WatchStatus {
		return this.#status;
	}

	/** The operations not yet terminal that satisfy `predicate`: what a mirrored control reads (D11). */
	active(predicate: (op: Operation) => boolean = () => true): Operation[] {
		return this.operations.filter((op) => !isTerminal(op.state) && predicate(op));
	}

	/** Calls `listener` after every change; returns the way to stop. */
	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/**
	 * The D13 trigger: calls `fn` when an operation of one of `capabilities`, run by any
	 * client, reaches a terminal state (or is found `unseen`). Callers must not name the
	 * capabilities their own reload calls: a read is never its own trigger.
	 */
	onTerminal(capabilities: readonly string[], fn: (op: Operation) => void): () => void {
		const t = { capabilities: new Set(capabilities), fn };
		this.#triggers.add(t);
		return () => {
			this.#triggers.delete(t);
		};
	}

	// Life.

	/** Starts watching (once the connection is open, if it is not yet). */
	start(): void {
		this.#begin();
	}

	/** Stops watching for good; the rows stay. */
	stop(): void {
		this.#run = null;
		this.#waiting = false;
		this.#cancelRetry();
		this.#release();
		this.#setStatus('ended', 'stopped');
	}

	/** The connection's state, from the client: a watch that lost its session starts again when it is open. */
	onConnection(state: ConnectionState): void {
		if (state === 'open' && this.#waiting) {
			this.#waiting = false;
			this.#begin();
		} else if ((state === 'unauthorized' || state === 'closed') && this.#run !== null) {
			this.#waiting = false;
			this.#cancelRetry();
			this.#setStatus('ended', state === 'unauthorized' ? 'the daemon refused the token' : 'the connection was closed');
		}
	}

	#begin(): void {
		const run: Run = { n: ++this.#runs };
		this.#run = run;
		this.#waiting = false;
		this.#cancelRetry();
		this.#release();
		this.#setStatus('starting', '');

		const handlers: SubscribeHandlers = {
			onEvent: (frame) => {
				if (this.#run === run) this.#event(run, frame);
			},
			onEnd: (why) => {
				if (this.#run !== run) return;
				this.#sub = null;
				if (why.kind === 'lost') {
					this.#waiting = true;
					this.#setStatus('waiting', 'the connection was lost');
				} else {
					this.#setStatus('ended', why.message);
				}
			},
		};
		const send = (): Promise<void> =>
			this.#client.subscribe('operation.watch', {}, handlers).then(
				(sub) => {
					if (this.#run !== run) {
						this.#unsubscribe(sub); // stopped or replaced while the answer was on its way
						return;
					}
					this.#sub = sub;
					this.#attempt = 0;
					this.#snapshot(run, sub.result);
					this.#setStatus('watching', '');
				},
				(err: unknown) => {
					if (this.#run !== run) return;
					if (err instanceof ConnectionError && err.kind === 'lost') {
						this.#waiting = true;
						this.#setStatus('waiting', 'the connection was lost');
					} else if (err instanceof ConnectionError) {
						this.#setStatus('ended', err.message);
					} else {
						const why =
							err instanceof CapabilityError
								? `${err.code}: ${err.message}`
								: err instanceof Error
									? err.message
									: 'operation.watch failed';
						this.#setStatus('error', why);
						const delay = Math.min(RETRY_START_MS * 2 ** this.#attempt, RETRY_MAX_MS);
						this.#attempt++;
						this.#retry = this.#timers.setTimeout(() => {
							this.#retry = null;
							if (this.#run === run) this.#begin();
						}, delay);
					}
				},
			);
		// One start at a time (as the Logcat stream does): a superseded start is never sent.
		const prev = this.#inflight;
		const p = prev === null ? send() : prev.then(() => (this.#run === run ? send() : undefined));
		this.#inflight = p;
		void p.finally(() => {
			if (this.#inflight === p) this.#inflight = null;
		});
	}

	#event(run: Run, frame: EventFrame): void {
		const ev = frame.event;
		if (ev.topic === 'events.gap' && ev.type === 'gap') {
			// Events were lost: ask again; terminal rows stay, the snapshot corrects the rest.
			this.#begin();
			return;
		}
		if (ev.topic !== TOPIC || ev.type !== 'state' || ev.operation === undefined) return;
		const p = ev.payload as StateEvent;
		this.#merge(
			run,
			{
				id: ev.operation,
				capability: p.capability,
				workspace: ev.workspace,
				client: p.client,
				session: p.session,
				target: p.target,
				state: p.state,
				startedAt: p.startedAt,
				durationMs: p.durationMs,
				code: p.code,
				blockedBy: p.blockedBy,
			},
			true,
		);
		this.#changed();
	}

	#snapshot(run: Run, snap: Snapshot): void {
		const seen = new Set<string>();
		for (const o of snap.operations) {
			seen.add(o.operationId);
			this.#merge(
				run,
				{
					id: o.operationId,
					capability: o.capability,
					workspace: o.workspace ?? null,
					client: o.client,
					session: o.session,
					target: o.target,
					state: o.state,
					startedAt: o.startedAt,
					blockedBy: o.blockedBy,
				},
				false,
			);
		}
		// A row still live that neither this snapshot nor an event of this run confirms
		// ended while the store could not see.
		const ended: Operation[] = [];
		for (const row of this.#rows.values()) {
			if (isTerminal(row.op.state) || seen.has(row.op.id) || row.run === run.n) continue;
			row.op = { ...row.op, state: 'unseen', blockedBy: undefined };
			ended.push(row.op);
		}
		this.#changed();
		for (const op of ended) this.#fire(op);
	}

	/** Merges one observation of an operation; `fromEvent` stamps the row as confirmed by this run. */
	#merge(run: Run, seen: Operation, fromEvent: boolean): void {
		const row = this.#rows.get(seen.id);
		if (row === undefined) {
			this.#rows.set(seen.id, { op: seen, order: this.#order++, run: fromEvent ? run.n : 0 });
			this.#evict();
			if (isTerminal(seen.state)) this.#fire(seen);
			return;
		}
		if (fromEvent) row.run = run.n;
		if (isTerminal(row.op.state)) return; // a terminal state is never replaced
		if (rank(seen.state) < rank(row.op.state)) return; // never backwards
		const wasLive = !isTerminal(row.op.state);
		row.op = { ...row.op, ...seen, blockedBy: seen.blockedBy };
		if (wasLive && isTerminal(seen.state)) this.#fire(row.op);
	}

	#evict(): void {
		while (this.#rows.size > this.#max) {
			let victim: string | undefined;
			for (const [id, r] of this.#rows) {
				if (isTerminal(r.op.state)) {
					victim = id;
					break;
				}
			}
			victim ??= this.#rows.keys().next().value;
			if (victim === undefined) return;
			this.#rows.delete(victim);
		}
	}

	#fire(op: Operation): void {
		for (const t of [...this.#triggers]) {
			if (t.capabilities.has(op.capability)) t.fn(op);
		}
	}

	#changed(): void {
		this.#cache = null;
		for (const l of [...this.#listeners]) l();
	}

	#setStatus(state: WatchState, detail: string): void {
		this.#status = { state, detail };
		this.#changed();
	}

	#cancelRetry(): void {
		if (this.#retry !== null) this.#timers.clearTimeout(this.#retry);
		this.#retry = null;
	}

	/** Ends the current subscription locally and, if it was answered, in the daemon. */
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
