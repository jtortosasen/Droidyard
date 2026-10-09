// The WebSocket client: one session per tab carries every call (plan m2a D4;
// http-websocket.md §GET /v1/ws). POST /v1/call is not used.
//
// The protocol it speaks: the first frame is `{"auth": "<token>"}`; then request
// envelopes `{id, capability, workspace?, params?, waitMs?}`; back come response
// envelopes matched by `id` and `{subscriptionId, event}` frames. A close with code 4401
// means the daemon refused the token (it is per launch: a restarted daemon has a new
// one), so the client stops and the UI shows "run `droidyard ui`". Any other close is
// retried with a backoff. Subscriptions belong to the session: a reconnect starts a new
// one, and whoever held a subscription starts it again (with `since`, for a stream).
import {
	capabilityInfo,
	type CapabilityName,
	type Params,
	type Result,
} from './capabilities.gen';

/** The WebSocket close code of a refused token (adapters/httpapi/ws.go). */
export const CLOSE_UNAUTHORIZED = 4401;

export type ConnectionState = 'connecting' | 'open' | 'reconnecting' | 'unauthorized' | 'closed';

/** A capability's typed error (capability.Error): `code` is the only field to branch on. */
export class CapabilityError extends Error {
	readonly code: string;
	readonly details?: Record<string, unknown>;
	readonly causeText?: string;
	readonly nextStep?: string;
	/** meta.untrusted: `details` may quote device text and must be shown as inert text. */
	readonly untrusted: boolean;

	constructor(e: WireError, untrusted: boolean) {
		super(e.message);
		this.name = 'CapabilityError';
		this.code = e.code;
		this.details = e.details;
		this.causeText = e.cause;
		this.nextStep = e.nextStep;
		this.untrusted = untrusted;
	}
}

/** The session ended before the call was answered. The operation, if one started, goes on in the daemon. */
export class ConnectionError extends Error {
	readonly kind: 'lost' | 'unauthorized' | 'closed';
	/** The long-running operation that was already started when the session ended, if any. */
	readonly operationId?: string;

	constructor(kind: 'lost' | 'unauthorized' | 'closed', operationId?: string) {
		super(
			kind === 'unauthorized'
				? 'the daemon refused the token'
				: kind === 'closed'
					? 'the client was closed'
					: 'the connection was lost',
		);
		this.name = 'ConnectionError';
		this.kind = kind;
		this.operationId = operationId;
	}
}

interface WireError {
	code: string;
	message: string;
	cause?: string;
	nextStep?: string;
	details?: Record<string, unknown>;
}

interface ResponseEnvelope {
	id: string;
	operation?: { id: string; state: string };
	result?: unknown;
	error?: WireError;
	meta?: { untrusted?: boolean };
}

/** An event as the bus encodes it (internal/event.Event). The payload's shape is per topic and type. */
export interface BusEvent {
	epoch: string;
	seq: number;
	time: string;
	workspace: string | null;
	operation?: string;
	topic: string;
	type: string;
	payload: unknown;
}

export interface EventFrame {
	subscriptionId: string;
	event: BusEvent;
}

/** The slice of the browser's WebSocket the client uses; a test passes a fake. */
export interface SocketLike {
	send(data: string): void;
	close(code?: number, reason?: string): void;
	onopen: ((ev: Event) => void) | null;
	onmessage: ((ev: MessageEvent) => void) | null;
	onclose: ((ev: CloseEvent) => void) | null;
}

export interface Timers {
	setTimeout(fn: () => void, ms: number): unknown;
	clearTimeout(handle: unknown): void;
}

export interface ClientOptions {
	url: string;
	token: string;
	createSocket: (url: string) => SocketLike;
	timers?: Timers;
	/** Called once with the state after every change. */
	onState?: (state: ConnectionState) => void;
}

export interface CallOptions {
	/** The workspace root, for a workspace-scoped capability (an envelope field, never a parameter). */
	workspace?: string;
}

export interface SubscribeHandlers {
	onEvent: (frame: EventFrame) => void;
	/** The session ended, so the subscription did; start it again when the client is open. */
	onEnd?: (why: ConnectionError) => void;
}

export interface Subscription {
	readonly subscriptionId: string;
	/** Stops delivering this subscription's events to this client. It does not end it in the daemon. */
	stop(): void;
}

export const BACKOFF_START_MS = 250;
export const BACKOFF_MAX_MS = 5000;
/** How long one operation.wait blocks before the client asks again. */
const WAIT_SLICE_MS = 30_000;

const realTimers: Timers = {
	setTimeout: (fn, ms) => setTimeout(fn, ms),
	clearTimeout: (h) => clearTimeout(h as ReturnType<typeof setTimeout>),
};

interface Pending {
	frame: string;
	resolve: (env: ResponseEnvelope) => void;
	reject: (e: ConnectionError) => void;
	/** Runs on the response, before the call's promise settles. */
	hook?: (env: ResponseEnvelope) => void;
	sent: boolean;
}

export class Client {
	#opts: ClientOptions;
	#timers: Timers;
	#socket: SocketLike | null = null;
	#state: ConnectionState = 'closed';
	#nextId = 1;
	#attempt = 0;
	#retry: unknown = null;
	#pending = new Map<string, Pending>();
	#subscriptions = new Map<string, SubscribeHandlers>();

	constructor(opts: ClientOptions) {
		this.#opts = opts;
		this.#timers = opts.timers ?? realTimers;
	}

	get state(): ConnectionState {
		return this.#state;
	}

	/** Opens the session. Calls made before it is open wait for it. A refused token is never retried. */
	connect(): void {
		if (this.#state !== 'closed') return;
		this.#attempt = 0;
		this.#open('connecting');
	}

	/** Ends the session for good: pending calls and subscriptions end with `closed`. */
	close(): void {
		this.#timers.clearTimeout(this.#retry);
		this.#retry = null;
		const s = this.#socket;
		this.#socket = null;
		if (s) {
			s.onopen = s.onmessage = s.onclose = null;
			s.close(1000);
		}
		this.#setState('closed');
		this.#end(new ConnectionError('closed'));
	}

	/**
	 * Calls a capability. A long-running one is sent with `waitMs: 0` and followed with
	 * `operation.wait` until it ends, so a slow build never holds the session's single
	 * request open; any other is sent without `waitMs`. Resolves to the result, or rejects
	 * with the capability's {@link CapabilityError} or a {@link ConnectionError}.
	 */
	async call<N extends CapabilityName>(
		name: N,
		params: Params<N>,
		opts: CallOptions = {},
	): Promise<Result<N>> {
		return (await this.#run(name, params, opts, undefined)) as Result<N>;
	}

	/**
	 * Calls a capability that opens a subscription and registers `handlers` for it before
	 * the answer is delivered, so no event can arrive ahead of its handler. The session
	 * writes a subscription's first frame only after the answer carrying its id.
	 */
	async subscribe<N extends CapabilityName>(
		name: N,
		params: Params<N>,
		handlers: SubscribeHandlers,
		opts: CallOptions = {},
	): Promise<Subscription & { readonly result: Result<N> }> {
		let id = '';
		const result = await this.#run(name, params, opts, (env) => {
			const sid = (env.result as { subscriptionId?: unknown } | undefined)?.subscriptionId;
			if (typeof sid === 'string' && sid !== '') {
				id = sid;
				this.#subscriptions.set(sid, handlers);
			}
		});
		if (id === '') throw new Error(`${name} answered with no subscriptionId`);
		return {
			subscriptionId: id,
			result: result as Result<N>,
			stop: () => {
				this.#subscriptions.delete(id);
			},
		};
	}

	async #run(
		name: CapabilityName,
		params: unknown,
		opts: CallOptions,
		hook: ((env: ResponseEnvelope) => void) | undefined,
	): Promise<unknown> {
		const longRunning = capabilityInfo[name].longRunning;
		// The id of the operation once the first answer gives it: a follow cut by a lost
		// connection asks again for the same operation on the next socket (plan m2b step 2);
		// a call lost before its first answer is not retried (the activity feed shows it if
		// it started).
		let operationId: string | undefined;
		let env: ResponseEnvelope;
		for (;;) {
			try {
				env =
					operationId === undefined
						? await this.#request(name, params, opts.workspace, longRunning ? 0 : undefined, hook)
						: await this.#request(
								'operation.wait',
								{ operationId, timeoutMs: WAIT_SLICE_MS },
								undefined,
								undefined,
								hook,
							);
			} catch (e) {
				if (operationId !== undefined && e instanceof ConnectionError) {
					if (e.kind === 'lost') continue;
					throw new ConnectionError(e.kind, operationId);
				}
				throw e;
			}
			if (longRunning && env.operation) operationId = env.operation.id;
			if (!(longRunning && env.operation && !env.error && isLive(env.operation.state))) break;
		}
		if (env.error) throw new CapabilityError(env.error, env.meta?.untrusted === true);
		return env.result;
	}

	#request(
		name: string,
		params: unknown,
		workspace: string | undefined,
		waitMs: number | undefined,
		hook: ((env: ResponseEnvelope) => void) | undefined,
	): Promise<ResponseEnvelope> {
		if (this.#state === 'unauthorized') return Promise.reject(new ConnectionError('unauthorized'));
		if (this.#state === 'closed') return Promise.reject(new ConnectionError('closed'));
		const id = `r${this.#nextId++}`;
		const frame = JSON.stringify({ id, capability: name, workspace, params, waitMs });
		return new Promise((resolve, reject) => {
			const p: Pending = { frame, resolve, reject, hook, sent: false };
			this.#pending.set(id, p);
			if (this.#state === 'open') this.#send(p);
		});
	}

	#send(p: Pending): void {
		p.sent = true;
		this.#socket?.send(p.frame);
	}

	#open(state: 'connecting' | 'reconnecting'): void {
		this.#setState(state);
		const s = this.#opts.createSocket(this.#opts.url);
		this.#socket = s;
		s.onopen = () => {
			if (this.#socket !== s) return;
			// The auth frame is the first frame, before any request (rule 4).
			s.send(JSON.stringify({ auth: this.#opts.token }));
			this.#setState('open');
			for (const p of this.#pending.values()) if (!p.sent) this.#send(p);
		};
		s.onmessage = (ev) => {
			if (this.#socket === s) this.#receive(ev.data);
		};
		s.onclose = (ev) => {
			if (this.#socket !== s) return;
			this.#socket = null;
			if (ev.code === CLOSE_UNAUTHORIZED) {
				this.#setState('unauthorized');
				this.#end(new ConnectionError('unauthorized'));
				return;
			}
			// The state first: a call made from an onEnd handler is then queued for the
			// next socket rather than sent on the closed one.
			this.#setState('reconnecting');
			this.#end(new ConnectionError('lost'));
			const delay = Math.min(BACKOFF_START_MS * 2 ** this.#attempt, BACKOFF_MAX_MS);
			this.#attempt++;
			this.#retry = this.#timers.setTimeout(() => {
				this.#retry = null;
				this.#open('reconnecting');
			}, delay);
		};
	}

	#receive(data: unknown): void {
		if (typeof data !== 'string') return;
		let msg: unknown;
		try {
			msg = JSON.parse(data);
		} catch {
			return;
		}
		if (typeof msg !== 'object' || msg === null) return;
		// A frame from the daemon proves the token was accepted: the backoff starts over.
		this.#attempt = 0;
		const m = msg as Partial<EventFrame> & Partial<ResponseEnvelope>;
		if (typeof m.subscriptionId === 'string' && m.event) {
			this.#subscriptions.get(m.subscriptionId)?.onEvent({ subscriptionId: m.subscriptionId, event: m.event });
			return;
		}
		if (typeof m.id !== 'string') return;
		const p = this.#pending.get(m.id);
		if (!p) return;
		this.#pending.delete(m.id);
		const env = m as ResponseEnvelope;
		p.hook?.(env);
		p.resolve(env);
	}

	/** The session is over: every call it held and every subscription ends. */
	#end(why: ConnectionError): void {
		// A lost connection ends the calls it carried; a call made while it was down was
		// never sent and goes out on the next one. Refusal and `close()` end everything.
		for (const [id, p] of [...this.#pending]) {
			if (why.kind !== 'lost' || p.sent) {
				this.#pending.delete(id);
				p.reject(why);
			}
		}
		const subs = [...this.#subscriptions.values()];
		this.#subscriptions.clear();
		for (const h of subs) h.onEnd?.(why);
	}

	#setState(s: ConnectionState): void {
		if (this.#state === s) return;
		this.#state = s;
		this.#opts.onState?.(s);
	}
}

function isLive(state: string): boolean {
	return state === 'queued' || state === 'running';
}
