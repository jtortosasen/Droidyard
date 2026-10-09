import { describe, expect, it } from 'vitest';
import {
	BACKOFF_MAX_MS,
	BACKOFF_START_MS,
	CapabilityError,
	Client,
	ConnectionError,
	type ConnectionState,
	type EventFrame,
	type SocketLike,
	type Timers,
} from './client';

class FakeSocket implements SocketLike {
	sent: Array<Record<string, unknown>> = [];
	closed = false;
	onopen: ((ev: Event) => void) | null = null;
	onmessage: ((ev: MessageEvent) => void) | null = null;
	onclose: ((ev: CloseEvent) => void) | null = null;
	send(data: string) {
		this.sent.push(JSON.parse(data));
	}
	close() {
		this.closed = true;
	}
	// The daemon's side.
	open() {
		this.onopen?.({} as Event);
	}
	reply(frame: unknown) {
		this.onmessage?.({ data: JSON.stringify(frame) } as MessageEvent);
	}
	drop(code = 1006) {
		this.onclose?.({ code } as CloseEvent);
	}
	/** The id of the last request sent (the auth frame has none). */
	get lastId(): string {
		return this.sent.at(-1)!.id as string;
	}
}

class FakeTimers implements Timers {
	scheduled: Array<{ fn: () => void; ms: number; live: boolean }> = [];
	setTimeout(fn: () => void, ms: number) {
		const t = { fn, ms, live: true };
		this.scheduled.push(t);
		return t;
	}
	clearTimeout(h: unknown) {
		(h as { live: boolean }).live = false;
	}
	/** Fires the pending timer and returns its delay. */
	fire(): number {
		const t = this.scheduled.filter((x) => x.live).at(-1)!;
		t.live = false;
		t.fn();
		return t.ms;
	}
}

function setup() {
	const sockets: FakeSocket[] = [];
	const timers = new FakeTimers();
	const states: ConnectionState[] = [];
	const client = new Client({
		url: 'ws://127.0.0.1:7317/v1/ws',
		token: 'tok',
		timers,
		onState: (s) => states.push(s),
		createSocket: () => {
			const s = new FakeSocket();
			sockets.push(s);
			return s;
		},
	});
	return { client, sockets, timers, states };
}

const tick = () => new Promise((r) => setTimeout(r, 0));

describe('Client', () => {
	it('sends the auth frame first, before any request, even one made while connecting', async () => {
		// Rule 4: the daemon closes with 4401 if the first frame is not the token.
		const { client, sockets } = setup();
		client.connect();
		const p = client.call('system.health', {});
		sockets[0].open();
		expect(sockets[0].sent[0]).toEqual({ auth: 'tok' });
		expect(sockets[0].sent[1]).toMatchObject({ capability: 'system.health', params: {} });
		sockets[0].reply({ id: sockets[0].lastId, result: { version: '1' } });
		await expect(p).resolves.toEqual({ version: '1' });
	});

	it('matches replies to calls by id, whatever the order', async () => {
		const { client, sockets } = setup();
		client.connect();
		sockets[0].open();
		const a = client.call('system.health', {});
		const b = client.call('events.unsubscribe', { subscriptionId: 'x' });
		const [ida, idb] = sockets[0].sent.slice(1).map((f) => f.id);
		expect(ida).not.toBe(idb);
		sockets[0].reply({ id: idb, result: { second: true } });
		sockets[0].reply({ id: ida, result: { first: true } });
		await expect(a).resolves.toEqual({ first: true });
		await expect(b).resolves.toEqual({ second: true });
	});

	it('surfaces an error by its code, with the untrusted mark', async () => {
		// Consumers branch on error.code, never on the message (capability-api.md).
		const { client, sockets } = setup();
		client.connect();
		sockets[0].open();
		const p = client.call('system.health', {});
		sockets[0].reply({
			id: sockets[0].lastId,
			error: { code: 'DEVICE_NOT_FOUND', message: 'no such device', details: { serial: 'x' } },
			meta: { untrusted: true },
		});
		const err = await p.catch((e: unknown) => e);
		expect(err).toBeInstanceOf(CapabilityError);
		expect((err as CapabilityError).code).toBe('DEVICE_NOT_FOUND');
		expect((err as CapabilityError).untrusted).toBe(true);
		expect((err as CapabilityError).details).toEqual({ serial: 'x' });
	});

	it('delivers events by subscriptionId, with the handler registered before the answer settles', async () => {
		// The daemon writes a subscription's first frame right after the answer that names
		// it; a handler registered only after `await` could miss it.
		const { client, sockets } = setup();
		client.connect();
		sockets[0].open();
		const got: Record<string, EventFrame[]> = { a: [], b: [] };
		const subA = client.subscribe(
			'events.subscribe',
			{ topics: ['workspace.lifecycle'] },
			{ onEvent: (f) => got.a.push(f) },
		);
		sockets[0].reply({ id: sockets[0].lastId, result: { subscriptionId: 's1' } });
		const ev = (seq: number) => ({
			epoch: 'e',
			seq,
			time: 't',
			workspace: null,
			topic: 'workspace.lifecycle',
			type: 'x',
			payload: {},
		});
		// The event follows in the same tick as the answer, before the promise continuation runs.
		sockets[0].reply({ subscriptionId: 's1', event: ev(1) });
		const sa = await subA;
		const subB = client.subscribe(
			'events.subscribe',
			{ topics: ['operation.progress'] },
			{ onEvent: (f) => got.b.push(f) },
		);
		sockets[0].reply({ id: sockets[0].lastId, result: { subscriptionId: 's2' } });
		await subB;
		sockets[0].reply({ subscriptionId: 's2', event: ev(2) });
		sockets[0].reply({ subscriptionId: 's1', event: ev(3) });
		sockets[0].reply({ subscriptionId: 'unknown', event: ev(4) });
		expect(got.a.map((f) => f.event.seq)).toEqual([1, 3]);
		expect(got.b.map((f) => f.event.seq)).toEqual([2]);
		sa.stop();
		sockets[0].reply({ subscriptionId: 's1', event: ev(5) });
		expect(got.a).toHaveLength(2);
	});

	it('sends a long-running capability with waitMs 0 and follows it with operation.wait', async () => {
		// A slow build must not hold the session's request open (plan m2a D4); the result
		// of operation.wait is the waited operation's own, in the envelope's `result`.
		const { client, sockets } = setup();
		client.connect();
		sockets[0].open();
		const p = client.call('build.tasks', {}, { workspace: '/w' });
		expect(sockets[0].sent[1]).toMatchObject({ capability: 'build.tasks', workspace: '/w', waitMs: 0 });
		sockets[0].reply({ id: sockets[0].lastId, operation: { id: 'op1', state: 'running' } });
		await tick();
		expect(sockets[0].sent[2]).toMatchObject({
			capability: 'operation.wait',
			params: { operationId: 'op1' },
		});
		expect(sockets[0].sent[2].workspace).toBeUndefined();
		sockets[0].reply({ id: sockets[0].lastId, operation: { id: 'op1', state: 'running' } });
		await tick();
		expect(sockets[0].sent[3]).toMatchObject({ capability: 'operation.wait' });
		sockets[0].reply({
			id: sockets[0].lastId,
			operation: { id: 'op1', state: 'completed' },
			result: { ok: 1 },
		});
		await expect(p).resolves.toEqual({ ok: 1 });
	});

	it('sends a short capability without waitMs, and a failed operation rejects with its error', async () => {
		const { client, sockets } = setup();
		client.connect();
		sockets[0].open();
		client.call('system.health', {}).catch(() => {});
		expect('waitMs' in sockets[0].sent[1]).toBe(false);
		const p = client.call('build.tasks', {});
		sockets[0].reply({ id: sockets[0].lastId, operation: { id: 'op', state: 'queued' } });
		await tick();
		sockets[0].reply({
			id: sockets[0].lastId,
			operation: { id: 'op', state: 'failed' },
			error: { code: 'BUILD_SIDECAR_CRASHED', message: 'm' },
		});
		await expect(p).rejects.toMatchObject({ code: 'BUILD_SIDECAR_CRASHED' });
	});

	it('maps close code 4401 to unauthorized: the calls end, nothing reconnects, later calls fail', async () => {
		// The token is per launch: retrying a refused one forever would be a loop.
		const { client, sockets, timers, states } = setup();
		client.connect();
		sockets[0].open();
		const p = client.call('system.health', {});
		sockets[0].drop(4401);
		await expect(p).rejects.toMatchObject({ kind: 'unauthorized' });
		expect(client.state).toBe('unauthorized');
		expect(states.at(-1)).toBe('unauthorized');
		expect(timers.scheduled.filter((t) => t.live)).toHaveLength(0);
		expect(sockets).toHaveLength(1);
		await expect(client.call('system.health', {})).rejects.toBeInstanceOf(ConnectionError);
	});

	it('reconnects after any other close, with a backoff from 250 ms doubling to 5 s', () => {
		const { client, sockets, timers } = setup();
		client.connect();
		const delays: number[] = [];
		for (let i = 0; i < 8; i++) {
			sockets[i].drop(1006); // never opened: a refused connection
			expect(client.state).toBe('reconnecting');
			delays.push(timers.fire());
		}
		expect(delays).toEqual([250, 500, 1000, 2000, 4000, 5000, 5000, 5000]);
		expect(BACKOFF_START_MS).toBe(250);
		expect(BACKOFF_MAX_MS).toBe(5000);
		expect(sockets).toHaveLength(9);
	});

	it('starts the backoff over once the daemon has answered, and authenticates again on the new socket', async () => {
		const { client, sockets, timers, states } = setup();
		client.connect();
		sockets[0].open();
		sockets[0].drop(1006);
		expect(timers.fire()).toBe(250);
		sockets[1].open();
		expect(sockets[1].sent[0]).toEqual({ auth: 'tok' });
		const p = client.call('system.health', {});
		sockets[1].reply({ id: sockets[1].lastId, result: {} });
		await p;
		sockets[1].drop(1006);
		expect(timers.fire()).toBe(250);
		expect(states).toEqual(['connecting', 'open', 'reconnecting', 'open', 'reconnecting']);
	});

	it('ends the calls and subscriptions a lost connection carried, and keeps those made while it was down', async () => {
		// A call in flight is lost (the operation, if one started, goes on in the daemon);
		// a call made while reconnecting was never sent and goes out on the next socket.
		const { client, sockets, timers } = setup();
		client.connect();
		sockets[0].open();
		let ended: ConnectionError | undefined;
		const sub = client.subscribe(
			'events.subscribe',
			{ topics: ['workspace.lifecycle'] },
			{ onEvent() {}, onEnd: (e) => (ended = e) },
		);
		sockets[0].reply({ id: sockets[0].lastId, result: { subscriptionId: 's' } });
		await sub;
		const inFlight = client.call('system.health', {});
		sockets[0].drop(1006);
		await expect(inFlight).rejects.toMatchObject({ kind: 'lost' });
		expect(ended?.kind).toBe('lost');
		const queued = client.call('system.health', {});
		timers.fire();
		sockets[1].open();
		expect(sockets[1].sent.map((f) => f.capability ?? 'auth')).toEqual(['auth', 'system.health']);
		sockets[1].reply({ id: sockets[1].lastId, result: { back: true } });
		await expect(queued).resolves.toEqual({ back: true });
	});

	it('keeps a call made before the first connection when that attempt fails', async () => {
		// The daemon may be starting: a call that was never sent waits for a socket that opens.
		const { client, sockets, timers } = setup();
		client.connect();
		const p = client.call('system.health', {});
		sockets[0].drop(1006);
		timers.fire();
		sockets[1].open();
		expect(sockets[1].sent.map((f) => f.capability ?? 'auth')).toEqual(['auth', 'system.health']);
		sockets[1].reply({ id: sockets[1].lastId, result: { ok: 1 } });
		await expect(p).resolves.toEqual({ ok: 1 });
	});

	it('close() ends the session for good and stops a pending reconnect', async () => {
		const { client, sockets, timers } = setup();
		client.connect();
		sockets[0].open();
		sockets[0].drop(1006);
		client.close();
		expect(timers.scheduled.filter((t) => t.live)).toHaveLength(0);
		expect(client.state).toBe('closed');
		await expect(client.call('system.health', {})).rejects.toMatchObject({ kind: 'closed' });
	});
});
