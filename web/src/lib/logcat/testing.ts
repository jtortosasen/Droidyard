// Fakes shared by the Logcat tests: a frame scheduler the test ticks, and a client whose
// answers and events the test plays (the daemon's side).
import type { BusEvent, SubscribeHandlers, Subscription } from '../api/client';
import type { Params, Result } from '../api/capabilities.gen';
import type { FrameScheduler } from './batcher';
import type { Line, Priority, StreamLine } from './entry';
import type { StreamClient } from './stream';

export class FakeFrames implements FrameScheduler {
	#next = 1;
	#callbacks = new Map<number, () => void>();
	requests = 0;

	request(callback: () => void): unknown {
		this.requests++;
		const id = this.#next++;
		this.#callbacks.set(id, callback);
		return id;
	}
	cancel(handle: unknown): void {
		this.#callbacks.delete(handle as number);
	}
	/** Frames asked for and not yet run. */
	get pending(): number {
		return this.#callbacks.size;
	}
	/** Runs the frames asked for so far, as one animation frame would. */
	tick(): void {
		const run = [...this.#callbacks.values()];
		this.#callbacks.clear();
		for (const cb of run) cb();
	}
}

export interface WatchCall {
	params: Params<'device.watchLogs'>;
	handlers: SubscribeHandlers;
	resolve: (sub: Subscription) => void;
	reject: (err: unknown) => void;
	/** Set once the call was answered. */
	id: string | null;
}

export class FakeClient implements StreamClient {
	watches: WatchCall[] = [];
	/** The subscription ids `events.unsubscribe` was called with, in order. */
	unsubscribed: string[] = [];
	/** The subscriptions whose local handler was removed (`Subscription.stop`). */
	stopped: string[] = [];

	subscribe(
		_name: 'device.watchLogs',
		params: Params<'device.watchLogs'>,
		handlers: SubscribeHandlers,
	): Promise<Subscription> {
		return new Promise((resolve, reject) => {
			this.watches.push({ params, handlers, resolve, reject, id: null });
		});
	}

	call(_name: 'events.unsubscribe', params: Params<'events.unsubscribe'>): Promise<Result<'events.unsubscribe'>> {
		this.unsubscribed.push(params.subscriptionId);
		return Promise.resolve({});
	}

	/** The daemon answers watch `i` with a subscription id. */
	answer(i: number, id = `sub${i + 1}`): void {
		const w = this.watches[i]!;
		w.id = id;
		w.resolve({
			subscriptionId: id,
			stop: () => {
				this.stopped.push(id);
			},
		});
	}

	/** The daemon sends an event on watch `i`'s subscription. */
	emit(i: number, topic: string, type: string, payload: unknown): void {
		const w = this.watches[i]!;
		const event: BusEvent = {
			epoch: 'e',
			seq: 1,
			time: '2026-10-09T12:00:00Z',
			workspace: null,
			topic,
			type,
			payload,
		};
		w.handlers.onEvent({ subscriptionId: w.id ?? `sub${i + 1}`, event });
	}

	lines(i: number, lines: StreamLine[], dropped = 0): void {
		this.emit(i, 'device.logcat', 'lines', { device: 'emulator-5554', lines, dropped, untrusted: true });
	}
}

/** Lets promise callbacks run. */
export function settle(): Promise<void> {
	return new Promise((resolve) => setTimeout(resolve, 0));
}

/** A line at a given microsecond of 2026-10-09T12:00:00 UTC. */
export function line(micro: number, message: string, over: Partial<Line> = {}): Line {
	const time = `2026-10-09T12:00:00.${String(micro).padStart(6, '0')}Z`;
	return { time, pid: 100, tid: 100, priority: 'info' as Priority, tag: 'Tag', message, ...over };
}
