import { describe, expect, it } from 'vitest';
import type { Params, Result } from '../api/capabilities.gen';
import { ConnectionError, type BusEvent, type SubscribeHandlers, type Subscription } from '../api/client';
import { ActivityStore, type ActivityClient, type ActivityTimers } from './activity';

type Snap = Result<'operation.watch'>;
type Live = Snap['operations'][number];

interface Watch {
	handlers: SubscribeHandlers;
	resolve: (s: Subscription & { result: Snap }) => void;
	reject: (e: unknown) => void;
	id: string;
}

class FakeClient implements ActivityClient {
	watches: Watch[] = [];
	unsubscribed: string[] = [];
	stopped: string[] = [];
	subscribe(_n: 'operation.watch', _p: Params<'operation.watch'>, handlers: SubscribeHandlers) {
		return new Promise<Subscription & { result: Snap }>((resolve, reject) => {
			this.watches.push({ handlers, resolve, reject, id: `sub${this.watches.length + 1}` });
		});
	}
	call(_n: 'events.unsubscribe', p: Params<'events.unsubscribe'>) {
		this.unsubscribed.push(p.subscriptionId);
		return Promise.resolve({});
	}
	/** The daemon answers watch i with a snapshot. */
	answer(i: number, operations: Live[] = []) {
		const w = this.watches[i]!;
		w.resolve({ subscriptionId: w.id, result: { subscriptionId: w.id, operations }, stop: () => this.stopped.push(w.id) });
	}
	event(i: number, topic: string, type: string, payload: unknown, operation?: string, workspace: string | null = 'w1') {
		const w = this.watches[i]!;
		const event: BusEvent = { epoch: 'e', seq: 1, time: 't', workspace, operation, topic, type, payload };
		w.handlers.onEvent({ subscriptionId: w.id, event });
	}
	state(i: number, op: string, p: { capability?: string; state: string; startedAt?: string; client?: string; code?: string }) {
		this.event(i, 'operation.state', 'state', { capability: 'build.assemble', startedAt: '2026-10-10T10:00:00Z', ...p }, op);
	}
}

const settle = () => new Promise((r) => setTimeout(r, 0));

const noTimers: ActivityTimers = { setTimeout: () => null, clearTimeout: () => {} };

function live(id: string, state: 'queued' | 'running', over: Partial<Live> = {}): Live {
	return { operationId: id, capability: 'build.assemble', workspace: 'w1', client: 'mcp', state, startedAt: '2026-10-10T10:00:00Z', ...over };
}

function setup(max?: number) {
	const client = new FakeClient();
	const store = new ActivityStore(client, { max, timers: noTimers });
	store.start();
	return { client, store };
}

const ids = (s: ActivityStore) => s.operations.map((o) => o.id);

describe('ActivityStore', () => {
	// Decision: the snapshot and the events are merged by operation id, so an operation in
	// both is one row, and an event for a snapshot row updates it.
	it('merges a start already in the snapshot into one row, and a running event updates a queued row', async () => {
		const { client, store } = setup();
		client.state(0, 'op1', { state: 'running' }); // event before the answer
		client.answer(0, [live('op1', 'running'), live('op2', 'queued')]);
		await settle();
		expect(ids(store).sort()).toEqual(['op1', 'op2']);
		expect(store.operations.find((o) => o.id === 'op2')?.state).toBe('queued');
		client.state(0, 'op2', { state: 'running' });
		expect(store.operations.find((o) => o.id === 'op2')?.state).toBe('running');
	});

	// Decision: a terminal state is never replaced; a state never moves backwards (a
	// snapshot taken just before an event cannot undo it).
	it('keeps a terminal state against a later snapshot or event, and a terminal event creates a row', async () => {
		const { client, store } = setup();
		client.state(0, 'op1', { state: 'completed' });
		client.answer(0, [live('op1', 'running')]);
		await settle();
		expect(store.operations).toHaveLength(1);
		expect(store.operations[0]!.state).toBe('completed');
		client.state(0, 'op1', { state: 'running' });
		client.state(0, 'op1', { state: 'failed', code: 'X' });
		expect(store.operations[0]!.state).toBe('completed');
		expect(store.operations[0]!.code).toBeUndefined();
	});

	// Decision: a state never moves backwards (a stale snapshot row cannot undo an event).
	it('does not move a running row back to queued', async () => {
		const { client, store } = setup();
		client.state(0, 'op1', { state: 'running' });
		client.answer(0, [live('op1', 'queued')]);
		await settle();
		expect(store.operations[0]!.state).toBe('running');
	});

	// Decision: the store keeps 500 operations (D16), dropping terminal ones first.
	it('keeps at most the cap, dropping the oldest terminal rows before a live one', async () => {
		const { client, store } = setup(3);
		client.answer(0, []);
		await settle();
		client.state(0, 'live', { state: 'running', startedAt: '2026-10-10T10:00:00Z' });
		for (let i = 1; i <= 4; i++) client.state(0, `t${i}`, { state: 'completed', startedAt: `2026-10-10T10:0${i}:00Z` });
		expect(store.operations).toHaveLength(3);
		expect(ids(store)).toEqual(['t4', 't3', 'live']);
	});

	// Decision: after a gap the store asks again; a live row the new snapshot lacks is
	// "ended unseen" (no mirror stays stuck) and terminal rows survive. Merge-only would
	// leave op1 running; replace-all would lose done.
	it('after a gap, marks a missing live row unseen, keeps terminal rows, and ends the old subscription', async () => {
		const { client, store } = setup();
		client.answer(0, [live('op1', 'running'), live('op2', 'running')]);
		await settle();
		client.state(0, 'done', { state: 'completed' });
		const fired: string[] = [];
		store.onTerminal(['build.assemble'], (op) => fired.push(`${op.id}:${op.state}`));
		client.event(0, 'events.gap', 'gap', { fromSeq: 1, toSeq: 9 });
		expect(client.unsubscribed).toEqual(['sub1']);
		expect(client.watches).toHaveLength(2);
		client.answer(1, [live('op2', 'running')]);
		await settle();
		const byId = Object.fromEntries(store.operations.map((o) => [o.id, o.state]));
		expect(byId).toEqual({ op1: 'unseen', op2: 'running', done: 'completed' });
		expect(fired).toEqual(['op1:unseen']);
		expect(store.active()).toHaveLength(1);
	});

	// Decision: a live row started by an event of the new run, delivered before the
	// snapshot's answer, is not "unseen" just because the snapshot predates it.
	it('does not mark unseen a row an event of the new subscription confirmed', async () => {
		const { client, store } = setup();
		client.answer(0, [live('op1', 'running')]);
		await settle();
		client.event(0, 'events.gap', 'gap', {});
		client.state(1, 'op1', { state: 'running' });
		client.answer(1, []);
		await settle();
		expect(store.operations[0]!.state).toBe('running');
	});

	// Decision: D13's trigger fires once per terminal operation of a named capability, by any client.
	it('calls a trigger once when a named capability ends, and not for another capability or a running event', async () => {
		const { client, store } = setup();
		client.answer(0, []);
		await settle();
		const fired: string[] = [];
		const off = store.onTerminal(['emulator.start'], (op) => fired.push(`${op.id}:${op.client}:${op.state}`));
		client.state(0, 'a', { capability: 'emulator.start', state: 'running', client: 'mcp' });
		client.state(0, 'b', { capability: 'device.list', state: 'completed', client: 'ui' });
		expect(fired).toEqual([]);
		client.state(0, 'a', { capability: 'emulator.start', state: 'completed', client: 'mcp' });
		client.state(0, 'a', { capability: 'emulator.start', state: 'completed', client: 'mcp' });
		expect(fired).toEqual(['a:mcp:completed']);
		off();
		client.state(0, 'c', { capability: 'emulator.start', state: 'failed' });
		expect(fired).toHaveLength(1);
	});

	// Decision (D11): active() returns only non-terminal operations that satisfy the predicate.
	it('active() lists the live operations matching a predicate', async () => {
		const { client, store } = setup();
		client.answer(0, [live('b1', 'running'), live('b2', 'queued', { workspace: 'w2' })]);
		await settle();
		client.state(0, 'b3', { state: 'completed' });
		expect(store.active((o) => o.workspace === 'w1').map((o) => o.id)).toEqual(['b1']);
		expect(store.active()).toHaveLength(2);
	});

	// Decision: a lost session waits for the connection and watches again (history kept);
	// a refused watch is retried; an answer to a superseded start is ended at once.
	it('restarts after a lost session once open, keeping history', async () => {
		const { client, store } = setup();
		client.answer(0, []);
		await settle();
		client.state(0, 'done', { state: 'completed' });
		client.watches[0]!.handlers.onEnd?.(new ConnectionError('lost'));
		expect(store.status.state).toBe('waiting');
		expect(client.watches).toHaveLength(1);
		store.onConnection('open');
		expect(client.watches).toHaveLength(2);
		client.answer(1, [live('x', 'running')]);
		await settle();
		expect(ids(store).sort()).toEqual(['done', 'x']);
		expect(store.status.state).toBe('watching');
	});

	it('ends an answer that arrives after the watch was replaced', async () => {
		const { client } = setup();
		client.event(0, 'events.gap', 'gap', {}); // before the first answer
		client.answer(0, []);
		await settle();
		expect(client.unsubscribed).toContain('sub1');
	});

	// Decision: a start the daemon refused is asked again after a delay, not forever at once.
	it('retries a refused watch after a delay', async () => {
		const client = new FakeClient();
		const pending: Array<() => void> = [];
		const delays: number[] = [];
		const store = new ActivityStore(client, {
			timers: { setTimeout: (fn, ms) => (delays.push(ms), pending.push(fn), 1), clearTimeout: () => {} },
		});
		store.start();
		client.watches[0]!.reject(new Error('boom'));
		await settle();
		expect(store.status.state).toBe('error');
		expect(client.watches).toHaveLength(1);
		pending.shift()!();
		expect(client.watches).toHaveLength(2);
		expect(delays).toEqual([1000]);
	});
});
