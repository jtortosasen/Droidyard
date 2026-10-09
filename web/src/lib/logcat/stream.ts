// The Logcat stream's life in the client (plan m2a step 5, D4; ADR-0041).
//
// It starts `device.watchLogs`, turns the subscription's events into entries for the
// panel, and keeps the stream honest:
//   - a reconnect is a new session, so the stream starts again with `since` = the last
//     line's time, and the lines at that instant that are already held are dropped (the
//     daemon sends the lines at or after `since`);
//   - `stopped` ends it: the reason is shown, `events.unsubscribe` is sent (ADR-0041 §4:
//     the subscription stays open and empty until then) and nothing restarts it;
//   - `dropped` and `events.gap` become markers, so nothing is lost silently;
//   - a start whose answer arrives after the stream was stopped or restarted is ended at
//     once, and a stopped run's late events are ignored.
// Every device field is passed on as data; this module never builds markup.
import { eventTopics, type EventPayload, type Params, type Result } from '../api/capabilities.gen';
import {
	CapabilityError,
	ConnectionError,
	type ConnectionState,
	type EventFrame,
	type SubscribeHandlers,
	type Subscription,
} from '../api/client';
import type { Entry, StreamLine } from './entry';

/** The two calls the stream makes; the real `Client` satisfies it. */
export interface StreamClient {
	subscribe(
		name: 'device.watchLogs',
		params: Params<'device.watchLogs'>,
		handlers: SubscribeHandlers,
	): Promise<Subscription>;
	call(name: 'events.unsubscribe', params: Params<'events.unsubscribe'>): Promise<Result<'events.unsubscribe'>>;
}

export type StreamState =
	| 'idle' // nothing asked for
	| 'starting' // watchLogs sent, not answered
	| 'streaming'
	| 'waiting' // the connection was lost; the stream starts again when it is back
	| 'stopped' // the daemon ended it (the reason is in the detail)
	| 'error' // the start was refused (the code is in the detail)
	| 'ended'; // the session is over for good

export interface StreamStatus {
	state: StreamState;
	detail: string;
}

export interface StreamSink {
	/** Entries in the order they belong in; `daemonDropped` is the lines the daemon reported it did not deliver. */
	entries(entries: Entry[], daemonDropped: number): void;
	status(status: StreamStatus): void;
}

const TOPIC_LOGCAT = eventTopics['device.watchLogs'];
const TOPIC_GAP = 'events.gap';

type Lines = EventPayload<'device.watchLogs', 'lines'>;
type Stopped = EventPayload<'device.watchLogs', 'stopped'>;

/** One start of the stream; what it learns is its own, so a stale one cannot touch the next. */
interface Run {
	ended: boolean;
}

export class LogStream {
	#client: StreamClient;
	#sink: StreamSink;
	#device = '';
	#applicationId = '';
	#run: Run | null = null;
	#sub: Subscription | null = null;
	// The start whose answer has not been handled yet. The daemon serves a session's
	// requests concurrently and registers a stream only once it runs, so a slow earlier
	// start (an application filter's uid lookup) could register after a fast later one
	// and replace it. A start is therefore sent only once the previous one has settled.
	#inflight: Promise<void> | null = null;
	#waiting = false;

	// The resume point: the newest line's time, and the lines held at exactly that time
	// (a multiset: logcat can print identical lines in one microsecond).
	#lastTime: string | null = null;
	#boundary = new Map<string, number>();
	// While resuming: the boundary lines still to be skipped. Null once a newer line came.
	#skip: Map<string, number> | null = null;

	constructor(client: StreamClient, sink: StreamSink) {
		this.#client = client;
		this.#sink = sink;
	}

	/** Starts a stream of `device` from its newest lines, replacing the running one. */
	open(device: string, applicationId = ''): void {
		this.#device = device;
		this.#applicationId = applicationId;
		this.#lastTime = null;
		this.#boundary.clear();
		this.#begin(false);
	}

	/** Starts the stream again where it left off (after `stopped` or an error). */
	resume(): void {
		if (this.#device !== '') this.#begin(true);
	}

	/** Ends the stream and forgets the device. */
	stop(): void {
		this.#run = null;
		this.#waiting = false;
		this.#device = '';
		this.#release();
		this.#set('idle', '');
	}

	/** The connection's state, from the client: a stream that lost its session starts again when it is open. */
	onConnection(state: ConnectionState): void {
		if (state === 'open' && this.#waiting) {
			this.#waiting = false;
			this.#begin(true);
		} else if ((state === 'unauthorized' || state === 'closed') && this.#device !== '') {
			this.#waiting = false;
			this.#set('ended', state === 'unauthorized' ? 'the daemon refused the token' : 'the connection was closed');
		}
	}

	#begin(resume: boolean): void {
		const run: Run = { ended: false };
		this.#run = run;
		this.#waiting = false;
		this.#release();

		const params: Params<'device.watchLogs'> = { device: this.#device };
		if (this.#applicationId !== '') params.applicationId = this.#applicationId;
		this.#skip = null;
		if (resume && this.#lastTime !== null) {
			params.since = this.#lastTime;
			this.#skip = new Map(this.#boundary);
		}
		this.#set('starting', resume ? 'resuming' : '');

		const handlers: SubscribeHandlers = {
			onEvent: (frame) => {
				if (this.#run === run && !run.ended) this.#event(run, frame);
			},
			onEnd: (why) => {
				if (this.#run !== run || run.ended) return;
				this.#sub = null;
				if (why.kind === 'lost') {
					this.#waiting = true;
					this.#set('waiting', 'the connection was lost');
				} else {
					this.#set('ended', why.message);
				}
			},
		};
		const send = (): Promise<void> => this.#client.subscribe('device.watchLogs', params, handlers).then(
			(sub) => {
				if (this.#run !== run) {
					this.#unsubscribe(sub); // stopped or replaced while the answer was on its way
					return;
				}
				if (run.ended) {
					sub.stop(); // the daemon ended it before the answer was handled
					return;
				}
				this.#sub = sub;
				this.#set('streaming', '');
			},
			(err: unknown) => {
				if (this.#run !== run || run.ended) return;
				if (err instanceof ConnectionError && err.kind === 'lost') {
					this.#waiting = true;
					this.#set('waiting', 'the connection was lost');
				} else if (err instanceof ConnectionError) {
					this.#set('ended', err.message);
				} else if (err instanceof CapabilityError) {
					this.#set('error', `${err.code}: ${err.message}${err.nextStep ? ` ${err.nextStep}` : ''}`);
				} else {
					this.#set('error', err instanceof Error ? err.message : 'the stream could not be started');
				}
			},
		);
		const prev = this.#inflight;
		// A start superseded while it waited is never sent.
		const p = prev === null ? send() : prev.then(() => (this.#run === run ? send() : undefined));
		this.#inflight = p;
		void p.finally(() => {
			if (this.#inflight === p) this.#inflight = null;
		});
	}

	#event(run: Run, frame: EventFrame): void {
		const ev = frame.event;
		if (ev.topic === TOPIC_LOGCAT && ev.type === 'lines') {
			this.#lines(ev.payload as Lines);
		} else if (ev.topic === TOPIC_LOGCAT && ev.type === 'stopped') {
			this.#stopped(run, frame.subscriptionId, ev.payload as Stopped);
		} else if (ev.topic === TOPIC_GAP && ev.type === 'gap') {
			const p = ev.payload as { fromSeq?: unknown; toSeq?: unknown } | null;
			const from = Number(p?.fromSeq);
			const to = Number(p?.toSeq);
			const n = Number.isFinite(from) && Number.isFinite(to) && to >= from ? to - from + 1 : 0;
			this.#sink.entries(
				[
					{
						marker: 'gap',
						text:
							n > 0
								? `the connection could not keep up: ${n} event${n === 1 ? '' : 's'} were skipped, with the lines in them`
								: 'the connection could not keep up: some events were skipped, with the lines in them',
					},
				],
				0,
			);
		}
	}

	#lines(p: Lines): void {
		if (!Array.isArray(p.lines)) return;
		const out: Entry[] = [];
		const dropped = typeof p.dropped === 'number' && p.dropped > 0 ? p.dropped : 0;
		if (dropped > 0) {
			out.push({
				marker: 'dropped',
				text: `${dropped} line${dropped === 1 ? '' : 's'} were not delivered: the daemon could not keep up with the device`,
			});
		}
		for (const line of p.lines) {
			if (this.#accept(line)) out.push(line);
		}
		if (out.length > 0 || dropped > 0) this.#sink.entries(out, dropped);
	}

	/** Dedups the resume boundary and tracks it; true when the line is new. */
	#accept(line: StreamLine): boolean {
		const key = lineKey(line);
		const last = this.#lastTime;
		if (this.#skip !== null && last !== null) {
			// Times are fixed-width RFC 3339 UTC, so they order as strings.
			if (line.time < last) return false;
			if (line.time === last) {
				const left = this.#skip.get(key) ?? 0;
				if (left > 0) {
					this.#skip.set(key, left - 1);
					return false;
				}
			} else {
				this.#skip = null; // past the boundary: every line from here is new
			}
		}
		if (last === null || line.time > last) {
			this.#lastTime = line.time;
			this.#boundary = new Map();
		}
		if (line.time === this.#lastTime) this.#boundary.set(key, (this.#boundary.get(key) ?? 0) + 1);
		return true;
	}

	#stopped(run: Run, subscriptionId: string, p: Stopped): void {
		run.ended = true;
		const why =
			p.reason === 'deviceGone'
				? 'the device went away'
				: p.reason === 'replaced'
					? 'another stream of this device replaced this one'
					: 'logcat ended';
		const detail = p.cause ? `${why} (${p.cause})` : why;
		this.#sink.entries([{ marker: 'stopped', text: `the stream stopped: ${detail}` }], 0);
		this.#set('stopped', detail);
		// ADR-0041 §4: the subscription stays open, empty, until it is unsubscribed.
		this.#sub?.stop();
		this.#sub = null;
		this.#send(subscriptionId);
	}

	/** Stops delivering the current subscription and ends it in the daemon. */
	#release(): void {
		const sub = this.#sub;
		this.#sub = null;
		if (sub) this.#unsubscribe(sub);
	}

	#unsubscribe(sub: Subscription): void {
		sub.stop();
		this.#send(sub.subscriptionId);
	}

	#send(subscriptionId: string): void {
		this.#client.call('events.unsubscribe', { subscriptionId }).catch(() => {
			// The session may be gone, and then so is the subscription.
		});
	}

	#set(state: StreamState, detail: string): void {
		this.#sink.status({ state, detail });
	}
}

/** What makes two lines the same line: logcat can print identical text at one instant. */
function lineKey(l: StreamLine): string {
	return `${l.pid}\u0000${l.tid}\u0000${l.priority}\u0000${l.tag}\u0000${l.message}`;
}
