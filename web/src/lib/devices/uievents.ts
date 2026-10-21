// The UI events panel's model (plan m2w step 7; ADR-0112 Decision 4): the screen's events of the
// toolbar's device, live, from `device.watchUiEvents`. A pure state machine with no markup and no
// socket, so a test drives it with a fake client.
//
//   idle      nothing is watched (the panel is hidden, or no device is selected)
//   watching  `device.watchUiEvents` sent or answered
//   stopped   the stream ended; the reason is shown with Restart
//
// It watches the toolbar's device only while the panel is shown (its region open and the page
// visible), and unsubscribes otherwise: a hidden panel costs the daemon nothing. Rows are newest
// first, at most MAX_ROWS; a gap (events the daemon says were lost, or the ones this panel was not
// watching for) is a row of its own, in its place in time. Every device field leaves as a string
// for a text node (ADR-0015 §1); the model never builds markup.
import type { EventPayload, Params, Result } from '../api/capabilities.gen';
import { eventTopics } from '../api/capabilities.gen';
import {
	ConnectionError,
	type ConnectionState,
	type SubscribeHandlers,
	type Subscription,
} from '../api/client';
import { failure, failureText } from '../build/failure';

export interface UiEventsClient {
	subscribe(
		name: 'device.watchUiEvents',
		params: Params<'device.watchUiEvents'>,
		handlers: SubscribeHandlers,
	): Promise<Subscription & { readonly result: Result<'device.watchUiEvents'> }>;
	call(name: 'events.unsubscribe', params: Params<'events.unsubscribe'>): Promise<Result<'events.unsubscribe'>>;
}

export interface UiEventsDevices {
	view: { selected: string };
	subscribe(listener: () => void): () => void;
}

export type UiEventsState = 'idle' | 'watching' | 'stopped';
export type StopReason = 'deviceGone' | 'replaced' | 'failed';
/** The daemon's gap reasons, and `interrupted`: this panel was not watching (the connection was lost, or the stream was restarted). */
export type GapReason = 'readerStarted' | 'readerRestarted' | 'overflow' | 'interrupted';

/** The rows kept: a working default, not a setting. */
export const MAX_ROWS = 500;

export interface EventRow {
	kind: 'event';
	/** Unique and increasing: the key of the row. */
	id: number;
	/** `hh:mm:ss.mmm`, the device clock's time of the event. */
	clock: string;
	type: string;
	package: string;
	class: string;
	/** The accessibility window id as text; '' when the event names none. */
	window: string;
	/** How many raw events were folded into this one; 1 for none. */
	count: number;
	/** A toast's text; '' for every other event. */
	text: string;
}

export interface GapRow {
	kind: 'gap';
	id: number;
	reason: GapReason;
	/** With `overflow`: how many events were lost; 0 when unknown. */
	dropped: number;
}

export type UiEventRow = EventRow | GapRow;

export interface UiEventsView {
	state: UiEventsState;
	/** Set while `stopped`. */
	reason: StopReason | null;
	/** The words for the stop (the daemon's, or ours); '' for none. */
	cause: string;
	device: string;
	/** Newest first. */
	rows: readonly UiEventRow[];
}

const TOPIC = eventTopics['device.watchUiEvents'];

type Push = EventPayload<'device.watchUiEvents', 'events'>;

interface Run {
	ended: boolean;
}

/** `hh:mm:ss.mmm` of an RFC 3339 time as the daemon writes it (`2026-10-21T01:02:03.456Z`); the text itself when it is not that. */
export function clockOf(time: string): string {
	const m = /^\d{4}-\d{2}-\d{2}T(\d{2}:\d{2}:\d{2}(?:\.\d{1,3})?)/.exec(time);
	return m?.[1] ?? time;
}

const GAPS: readonly GapReason[] = ['readerStarted', 'readerRestarted', 'overflow'];
const STOPS: readonly StopReason[] = ['deviceGone', 'replaced', 'failed'];

export class UiEventsModel {
	#client: UiEventsClient;
	#devices: UiEventsDevices;
	#listeners = new Set<() => void>();
	#view: UiEventsView | null = null;
	#off: () => void;

	#active = false;
	#state: UiEventsState = 'idle';
	#reason: StopReason | null = null;
	#cause = '';
	#device = ''; // the device the current watch (or stop) is for
	#rows: UiEventRow[] = [];
	#nextId = 1;
	#run: Run | null = null;
	#sub: Subscription | null = null;
	#waiting = false; // the connection was lost; the watch starts again when it is back

	constructor(client: UiEventsClient, devices: UiEventsDevices) {
		this.#client = client;
		this.#devices = devices;
		this.#off = devices.subscribe(() => this.#sync());
	}

	get view(): UiEventsView {
		this.#view ??= {
			state: this.#state,
			reason: this.#reason,
			cause: this.#cause,
			device: this.#device,
			rows: this.#rows,
		};
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** The panel is shown (its region open, the page visible) or not. */
	setActive(active: boolean): void {
		if (active === this.#active) return;
		this.#active = active;
		this.#sync();
	}

	/** After a stop: watches the toolbar's device again. What happened meanwhile is marked as a gap. */
	restart(): void {
		if (this.#state !== 'stopped') return;
		this.#state = 'idle';
		this.#reason = null;
		this.#cause = '';
		this.#sync(true);
	}

	/** Empties the list. */
	clear(): void {
		this.#rows = [];
		this.#changed();
	}

	onConnection(state: ConnectionState): void {
		if (state === 'open' && this.#waiting) {
			this.#waiting = false;
			this.#begin(this.#device, true);
		} else if ((state === 'unauthorized' || state === 'closed') && this.#state === 'watching') {
			this.#release();
			this.#stop('failed', state === 'unauthorized' ? 'the daemon refused the token' : 'the connection was closed');
		}
	}

	dispose(): void {
		this.#off();
		this.#release();
		this.#listeners.clear();
	}

	/** Brings the watch in line with what is wanted: the toolbar's device while the panel is shown. */
	#sync(marked = false): void {
		const want = this.#active ? this.#devices.view.selected : '';
		if (want === '') {
			if (this.#state !== 'idle') {
				this.#release();
				this.#waiting = false;
				this.#state = 'idle';
				this.#reason = null;
				this.#cause = '';
				this.#changed();
			}
			return;
		}
		if (want === this.#device && this.#state !== 'idle') return; // watching it, or stopped on it
		// Another device is another list; the same device after a hide is the same list, with a gap.
		const sameDevice = want === this.#device;
		this.#release();
		this.#begin(want, marked || (sameDevice && this.#rows.length > 0));
	}

	#begin(device: string, interrupted: boolean): void {
		const run: Run = { ended: false };
		this.#run = run;
		this.#waiting = false;
		if (device !== this.#device) this.#rows = [];
		const top = this.#rows[0];
		if (interrupted && top !== undefined && !(top.kind === 'gap' && top.reason === 'interrupted')) {
			const gap: GapRow = { kind: 'gap', id: this.#nextId++, reason: 'interrupted', dropped: 0 };
			this.#rows = [gap, ...this.#rows].slice(0, MAX_ROWS);
		}
		this.#device = device;
		this.#reason = null;
		this.#cause = '';
		this.#state = 'watching';
		this.#changed();

		const handlers: SubscribeHandlers = {
			onEvent: (frame) => {
				const ev = frame.event;
				if (this.#run !== run || run.ended || ev.topic !== TOPIC) return;
				if (ev.type === 'events') this.#events(ev.payload as Partial<Push>);
				else if (ev.type === 'stopped') this.#stopped(run, frame.subscriptionId, ev.payload as { reason?: unknown; cause?: unknown });
			},
			onEnd: (why) => {
				if (this.#run !== run || run.ended) return;
				this.#sub = null;
				if (why.kind === 'lost') {
					// The session is gone, so is its subscription: watch again when the connection is back.
					this.#waiting = true;
					this.#changed();
				} else {
					this.#release();
					this.#stop('failed', why.message);
				}
			},
		};
		this.#client.subscribe('device.watchUiEvents', { device }, handlers).then(
			(sub) => {
				if (this.#run !== run) {
					this.#unsubscribe(sub); // replaced or hidden while the answer was on its way
					return;
				}
				if (run.ended) {
					sub.stop();
					return;
				}
				this.#sub = sub;
			},
			(err: unknown) => {
				if (this.#run !== run || run.ended) return;
				if (err instanceof ConnectionError && err.kind === 'lost') {
					this.#waiting = true;
					this.#changed();
				} else {
					this.#release();
					this.#stop('failed', failureText(failure(err)));
				}
			},
		);
	}

	/** One `events` event: its gap first, then its events, oldest first as the daemon sends them. */
	#events(p: Partial<Push>): void {
		const added: UiEventRow[] = [];
		if (p.gap && GAPS.includes(p.gap.reason)) {
			added.push({ kind: 'gap', id: this.#nextId++, reason: p.gap.reason, dropped: typeof p.gap.dropped === 'number' ? p.gap.dropped : 0 });
		}
		for (const e of Array.isArray(p.events) ? p.events : []) {
			added.push({
				kind: 'event',
				id: this.#nextId++,
				clock: clockOf(String(e.time)),
				type: String(e.type),
				package: typeof e.package === 'string' ? e.package : '',
				class: typeof e.class === 'string' ? e.class : '',
				window: typeof e.window === 'number' ? String(e.window) : '',
				count: typeof e.count === 'number' && e.count > 1 ? e.count : 1,
				text: typeof e.text === 'string' ? e.text : '',
			});
		}
		if (added.length === 0) return;
		// Newest first, within MAX_ROWS. The list is never changed in place: a view handed out keeps its rows.
		this.#rows = [...added.reverse(), ...this.#rows].slice(0, MAX_ROWS);
		this.#changed();
	}

	#stopped(run: Run, subscriptionId: string, p: { reason?: unknown; cause?: unknown }): void {
		run.ended = true;
		// ADR-0041 §4: the subscription stays open, empty, until it is unsubscribed.
		this.#sub?.stop();
		this.#sub = null;
		this.#send(subscriptionId);
		this.#run = null;
		const reason = STOPS.find((r) => r === p.reason) ?? 'failed';
		this.#stop(reason, typeof p.cause === 'string' ? p.cause : '');
	}

	#stop(reason: StopReason, cause: string): void {
		this.#state = 'stopped';
		this.#reason = reason;
		this.#cause = cause;
		this.#waiting = false;
		this.#changed();
	}

	/** Ends the current watch in the daemon and ignores whatever it still says. */
	#release(): void {
		if (this.#run) this.#run.ended = true;
		this.#run = null;
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

	#changed(): void {
		this.#view = null;
		for (const l of [...this.#listeners]) l();
	}
}
