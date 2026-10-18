import { describe, expect, it } from 'vitest';
import type { Params, Result } from '../api/capabilities.gen';
import type { BusEvent, SubscribeHandlers, Subscription } from '../api/client';
import { LiveViewModel, type ScreenClient } from './liveview';

type Answer = Subscription & { readonly result: Result<'device.watchScreen'> };

class FakeClient implements ScreenClient {
	watches: { device: string; handlers: SubscribeHandlers; resolve: (s: Answer) => void }[] = [];
	unsubscribed: string[] = [];
	stoppedLocally: string[] = [];

	subscribe(_n: 'device.watchScreen', params: Params<'device.watchScreen'>, handlers: SubscribeHandlers): Promise<Answer> {
		return new Promise((resolve) => this.watches.push({ device: params.device, handlers, resolve }));
	}
	call(_n: 'events.unsubscribe', params: Params<'events.unsubscribe'>): Promise<Result<'events.unsubscribe'>> {
		this.unsubscribed.push(params.subscriptionId);
		return Promise.resolve({});
	}
	answer(i: number): void {
		const w = this.watches[i]!;
		const id = `sub${i + 1}`;
		w.resolve({
			subscriptionId: id,
			result: { subscriptionId: id, device: w.device, streamId: `stream${i + 1}`, width: 1080, height: 2400 },
			stop: () => this.stoppedLocally.push(id),
		});
	}
	state(i: number, secure: boolean): void {
		const event: BusEvent = {
			epoch: 'e',
			seq: 1,
			time: 't',
			workspace: null,
			topic: 'device.screen',
			type: 'state',
			payload: { device: this.watches[i]!.device, secure },
		};
		this.watches[i]!.handlers.onEvent({ subscriptionId: `sub${i + 1}`, event });
	}
	stopped(i: number, reason: string, cause?: string): void {
		const event: BusEvent = {
			epoch: 'e',
			seq: 1,
			time: 't',
			workspace: null,
			topic: 'device.screen',
			type: 'stopped',
			payload: { device: this.watches[i]!.device, reason, ...(cause ? { cause } : {}) },
		};
		this.watches[i]!.handlers.onEvent({ subscriptionId: `sub${i + 1}`, event });
	}
}

class FakeDevices {
	view = { selected: 'emulator-5554' };
	#l = new Set<() => void>();
	subscribe(l: () => void) {
		this.#l.add(l);
		return () => this.#l.delete(l);
	}
	select(s: string) {
		this.view = { selected: s };
		for (const l of [...this.#l]) l();
	}
}

const settle = async () => {
	for (let i = 0; i < 5; i++) await Promise.resolve();
};

function make(supported = true) {
	const client = new FakeClient();
	const devices = new FakeDevices();
	const model = new LiveViewModel(client, devices, { supported });
	return { client, devices, model };
}

async function live() {
	const t = make();
	t.model.setActive(true);
	t.client.answer(0);
	await settle();
	t.model.frameDecoded();
	return t;
}

describe('LiveViewModel', () => {
	it('is idle until the panel is shown, then watches the toolbar device', async () => {
		const t = make();
		expect(t.model.view.state).toBe('idle');
		expect(t.client.watches).toHaveLength(0);
		t.model.setActive(true);
		expect(t.client.watches.map((w) => w.device)).toEqual(['emulator-5554']);
		expect(t.model.view.state).toBe('starting');
		t.client.answer(0);
		await settle();
		expect(t.model.view).toMatchObject({ state: 'starting', streamId: 'stream1', width: 1080, height: 2400, generation: 1 });
		t.model.frameDecoded();
		expect(t.model.view.state).toBe('live');
	});

	// Decision (plan m2p step 5, ADR-0078 §7): a hidden panel holds no stream; it
	// unsubscribes, and showing it watches again.
	it('unsubscribes when hidden and watches again when shown', async () => {
		const t = await live();
		t.model.setActive(false);
		expect(t.client.unsubscribed).toEqual(['sub1']);
		expect(t.model.view).toMatchObject({ state: 'idle', streamId: '' });
		t.model.setActive(true);
		expect(t.client.watches).toHaveLength(2);
	});

	it('unsubscribes an answer that arrives after the panel was hidden', async () => {
		const t = make();
		t.model.setActive(true);
		t.model.setActive(false);
		t.client.answer(0);
		await settle();
		expect(t.client.unsubscribed).toEqual(['sub1']);
		expect(t.model.view.state).toBe('idle');
	});

	it('re-watches when the toolbar device changes', async () => {
		const t = await live();
		t.devices.select('emulator-5556');
		expect(t.client.unsubscribed).toEqual(['sub1']);
		expect(t.client.watches.map((w) => w.device)).toEqual(['emulator-5554', 'emulator-5556']);
		expect(t.model.view).toMatchObject({ state: 'starting', device: 'emulator-5556', streamId: '' });
	});

	it('goes idle when the device is cleared', async () => {
		const t = await live();
		t.devices.select('');
		expect(t.model.view.state).toBe('idle');
		expect(t.client.unsubscribed).toEqual(['sub1']);
	});

	// Decision (plan m2p step 5): `detached` is the stream socket going away; one re-watch,
	// and a second before a frame decodes is shown.
	it('re-watches once on detached, and a frame renews the allowance', async () => {
		const t = await live();
		t.client.stopped(0, 'detached');
		expect(t.client.watches).toHaveLength(2);
		expect(t.model.view.state).toBe('starting');
		t.client.answer(1);
		await settle();
		t.client.stopped(1, 'detached');
		expect(t.client.watches).toHaveLength(2);
		expect(t.model.view).toMatchObject({ state: 'stopped', reason: 'detached' });
		// the stopped subscription is ended in the daemon too (ADR-0041 §4)
		expect(t.client.unsubscribed).toEqual(['sub1', 'sub2']);
	});

	it('gives the allowance back once a frame decoded', async () => {
		const t = await live();
		t.client.stopped(0, 'detached');
		t.client.answer(1);
		await settle();
		t.model.frameDecoded();
		t.client.stopped(1, 'detached');
		expect(t.client.watches).toHaveLength(3);
	});

	// Decision: a decoder error closes the socket and watches again once (a rejoin with
	// config); a second error before a frame decodes is `failed`, never an unbounded retry.
	it('re-watches once on a decoder error, and a second gives stopped(failed)', async () => {
		const t = await live();
		t.model.decoderError('bad');
		expect(t.client.unsubscribed).toEqual(['sub1']);
		expect(t.client.watches).toHaveLength(2);
		t.client.answer(1);
		await settle();
		t.model.decoderError('bad again');
		expect(t.client.watches).toHaveLength(2);
		expect(t.model.view).toMatchObject({ state: 'stopped', reason: 'failed' });
		expect(t.model.view.cause).toContain('bad again');
	});

	// Decision: any other stopped shows its reason with Restart, and no re-watch.
	it.each(['deviceGone', 'exited', 'failed'] as const)('shows %s and waits for Restart', async (reason) => {
		const t = await live();
		t.client.stopped(0, reason, 'why');
		expect(t.client.watches).toHaveLength(1);
		expect(t.model.view).toMatchObject({ state: 'stopped', reason, cause: 'why', streamId: '' });
		t.model.restart();
		expect(t.client.watches).toHaveLength(2);
		expect(t.model.view.state).toBe('starting');
	});

	it('shows a stop for the device it is on until the device changes', async () => {
		const t = await live();
		t.client.stopped(0, 'deviceGone');
		t.devices.select('emulator-5554'); // a list refresh, same selection
		expect(t.client.watches).toHaveLength(1);
		t.devices.select('emulator-5556');
		expect(t.client.watches).toHaveLength(2);
	});

	it('ignores the events of a watch it left', async () => {
		const t = await live();
		t.devices.select('emulator-5556');
		t.client.stopped(0, 'deviceGone');
		expect(t.model.view.state).toBe('starting');
	});

	it('ends the stream for the socket refusals only', async () => {
		const t = await live();
		t.model.socketClosed(4410, 1);
		expect(t.model.view.state).toBe('live');
		t.model.socketClosed(4409, 99); // a socket of an older stream
		expect(t.model.view.state).toBe('live');
		t.model.socketClosed(4409, 1);
		expect(t.model.view).toMatchObject({ state: 'stopped', reason: 'failed' });
		expect(t.client.unsubscribed).toEqual(['sub1']);
	});

	// Decision: no WebCodecs video decoder means no stream asked for.
	it('is unsupported without a decoder and never watches', () => {
		const t = make(false);
		t.model.setActive(true);
		expect(t.model.view.state).toBe('unsupported');
		expect(t.client.watches).toHaveLength(0);
	});

	it('watches again when the connection is back', async () => {
		const t = await live();
		t.client.watches[0]!.handlers.onEnd?.({ kind: 'lost', message: 'lost' } as never);
		expect(t.model.view).toMatchObject({ state: 'starting', streamId: '' });
		t.model.onConnection('open');
		expect(t.client.watches).toHaveLength(2);
	});

	// Decision (ADR-0080 §3): the banner follows the daemon's state events (`secure` in the
	// view, which LiveView.svelte shows over the canvas), starts false with each watch and
	// does not outlive it; a state event of a watch that was left is ignored.
	it('follows the secure state of its watch', async () => {
		const t = await live();
		expect(t.model.view.secure).toBe(false);
		t.client.state(0, true);
		expect(t.model.view).toMatchObject({ state: 'live', secure: true });
		t.client.state(0, false);
		expect(t.model.view.secure).toBe(false);

		t.client.state(0, true);
		t.devices.select('emulator-5556');
		expect(t.model.view.secure).toBe(false);
		t.client.state(0, true); // the left watch speaks on
		expect(t.model.view.secure).toBe(false);
	});

	it('notifies once per change of the secure state', async () => {
		const t = await live();
		let n = 0;
		t.model.subscribe(() => n++);
		t.client.state(0, true);
		t.client.state(0, true);
		expect(n).toBe(1);
	});

	it('drops the secure state when the stream stops', async () => {
		const t = await live();
		t.client.state(0, true);
		t.client.stopped(0, 'deviceGone');
		expect(t.model.view).toMatchObject({ state: 'stopped', secure: false });
	});
});
