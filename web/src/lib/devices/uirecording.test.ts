import { afterEach, describe, expect, it, vi } from 'vitest';
import { render } from 'svelte/server';
import type { Params, Result } from '../api/capabilities.gen';
import { ConnectionError, type BusEvent, type SubscribeHandlers, type Subscription } from '../api/client';
import UiRecordingPanel from './UiRecording.svelte';
import { canDownload, clockText, elapsedSeconds, UiRecordingModel, type UiRecording, type UiRecordingClient } from './uirecording';

type Answer = Subscription & { readonly result: Result<'device.watchUiRecordings'> };

const A = '20261021T010100Z-aaaaaaa1';
const B = '20261021T010200Z-aaaaaaa2';
const C = '20261021T010300Z-aaaaaaa3';

function rec(id: string, over: Partial<UiRecording> = {}): UiRecording {
	const minute = id.slice(11, 13);
	return {
		id,
		device: 'emulator-5554',
		invisible: false,
		maxSeconds: 900,
		startedBy: { client: 'web' },
		state: 'ended',
		startedAt: `2026-10-21T01:${minute}:00Z`,
		endedAt: `2026-10-21T01:${minute}:30Z`,
		endReason: 'stopped',
		dumps: 3,
		unchanged: 0,
		missed: 0,
		lockWaits: 0,
		events: 7,
		gaps: 0,
		bytes: 100,
		timeline: { ref: `droidyard://artifact/r.${id}`, mediaType: 'application/x-ndjson', bytes: 100, durability: 'durable', expiresAt: '', untrusted: true },
		...over,
	};
}

function running(id: string, device = 'emulator-5554'): UiRecording {
	const r = rec(id, { state: 'recording', device });
	delete r.endedAt;
	delete r.endReason;
	return r;
}

class FakeClient implements UiRecordingClient {
	watches: { handlers: SubscribeHandlers; resolve: (s: Answer) => void; reject: (e: unknown) => void }[] = [];
	calls: { name: string; params: unknown }[] = [];
	stoppedLocally: string[] = [];
	stopAnswer: Result<'device.stopUiRecording'> = { stopped: true };

	subscribe(_n: 'device.watchUiRecordings', _p: Params<'device.watchUiRecordings'>, handlers: SubscribeHandlers): Promise<Answer> {
		return new Promise((resolve, reject) => this.watches.push({ handlers, resolve, reject }));
	}
	call(name: string, params: unknown): Promise<never> {
		this.calls.push({ name, params });
		if (name === 'device.recordUi') return Promise.resolve(running('20261021T010900Z-aaaaaaa9', (params as { device: string }).device) as never);
		if (name === 'device.stopUiRecording') return Promise.resolve(this.stopAnswer as never);
		if (name === 'device.deleteUiRecording') return Promise.resolve({ deleted: true } as never);
		return Promise.resolve({} as never);
	}
	answer(i: number, recordings: UiRecording[]): void {
		const id = `sub${i + 1}`;
		this.watches[i]!.resolve({ subscriptionId: id, result: { subscriptionId: id, recordings }, stop: () => this.stoppedLocally.push(id) } as Answer);
	}
	#emit(i: number, type: string, payload: unknown): void {
		const event: BusEvent = { epoch: 'e', seq: 1, time: 't', workspace: null, topic: 'device.uiRecording', type, payload };
		this.watches[i]!.handlers.onEvent({ subscriptionId: `sub${i + 1}`, event });
	}
	state(i: number, recording: UiRecording): void {
		this.#emit(i, 'state', { recording });
	}
	deleted(i: number, id: string): void {
		this.#emit(i, 'deleted', { id });
	}
	lost(i: number): void {
		this.watches[i]!.handlers.onEnd?.(new ConnectionError('lost'));
	}
	unsubscribed(): string[] {
		return this.calls.filter((c) => c.name === 'events.unsubscribe').map((c) => (c.params as { subscriptionId: string }).subscriptionId);
	}
	named(name: string) {
		return this.calls.filter((c) => c.name === name);
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
	for (let i = 0; i < 8; i++) await Promise.resolve();
};

function fakeResponse(status: number, body: string) {
	return Promise.resolve({ ok: status >= 200 && status < 300, status, blob: () => Promise.resolve(new Blob([body])), text: () => Promise.resolve(body) });
}

function make(respond: () => ReturnType<typeof fakeResponse> = () => fakeResponse(200, 'line1\nline2\n')) {
	const client = new FakeClient();
	const devices = new FakeDevices();
	const saved: { blob: Blob; name: string }[] = [];
	const fetched: { url: string; headers: Record<string, string> }[] = [];
	const model = new UiRecordingModel(client, devices, {
		token: 'tok123',
		fetch: (url, init) => {
			fetched.push({ url, headers: init.headers });
			return respond();
		},
		save: (blob, name) => saved.push({ blob, name }),
	});
	return { client, devices, model, saved, fetched };
}

async function watching(recordings: UiRecording[], respond?: () => ReturnType<typeof fakeResponse>) {
	const t = make(respond);
	t.model.setActive(true);
	t.client.answer(0, recordings);
	await settle();
	return t;
}

const draw = (t: { model: UiRecordingModel }) => {
	vi.stubGlobal('document', { hidden: false });
	return render(UiRecordingPanel, { props: { model: t.model, connection: 'open', visible: true } }).body;
};

afterEach(() => vi.unstubAllGlobals());

describe('UiRecordingModel', () => {
	// Decision (ADR-0113 §6: Stop for the toolbar's device): a recording of another device does not
	// turn Record into Stop. Seen failing with `running` taking any device's recording.
	it('shows Stop for the toolbar device running recording and Record when only another device records', async () => {
		const t = await watching([running(B, 'emulator-5556')]);
		expect(t.model.view.running).toBeNull();
		expect(draw(t)).toContain('Record</button>');
		expect(draw(t)).not.toContain('Stop</button>');
		t.devices.select('emulator-5556');
		expect(t.model.view.running?.id).toBe(B);
		expect(draw(t)).toContain('Stop</button>');
		expect(draw(t)).not.toContain('Record</button>');
	});

	// Decision (plan step 8: Download once the timeline is whole): none while it grows, and none for a
	// remote recording not yet copied. Seen failing with canDownload true for every row.
	it('offers Download only to an ended recording, and for a remote one only when copied', () => {
		expect(canDownload(running(A))).toBe(false);
		expect(canDownload(rec(A))).toBe(true);
		expect(canDownload(rec(A, { remote: true }))).toBe(false);
		expect(canDownload(rec(A, { remote: true, copied: true }))).toBe(true);
	});

	it('draws no Download for a running recording and fetches nothing for it', async () => {
		const t = await watching([running(A)]);
		await t.model.downloadRecording(A);
		expect(t.fetched).toHaveLength(0);
		expect(draw(t)).not.toContain('Download');
	});

	// Decision: a `deleted` event removes the row (another client, or the retention). Seen failing
	// with the event ignored.
	it('removes a row on a deleted event', async () => {
		const t = await watching([rec(A), rec(B)]);
		t.client.deleted(0, A);
		expect(t.model.view.recordings.map((r) => r.id)).toEqual([B]);
	});

	it('updates a row on a state event and puts a new recording first', async () => {
		const t = await watching([running(A)]);
		t.client.state(0, rec(A, { dumps: 9 }));
		t.client.state(0, rec(C));
		expect(t.model.view.recordings.map((r) => r.id)).toEqual([C, A]);
		expect(t.model.view.recordings[1]!.dumps).toBe(9);
		expect(t.model.view.running).toBeNull();
	});

	// Decision: the snapshot is taken after the subscription opened, so an event that arrived before
	// it must not undo it. Seen failing with buffered events applied unconditionally.
	it('does not let an event older than the snapshot undo it', async () => {
		const t = make();
		t.model.setActive(true);
		t.client.state(0, running(A));
		t.client.answer(0, [rec(A)]);
		await settle();
		expect(t.model.view.recordings[0]!.state).toBe('ended');
	});

	// Decision: Download sends the per-launch token and saves the response's bytes as <id>.jsonl.
	it('fetches the timeline with the token and saves the response bytes', async () => {
		const t = await watching([rec(A)]);
		await t.model.downloadRecording(A);
		expect(t.fetched).toEqual([{ url: `/v1/artifacts/r.${A}`, headers: { Authorization: 'Bearer tok123' } }]);
		expect(t.saved.map((s) => s.name)).toEqual([`${A}.jsonl`]);
		expect(await t.saved[0]!.blob.text()).toBe('line1\nline2\n');
		expect(t.model.view.error).toBe('');
	});

	it('shows the daemon words when the download is refused, and saves nothing', async () => {
		const t = await watching([rec(A)], () => fakeResponse(400, JSON.stringify({ code: 'INVALID_PARAMS', message: 'the artifact is still being written' })));
		await t.model.downloadRecording(A);
		expect(t.saved).toHaveLength(0);
		expect(t.model.view.error).toBe('INVALID_PARAMS: the artifact is still being written');
	});

	// Decision (no browser dialogs): Delete is two steps, and nothing is called before the second.
	it('asks before deleting, and keeping deletes nothing', async () => {
		const t = await watching([rec(A)]);
		t.model.askDelete(A);
		expect(t.model.view.confirming).toBe(A);
		expect(t.client.named('device.deleteUiRecording')).toHaveLength(0);
		expect(draw(t)).toContain('Delete this recording?');
		t.model.cancelDelete();
		expect(t.model.view.confirming).toBeNull();
		expect(t.client.named('device.deleteUiRecording')).toHaveLength(0);
		t.model.askDelete(A);
		await t.model.confirmDelete();
		expect(t.client.named('device.deleteUiRecording')).toEqual([{ name: 'device.deleteUiRecording', params: { id: A } }]);
		expect(t.model.view.recordings).toHaveLength(0);
	});

	it('Record and Stop call the toolbar device', async () => {
		const t = await watching([]);
		await t.model.record();
		expect(t.client.named('device.recordUi')[0]!.params).toEqual({ device: 'emulator-5554' });
		expect(t.model.view.running).not.toBeNull();
		t.client.stopAnswer = { stopped: true, recording: rec('20261021T010900Z-aaaaaaa9') };
		await t.model.stop();
		expect(t.client.named('device.stopUiRecording')[0]!.params).toEqual({ device: 'emulator-5554' });
		expect(t.model.view.running).toBeNull();
	});

	// Decision (hidden panels watch nothing): hiding unsubscribes; showing watches again.
	it('unsubscribes when hidden and watches again when shown', async () => {
		const t = await watching([]);
		t.model.setActive(false);
		expect(t.client.unsubscribed()).toEqual(['sub1']);
		t.client.state(0, rec(A));
		expect(t.model.view.recordings).toHaveLength(0);
		t.model.setActive(true);
		expect(t.client.watches).toHaveLength(2);
	});

	it('ends a subscription whose answer arrives after the panel was hidden', async () => {
		const t = make();
		t.model.setActive(true);
		t.model.setActive(false);
		t.client.answer(0, []);
		await settle();
		expect(t.client.unsubscribed()).toEqual(['sub1']);
	});

	it('watches again when the connection comes back after a loss', async () => {
		const t = await watching([]);
		t.client.lost(0);
		t.model.onConnection('open');
		expect(t.client.watches).toHaveLength(2);
	});

	it('formats the elapsed time from the daemon times', () => {
		expect(elapsedSeconds(rec(A), 0)).toBe(30);
		expect(elapsedSeconds(running(A), Date.parse('2026-10-21T01:01:05Z'))).toBe(5);
		expect(clockText(65)).toBe('1:05');
		expect(clockText(3725)).toBe('1:02:05');
	});

	it('renders the daemon cause as text, never markup', async () => {
		const t = await watching([rec(A, { endReason: 'failed', cause: '<img src=x onerror=alert(1)>' })]);
		const body = draw(t);
		expect(body).toContain('&lt;img src=x onerror=alert(1)>');
		expect(body).not.toContain('<img');
	});
});
