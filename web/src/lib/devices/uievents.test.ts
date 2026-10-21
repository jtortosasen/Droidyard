import { afterEach, describe, expect, it, vi } from 'vitest';
import { render } from 'svelte/server';
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError, ConnectionError, type BusEvent, type SubscribeHandlers, type Subscription } from '../api/client';
import UiEvents from './UiEvents.svelte';
import { clockOf, MAX_ROWS, UiEventsModel, type UiEventsClient } from './uievents';

type Answer = Subscription & { readonly result: Result<'device.watchUiEvents'> };

interface WireEvent {
	time?: string;
	type?: string;
	package?: string;
	class?: string;
	window?: number;
	count?: number;
	text?: string;
}

class FakeClient implements UiEventsClient {
	watches: { device: string; handlers: SubscribeHandlers; resolve: (s: Answer) => void; reject: (e: unknown) => void }[] = [];
	unsubscribed: string[] = [];
	stoppedLocally: string[] = [];

	subscribe(_n: 'device.watchUiEvents', params: Params<'device.watchUiEvents'>, handlers: SubscribeHandlers): Promise<Answer> {
		return new Promise((resolve, reject) => this.watches.push({ device: params.device, handlers, resolve, reject }));
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
			result: { subscriptionId: id, device: w.device },
			stop: () => this.stoppedLocally.push(id),
		});
	}
	#emit(i: number, type: string, payload: unknown): void {
		const event: BusEvent = { epoch: 'e', seq: 1, time: 't', workspace: null, topic: 'device.uiEvents', type, payload };
		this.watches[i]!.handlers.onEvent({ subscriptionId: `sub${i + 1}`, event });
	}
	events(i: number, events: WireEvent[], gap?: { reason: string; dropped?: number }): void {
		this.#emit(i, 'events', {
			device: this.watches[i]!.device,
			events: events.map((e) => ({ time: '2026-10-21T01:02:03.456Z', type: 'click', ...e })),
			...(gap ? { gap } : {}),
			untrusted: true,
		});
	}
	stopped(i: number, reason: string, cause?: string): void {
		this.#emit(i, 'stopped', { device: this.watches[i]!.device, reason, ...(cause ? { cause } : {}) });
	}
	lost(i: number): void {
		this.watches[i]!.handlers.onEnd?.(new ConnectionError('lost'));
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

function make() {
	const client = new FakeClient();
	const devices = new FakeDevices();
	const model = new UiEventsModel(client, devices);
	return { client, devices, model };
}

async function watching() {
	const t = make();
	t.model.setActive(true);
	t.client.answer(0);
	await settle();
	return t;
}

afterEach(() => vi.unstubAllGlobals());

describe('UiEventsModel', () => {
	// Decision (ADR-0112 Decision 4): nothing is watched until the panel is shown, and then it is
	// the toolbar's device. Seen failing with the model watching at construction.
	it('is idle until the panel is shown, then watches the toolbar device', async () => {
		const t = make();
		expect(t.model.view.state).toBe('idle');
		expect(t.client.watches).toHaveLength(0);
		t.model.setActive(true);
		expect(t.client.watches.map((w) => w.device)).toEqual(['emulator-5554']);
		expect(t.model.view.state).toBe('watching');
	});

	// Decision (ADR-0112 Decision 4: hidden panels watch nothing): hiding the panel unsubscribes
	// the stream in the daemon, and showing it again watches again. Seen failing without the
	// visibility test (setActive(false) left the stream running: no unsubscribe).
	it('unsubscribes when hidden and watches again when shown', async () => {
		const t = await watching();
		t.model.setActive(false);
		expect(t.client.unsubscribed).toEqual(['sub1']);
		expect(t.model.view.state).toBe('idle');
		// A late event of the ended stream is ignored.
		t.client.events(0, [{ window: 1 }]);
		expect(t.model.view.rows).toHaveLength(0);
		t.model.setActive(true);
		expect(t.client.watches).toHaveLength(2);
		expect(t.model.view.state).toBe('watching');
	});

	// Decision: a hide while the answer is still on its way does not leave a stream behind.
	it('ends a stream whose answer arrives after the panel was hidden', async () => {
		const t = make();
		t.model.setActive(true);
		t.model.setActive(false);
		t.client.answer(0);
		await settle();
		expect(t.client.unsubscribed).toEqual(['sub1']);
	});

	// Decision (ADR-0112 Decision 4: the toolbar's device): another device ends the stream of the
	// first and watches the second, with a list of its own; no device watches nothing.
	it('watches the new device when the toolbar selects another, with its own list', async () => {
		const t = await watching();
		t.client.events(0, [{ window: 1 }]);
		expect(t.model.view.rows).toHaveLength(1);
		t.devices.select('emulator-5556');
		expect(t.client.unsubscribed).toEqual(['sub1']);
		expect(t.client.watches.map((w) => w.device)).toEqual(['emulator-5554', 'emulator-5556']);
		expect(t.model.view.rows).toHaveLength(0);
		expect(t.model.view.device).toBe('emulator-5556');
		t.devices.select('');
		expect(t.model.view.state).toBe('idle');
	});

	// Decision (ADR-0112 Decision 4: newest first): events arrive oldest first in a push and
	// across pushes; the list shows the newest on top.
	it('lists the newest event first', async () => {
		const t = await watching();
		t.client.events(0, [{ window: 1, time: '2026-10-21T01:00:01.000Z' }, { window: 2, time: '2026-10-21T01:00:02.000Z' }]);
		t.client.events(0, [{ window: 3, time: '2026-10-21T01:00:03.000Z' }]);
		const rows = t.model.view.rows.flatMap((r) => (r.kind === 'event' ? [r.window] : []));
		expect(rows).toEqual(['3', '2', '1']);
		const first = t.model.view.rows[0];
		expect(first?.kind === 'event' && first.clock).toBe('01:00:03.000');
	});

	// Decision (ADR-0112 Decision 4: at most 500 rows): the oldest go first. Seen failing with the
	// cap removed (600 rows).
	it('keeps at most 500 rows, dropping the oldest', async () => {
		const t = await watching();
		for (let b = 0; b < 3; b++) t.client.events(0, Array.from({ length: 200 }, (_, i) => ({ window: b * 200 + i })));
		const rows = t.model.view.rows;
		expect(rows).toHaveLength(MAX_ROWS);
		expect(rows[0]?.kind === 'event' && rows[0].window).toBe('599');
		expect(rows.at(-1)?.kind === 'event' && rows.at(-1)).toMatchObject({ window: '100' });
	});

	// Decision (ADR-0111 Decision 6; ADR-0112 Decision 4: a gap shown as a row): a gap is a row
	// of its own, before (below) the events of its push, with the count of an overflow; a gap with
	// no events is a row too. Seen failing with the gap dropped.
	it('shows a gap as a row before the events that follow it', async () => {
		const t = await watching();
		t.client.events(0, [{ window: 1 }, { window: 2 }], { reason: 'overflow', dropped: 7 });
		t.client.events(0, [], { reason: 'readerRestarted' });
		const kinds = t.model.view.rows.map((r) => (r.kind === 'gap' ? `gap:${r.reason}:${r.dropped}` : `event:${r.window}`));
		expect(kinds).toEqual(['gap:readerRestarted:0', 'event:2', 'event:1', 'gap:overflow:7']);
	});

	// Decision (ADR-0112 Decision 4: `stopped` with a Restart): the reason and cause are shown, the
	// rows stay, the stream is released and Restart watches again with a gap marking the time
	// between. Seen failing with Restart doing nothing.
	it('stops on a stopped event and restarts', async () => {
		const t = await watching();
		t.client.events(0, [{ window: 1 }]);
		t.client.stopped(0, 'failed', 'the reader could not be started');
		expect(t.model.view).toMatchObject({ state: 'stopped', reason: 'failed', cause: 'the reader could not be started' });
		expect(t.model.view.rows).toHaveLength(1);
		expect(t.client.unsubscribed).toEqual(['sub1']);
		t.model.restart();
		expect(t.client.watches).toHaveLength(2);
		expect(t.model.view.state).toBe('watching');
		expect(t.model.view.rows.map((r) => r.kind === 'gap' && r.reason)).toEqual(['interrupted', false]);
	});

	it('reads an unknown stop reason as failed', async () => {
		const t = await watching();
		t.client.stopped(0, 'surprise');
		expect(t.model.view).toMatchObject({ state: 'stopped', reason: 'failed' });
	});

	// Decision: a stopped stream asks the daemon for nothing; a device change leaves a stop behind.
	it('leaves a stop when the toolbar selects another device', async () => {
		const t = await watching();
		t.client.stopped(0, 'deviceGone', 'device.list no longer holds it');
		t.devices.select('emulator-5556');
		expect(t.client.watches.map((w) => w.device)).toEqual(['emulator-5554', 'emulator-5556']);
		expect(t.model.view.state).toBe('watching');
	});

	// Decision: a call the daemon refuses is a stop with its code and message, as text.
	it('stops with the daemon refusal when the call fails', async () => {
		const t = make();
		t.model.setActive(true);
		t.client.watches[0]!.reject(new CapabilityError({ code: 'DEVICE_NOT_FOUND', message: 'no such device' }, false));
		await settle();
		expect(t.model.view).toMatchObject({ state: 'stopped', reason: 'failed' });
		expect(t.model.view.cause).toContain('DEVICE_NOT_FOUND: no such device');
	});

	// Decision: a lost connection watches again when the connection is back, with a gap row.
	it('watches again after the connection is lost and back', async () => {
		const t = await watching();
		t.client.events(0, [{ window: 1 }]);
		t.client.lost(0);
		expect(t.client.watches).toHaveLength(1);
		t.model.onConnection('open');
		expect(t.client.watches).toHaveLength(2);
		expect(t.model.view.rows[0]).toMatchObject({ kind: 'gap', reason: 'interrupted' });
		// Nothing of the first stream is heard after it.
		t.client.events(0, [{ window: 9 }]);
		expect(t.model.view.rows).toHaveLength(2);
	});

	it('stops when the daemon refuses the token', async () => {
		const t = await watching();
		t.model.onConnection('unauthorized');
		expect(t.model.view).toMatchObject({ state: 'stopped', reason: 'failed', cause: 'the daemon refused the token' });
		expect(t.client.unsubscribed).toEqual(['sub1']);
	});

	it('ends the stream when disposed', async () => {
		const t = await watching();
		t.model.dispose();
		expect(t.client.unsubscribed).toEqual(['sub1']);
	});

	// Decision: a view handed out keeps its rows when more arrive (the component compares them).
	it('never changes the rows of a view it handed out', async () => {
		const t = await watching();
		t.client.events(0, [{ window: 1 }]);
		const before = t.model.view.rows;
		t.client.events(0, [{ window: 2 }]);
		expect(before).toHaveLength(1);
		expect(t.model.view.rows).toHaveLength(2);
	});

	it('clears the list', async () => {
		const t = await watching();
		t.client.events(0, [{ window: 1 }]);
		t.model.clear();
		expect(t.model.view.rows).toHaveLength(0);
		expect(t.model.view.state).toBe('watching');
	});

	it('shows the clock of a time, or the time itself when it is not one', () => {
		expect(clockOf('2026-10-21T01:02:03.456Z')).toBe('01:02:03.456');
		expect(clockOf('later')).toBe('later');
	});
});

describe('UiEvents panel', () => {
	// Decision (ADR-0015 §1; http-websocket.md rule 8): device text reaches the page as a text node
	// only. A toast whose text is markup is rendered escaped, a class and a package likewise, and a
	// bidirectional override is shown as its code point. Rendered on the server side, where the
	// markup a component writes is the string a test can read. Seen failing with the toast text
	// written as raw markup (the tag came out as a tag).
	it('renders device text as text, never as markup', async () => {
		vi.stubGlobal('document', { hidden: false });
		const t = await watching();
		t.client.events(0, [
			{ type: 'toast', text: '<img src=x onerror=alert(1)>', package: '<b>pkg</b>', class: 'a.b.<script>Evil' },
			{ type: 'toast', text: 'abc‮def' },
		]);
		const { body } = render(UiEvents, { props: { model: t.model, connection: 'open', visible: true } });
		expect(body).not.toContain('<img');
		expect(body).not.toContain('<b>pkg');
		expect(body).not.toContain('<script>');
		// Svelte escapes `<` and `&`, which is all a text node needs.
		expect(body).toContain('&lt;img src=x onerror=alert(1)>');
		expect(body).toContain('&lt;b>pkg&lt;/b>');
		expect(body).not.toContain('‮');
		expect(body).toContain('abc\\u{202e}def');
	});

	it('says what stopped the stream, with a Restart', async () => {
		vi.stubGlobal('document', { hidden: false });
		const t = await watching();
		t.client.stopped(0, 'deviceGone', 'device.list no longer holds the serial');
		const { body } = render(UiEvents, { props: { model: t.model, connection: 'open', visible: true } });
		expect(body).toContain('The device went away.');
		expect(body).toContain('device.list no longer holds the serial');
		expect(body).toContain('Restart');
	});
});
