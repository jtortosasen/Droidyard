// Fakes shared by the build tests: a daemon's side of `call`, and of the two
// subscriptions (operation.watch for the activity store, events.subscribe for the hub).
import type { Params, Result } from '../api/capabilities.gen';
import {
	CapabilityError,
	type BusEvent,
	type CallOptions,
	type SubscribeHandlers,
	type Subscription,
} from '../api/client';
import { ActivityStore, type ActivityClient } from '../ops/activity';
import { EventsHub, type HubClient } from './hub';

export const settle = () => new Promise<void>((r) => setTimeout(r, 0));

export function capError(code: string, message = code): CapabilityError {
	return new CapabilityError({ code, message }, false);
}

export interface Recorded {
	name: string;
	params: unknown;
	workspace?: string;
}

/** `call` answered by per-capability handlers; every call is recorded. */
export class FakeCalls {
	calls: Recorded[] = [];
	handlers = new Map<string, (params: unknown) => unknown>();

	on(name: string, fn: (params: unknown) => unknown): void {
		this.handlers.set(name, fn);
	}
	names(): string[] {
		return this.calls.map((c) => c.name);
	}
	call = ((name: string, params: unknown, opts?: CallOptions) => {
		this.calls.push({ name, params, workspace: opts?.workspace });
		const h = this.handlers.get(name);
		if (!h) return Promise.reject(new Error(`no handler for ${name}`));
		try {
			return Promise.resolve(h(params));
		} catch (e) {
			return Promise.reject(e);
		}
	}) as <N extends string>(name: N, params: unknown, opts?: CallOptions) => Promise<never>;
}

/** The daemon's side of one subscription. */
export class FakeStream {
	handlers: SubscribeHandlers[] = [];
	ids: string[] = [];
	unsubscribed: string[] = [];
	stopped: string[] = [];
	#answers: ((r: unknown) => void)[] = [];
	#extra: unknown;

	constructor(private prefix: string, extra: unknown = {}) {
		this.#extra = extra;
	}

	subscribe(_name: string, _params: unknown, handlers: SubscribeHandlers): Promise<Subscription & { result: never }> {
		const i = this.handlers.length;
		this.handlers.push(handlers);
		const id = `${this.prefix}${i + 1}`;
		this.ids.push(id);
		return new Promise((resolve) => {
			this.#answers.push((result) =>
				resolve({ subscriptionId: id, result: result as never, stop: () => this.stopped.push(id) }),
			);
		});
	}
	call(_name: 'events.unsubscribe', p: Params<'events.unsubscribe'>): Promise<Result<'events.unsubscribe'>> {
		this.unsubscribed.push(p.subscriptionId);
		return Promise.resolve({});
	}
	answer(i = 0, result: unknown = this.#extra): void {
		this.#answers[i]!(result);
	}
	emit(i: number, ev: Partial<BusEvent> & { topic: string; type: string; payload: unknown }): void {
		const event: BusEvent = { epoch: 'e', seq: 1, time: 't', workspace: null, ...ev };
		this.handlers[i]!.onEvent({ subscriptionId: this.ids[i]!, event });
	}
}

/** The daemon's operation view of one call: what operation.watch's snapshot lists. */
export function liveOp(id: string, state: 'queued' | 'running', over: Record<string, unknown> = {}) {
	return {
		operationId: id,
		capability: 'build.assemble',
		workspace: 'w1',
		client: 'mcp',
		state,
		startedAt: '2026-10-10T10:00:00Z',
		...over,
	};
}

export async function activityWith(operations: ReturnType<typeof liveOp>[] = []) {
	const watch = new FakeStream('watch', { subscriptionId: 'watch1', operations });
	const store = new ActivityStore(watch as unknown as ActivityClient, { timers: { setTimeout: () => null, clearTimeout: () => {} } });
	store.start();
	watch.answer(0);
	await settle();
	return { watch, store };
}

export function stateEvent(
	watch: FakeStream,
	operation: string,
	p: { capability?: string; state: string; client?: string },
	workspace: string | null = 'w1',
): void {
	watch.emit(0, {
		topic: 'operation.state',
		type: 'state',
		operation,
		workspace,
		payload: { capability: 'build.assemble', client: 'mcp', startedAt: '2026-10-10T10:00:00Z', ...p },
	});
}

export async function hubStarted() {
	const events = new FakeStream('ev', { subscriptionId: 'ev1' });
	const hub = new EventsHub(events as unknown as HubClient);
	hub.start();
	events.answer(0);
	await settle();
	return { events, hub };
}
