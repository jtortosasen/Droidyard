// The Live view panel's model (plan m2p step 5; ADR-0078 §4, §7): a pure state machine
// with no markup, no socket and no decoder, so a test drives it with a fake client.
//
//   idle         nothing is watched (hidden, or no device)
//   starting     `device.watchScreen` sent or answered, no frame decoded yet
//   live         a frame decoded
//   stopped      the stream ended; the reason is shown with Restart
//   unsupported  the browser has no WebCodecs video decoder
//
// `secure` is the daemon's `state` event (ADR-0080 §3): the focused window has FLAG_SECURE
// and the screen is shown black. It belongs to one watch and starts false with each.
//
// It watches the toolbar's device only while the panel is shown (its tab is current and
// its region open), and unsubscribes otherwise. One re-watch is allowed per failure
// streak, whether it came from a `detached` event or a decoder error; a frame that
// decodes ends the streak. The component opens the screen socket for each new
// `generation` of the view and reports what the socket and the decoder do.
import type { Params, Result } from '../api/capabilities.gen';
import {
	CapabilityError,
	ConnectionError,
	type ConnectionState,
	type SubscribeHandlers,
	type Subscription,
} from '../api/client';
import { eventTopics } from '../api/capabilities.gen';

export interface ScreenClient {
	subscribe(
		name: 'device.watchScreen',
		params: Params<'device.watchScreen'>,
		handlers: SubscribeHandlers,
	): Promise<Subscription & { readonly result: Result<'device.watchScreen'> }>;
	call(name: 'events.unsubscribe', params: Params<'events.unsubscribe'>): Promise<Result<'events.unsubscribe'>>;
}

export interface ScreenDevices {
	view: { selected: string };
	subscribe(listener: () => void): () => void;
}

export type LiveState = 'idle' | 'starting' | 'live' | 'stopped' | 'unsupported';
export type StopReason = 'deviceGone' | 'exited' | 'failed' | 'detached';

export interface LiveViewView {
	state: LiveState;
	/** Set while `stopped`. */
	reason: StopReason | null;
	/** The words for the reason (the daemon's, or ours); '' for none. */
	cause: string;
	device: string;
	/** The stream the socket attaches to; '' until `device.watchScreen` was answered. */
	streamId: string;
	/** The stream takes binary input frames (`device.watchScreen`'s `control`; false for a host that lacks the field). */
	control: boolean;
	/** The focused window is secure (FLAG_SECURE): the screen is black by the platform's rule. */
	secure: boolean;
	/** Counts the answered watches: a new value means a new socket and a new decoder. */
	generation: number;
	width: number;
	height: number;
}

const TOPIC = eventTopics['device.watchScreen'];

/** The screen socket's close codes that end the stream for us (ADR-0078 §4): the rest are the daemon's own end, which its `stopped` event reports. */
const REFUSED: Record<number, string> = {
	4401: 'the daemon refused the token',
	4404: 'the daemon does not know the stream',
	4409: 'another client already attached to the stream',
	1003: 'the daemon refused what the client sent',
};

interface Run {
	ended: boolean;
}

export class LiveViewModel {
	#client: ScreenClient;
	#devices: ScreenDevices;
	#supported: boolean;
	#listeners = new Set<() => void>();
	#view: LiveViewView | null = null;
	#off: () => void;

	#active = false;
	#state: LiveState;
	#reason: StopReason | null = null;
	#cause = '';
	#device = ''; // the device the current watch (or stop) is for
	#streamId = '';
	#secure = false;
	#control = false;
	#generation = 0;
	#width = 0;
	#height = 0;
	#run: Run | null = null;
	#sub: Subscription | null = null;
	#spent = false; // the failure streak's one re-watch is used
	#waiting = false; // the connection was lost; the watch starts again when it is back

	#onDeviceGone: (device: string) => void;

	/** `onDeviceGone` hears of the device whose stream stopped `deviceGone` (a remote host stopped lending it, plan m2t step 7). */
	constructor(client: ScreenClient, devices: ScreenDevices, opts: { supported: boolean; onDeviceGone?: (device: string) => void }) {
		this.#client = client;
		this.#devices = devices;
		this.#supported = opts.supported;
		this.#onDeviceGone = opts.onDeviceGone ?? (() => {});
		this.#state = opts.supported ? 'idle' : 'unsupported';
		this.#off = devices.subscribe(() => this.#sync());
	}

	get view(): LiveViewView {
		this.#view ??= {
			state: this.#state,
			reason: this.#reason,
			cause: this.#cause,
			device: this.#device,
			streamId: this.#streamId,
			control: this.#control,
			secure: this.#secure,
			generation: this.#generation,
			width: this.#width,
			height: this.#height,
		};
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** The panel is shown (its tab current, its region open, the page visible) or not. */
	setActive(active: boolean): void {
		if (active === this.#active) return;
		this.#active = active;
		this.#sync();
	}

	/** After a stop: watches the toolbar's device again. */
	restart(): void {
		if (this.#state !== 'stopped') return;
		this.#spent = false;
		this.#state = 'idle';
		this.#device = '';
		this.#sync();
	}

	onConnection(state: ConnectionState): void {
		if (state === 'open' && this.#waiting) {
			this.#waiting = false;
			this.#begin(this.#device);
		} else if ((state === 'unauthorized' || state === 'closed') && this.#state === 'starting') {
			this.#release();
			this.#stop('failed', state === 'unauthorized' ? 'the daemon refused the token' : 'the connection was closed');
		}
	}

	/** A frame came out of the decoder: the stream is live and the failure streak is over. */
	frameDecoded(): void {
		this.#spent = false;
		if (this.#state === 'starting' && this.#streamId !== '') {
			this.#state = 'live';
			this.#changed();
		}
	}

	/** The decoder failed. */
	decoderError(message: string): void {
		if (this.#state !== 'starting' && this.#state !== 'live') return;
		this.#failure('failed', `the browser could not decode the video: ${message}`, true);
	}

	/** The screen socket was closed by the daemon. Only the refusals end the stream here; its own end arrives as `stopped`. */
	socketClosed(code: number, generation: number): void {
		if (generation !== this.#generation || (this.#state !== 'starting' && this.#state !== 'live')) return;
		const why = REFUSED[code];
		if (why === undefined) return;
		this.#release();
		this.#stop('failed', why);
	}

	dispose(): void {
		this.#off();
		this.#release();
		this.#listeners.clear();
	}

	/** Brings the watch in line with what is wanted: the toolbar's device while the panel is shown. */
	#sync(): void {
		if (!this.#supported) return;
		const want = this.#active ? this.#devices.view.selected : '';
		if (want === '') {
			if (this.#state !== 'idle') {
				this.#release();
				this.#spent = false;
				this.#reset('idle');
			}
			return;
		}
		if (want === this.#device && this.#state !== 'idle') return; // watching it, or stopped on it
		this.#release();
		this.#spent = false;
		this.#begin(want);
	}

	#begin(device: string): void {
		const run: Run = { ended: false };
		this.#run = run;
		this.#waiting = false;
		this.#device = device;
		this.#streamId = '';
		this.#control = false;
		this.#secure = false;
		this.#reason = null;
		this.#cause = '';
		this.#state = 'starting';
		this.#changed();

		const handlers: SubscribeHandlers = {
			onEvent: (frame) => {
				const ev = frame.event;
				if (this.#run !== run || run.ended || ev.topic !== TOPIC) return;
				if (ev.type === 'state') {
					const secure = (ev.payload as { secure?: unknown }).secure === true;
					if (secure !== this.#secure) {
						this.#secure = secure;
						this.#changed();
					}
					return;
				}
				if (ev.type !== 'stopped') return;
				this.#stopped(run, frame.subscriptionId, ev.payload as { reason?: unknown; cause?: unknown });
			},
			onEnd: (why) => {
				if (this.#run !== run || run.ended) return;
				this.#sub = null;
				if (why.kind === 'lost') {
					this.#waiting = true;
					this.#streamId = '';
					this.#control = false;
					this.#state = 'starting';
					this.#changed();
				} else {
					this.#stop('failed', why.message);
				}
			},
		};
		this.#client.subscribe('device.watchScreen', { device }, handlers).then(
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
				this.#streamId = sub.result.streamId;
				this.#control = sub.result.control === true;
				this.#width = sub.result.width;
				this.#height = sub.result.height;
				this.#generation++;
				this.#changed();
			},
			(err: unknown) => {
				if (this.#run !== run || run.ended) return;
				if (err instanceof ConnectionError && err.kind === 'lost') {
					this.#waiting = true;
					this.#changed();
				} else if (err instanceof CapabilityError) {
					this.#stop('failed', `${err.code}: ${err.message}`);
				} else {
					this.#stop('failed', err instanceof Error ? err.message : 'the stream could not be started');
				}
			},
		);
	}

	#stopped(run: Run, subscriptionId: string, p: { reason?: unknown; cause?: unknown }): void {
		run.ended = true;
		// ADR-0041 §4: the subscription stays open, empty, until it is unsubscribed.
		this.#sub?.stop();
		this.#sub = null;
		this.#send(subscriptionId);
		const reason = (['deviceGone', 'exited', 'failed', 'detached'] as const).find((r) => r === p.reason) ?? 'failed';
		const device = this.#device;
		// Only `detached` (the socket went away) is worth one more try; the rest are shown as they are.
		this.#failure(reason, typeof p.cause === 'string' ? p.cause : '', reason === 'detached');
		if (reason === 'deviceGone') this.#onDeviceGone(device);
	}

	/** A failure that earns one re-watch per streak: the next one, before a frame decodes, stops. */
	#failure(reason: StopReason, cause: string, retryable: boolean): void {
		this.#release();
		if (retryable && !this.#spent && this.#device !== '') {
			this.#spent = true;
			this.#begin(this.#device);
		} else {
			this.#stop(reason, cause);
		}
	}

	#stop(reason: StopReason, cause: string): void {
		this.#reset('stopped');
		this.#reason = reason;
		this.#cause = cause;
		this.#changed();
	}

	#reset(state: LiveState): void {
		this.#state = state;
		this.#reason = null;
		this.#cause = '';
		this.#streamId = '';
		this.#control = false;
		this.#secure = false;
		this.#device = state === 'idle' ? '' : this.#device;
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
