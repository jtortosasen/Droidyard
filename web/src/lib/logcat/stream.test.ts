import { describe, expect, it } from 'vitest';
import { CapabilityError, ConnectionError } from '../api/client';
import type { Entry } from './entry';
import { LogStream, type StreamStatus } from './stream';
import { FakeClient, line, settle } from './testing';

function make() {
	const client = new FakeClient();
	const entries: Entry[] = [];
	let daemonDropped = 0;
	const statuses: StreamStatus[] = [];
	const stream = new LogStream(client, {
		entries: (e, d) => {
			entries.push(...e);
			daemonDropped += d;
		},
		status: (s) => statuses.push(s),
	});
	const messages = () => entries.map((e) => ('message' in e ? e.message : `[${e.marker}]`));
	return {
		client,
		entries,
		stream,
		statuses,
		messages,
		get daemonDropped() {
			return daemonDropped;
		},
		get state() {
			return statuses.at(-1)?.state;
		},
		get detail() {
			return statuses.at(-1)?.detail;
		},
	};
}

async function started(applicationId = '') {
	const t = make();
	t.stream.open('emulator-5554', applicationId);
	t.client.answer(0);
	await settle();
	return t;
}

describe('LogStream', () => {
	// Decision (ADR-0041 §2): the application filter is a parameter of the stream, the
	// device is the serial, and a fresh stream names neither `since` nor `tail`.
	it('starts the watch with the device and the application', async () => {
		const t = make();
		t.stream.open('emulator-5554', 'dev.app.id');
		expect(t.client.watches[0]!.params).toEqual({ device: 'emulator-5554', applicationId: 'dev.app.id' });
		expect(t.state).toBe('starting');
		t.client.answer(0);
		await settle();
		expect(t.state).toBe('streaming');
		const plain = make();
		plain.stream.open('emulator-5554');
		expect(plain.client.watches[0]!.params).toEqual({ device: 'emulator-5554' });
	});

	it('passes lines on in order, and an event that came before the answer was handled', () => {
		const t = make();
		t.stream.open('d');
		t.client.lines(0, [line(1, 'a'), line(2, 'b')]); // handlers are registered before the answer
		expect(t.messages()).toEqual(['a', 'b']);
	});

	// Decision (ADR-0041 §5): nothing is dropped silently, so a lines event's `dropped`
	// is a marker before the lines it precedes, and counted.
	it('shows the daemon-reported drops as a marker and counts them', async () => {
		const t = await started();
		t.client.lines(0, [line(1, 'a')], 7);
		expect(t.messages()).toEqual(['[dropped]', 'a']);
		expect(t.entries[0]).toMatchObject({ marker: 'dropped' });
		expect((t.entries[0] as { text: string }).text).toContain('7 lines');
		expect(t.daemonDropped).toBe(7);
	});

	// Decision: events.gap is the bus saying events were skipped (ADR-0010).
	it('shows an events.gap as a marker with how many events it stands for', async () => {
		const t = await started();
		t.client.emit(0, 'events.gap', 'gap', { fromSeq: 10, toSeq: 14 });
		expect(t.entries).toHaveLength(1);
		expect(t.entries[0]).toMatchObject({ marker: 'gap' });
		expect((t.entries[0] as { text: string }).text).toContain('5 events');
	});

	// Decision (ADR-0041 §4): `stopped` shows its reason and the client unsubscribes, since
	// the subscription stays open and empty until then; nothing restarts the stream.
	it('on stopped shows the reason, unsubscribes, and does not restart', async () => {
		const t = await started();
		t.client.lines(0, [line(1, 'a')]);
		t.client.emit(0, 'device.logcat', 'stopped', { device: 'd', reason: 'deviceGone', cause: 'adb logcat exited with status 1' });
		expect(t.entries.at(-1)).toMatchObject({ marker: 'stopped' });
		const text = (t.entries.at(-1) as { text: string }).text;
		expect(text).toContain('the device went away');
		expect(text).toContain('adb logcat exited with status 1');
		expect(t.state).toBe('stopped');
		expect(t.client.unsubscribed).toEqual(['sub1']);
		expect(t.client.stopped).toEqual(['sub1']);
		// A late line from that subscription is ignored, and a reconnect does not revive it.
		t.client.lines(0, [line(2, 'late')]);
		t.stream.onConnection('open');
		expect(t.messages()).toEqual(['a', '[stopped]']);
		expect(t.client.watches).toHaveLength(1);
	});

	it('names each reason of stopped', async () => {
		for (const [reason, words] of [
			['exited', 'logcat ended'],
			['replaced', 'replaced this one'],
		] as const) {
			const t = await started();
			t.client.emit(0, 'device.logcat', 'stopped', { device: 'd', reason });
			expect((t.entries.at(-1) as { text: string }).text).toContain(words);
		}
	});

	// Decision (D4): a lost connection ends the session's subscriptions; the stream waits
	// for the connection and starts again from the last line's time.
	it('resumes after a reconnect with since = the last line, once the connection is open', async () => {
		const t = await started('dev.app.id');
		t.client.lines(0, [line(10, 'a'), line(20, 'b')]);
		t.client.watches[0]!.handlers.onEnd?.(new ConnectionError('lost'));
		expect(t.state).toBe('waiting');
		expect(t.client.watches).toHaveLength(1); // not before the connection is back
		t.stream.onConnection('reconnecting');
		expect(t.client.watches).toHaveLength(1);
		t.stream.onConnection('open');
		expect(t.client.watches).toHaveLength(2);
		expect(t.client.watches[1]!.params).toEqual({
			device: 'emulator-5554',
			applicationId: 'dev.app.id',
			since: line(20, '').time,
		});
	});

	// Decision (D4): the daemon sends the lines at or after `since`, so the lines at that
	// instant that are already held are dropped; identical lines are counted, not collapsed.
	it('drops the lines at the resume boundary that are already held', async () => {
		const t = await started();
		const same = (msg: string) => line(20, msg);
		t.client.lines(0, [line(10, 'a'), same('b'), same('b'), same('c')]);
		t.client.watches[0]!.handlers.onEnd?.(new ConnectionError('lost'));
		t.stream.onConnection('open');
		t.client.answer(1);
		await settle();
		t.client.lines(1, [
			line(10, 'a'), // before the boundary: already held
			same('b'),
			same('b'),
			same('c'),
			same('b'), // a third b at that instant: new
			line(30, 'd'),
			line(30, 'd'), // past the boundary nothing is deduplicated
		]);
		expect(t.messages()).toEqual(['a', 'b', 'b', 'c', 'b', 'd', 'd']);
	});

	it('resumes from the newest tail when no line arrived yet', async () => {
		const t = await started();
		t.client.watches[0]!.handlers.onEnd?.(new ConnectionError('lost'));
		t.stream.onConnection('open');
		expect(t.client.watches[1]!.params).toEqual({ device: 'emulator-5554' });
	});

	it('resumes when the connection was lost before the watch was answered', async () => {
		const t = make();
		t.stream.open('d');
		t.client.watches[0]!.reject(new ConnectionError('lost'));
		await settle();
		expect(t.state).toBe('waiting');
		t.stream.onConnection('open');
		expect(t.client.watches).toHaveLength(2);
	});

	it('ends for good when the daemon refuses the token, and does not restart', async () => {
		const t = await started();
		t.client.watches[0]!.handlers.onEnd?.(new ConnectionError('unauthorized'));
		expect(t.state).toBe('ended');
		t.stream.onConnection('open');
		expect(t.client.watches).toHaveLength(1);
	});

	// Decision: a stream stopped or replaced while its start was on the wire must not
	// survive it: its late answer is unsubscribed at once and its events are ignored.
	it('unsubscribes a start that is answered after the stream was stopped', async () => {
		const t = make();
		t.stream.open('d');
		t.stream.stop();
		t.client.answer(0);
		await settle();
		expect(t.client.unsubscribed).toEqual(['sub1']);
		expect(t.state).toBe('idle');
		t.client.lines(0, [line(1, 'late')]);
		expect(t.messages()).toEqual([]);
	});

	// Decision: the application filter restarts the stream (ADR-0041 §2): the old
	// subscription is ended, the new one is fresh (no `since`), and the old one's events
	// no longer reach the panel.
	it('replaces the running stream when opened again, fresh', async () => {
		const t = await started();
		t.client.lines(0, [line(10, 'a')]);
		t.stream.open('emulator-5554', 'dev.other.app');
		expect(t.client.unsubscribed).toEqual(['sub1']);
		expect(t.client.watches[1]!.params).toEqual({ device: 'emulator-5554', applicationId: 'dev.other.app' });
		t.client.lines(0, [line(11, 'old stream')]);
		t.client.answer(1);
		await settle();
		t.client.lines(1, [line(5, 'fresh')]); // earlier than the old stream's last line: not deduplicated
		expect(t.messages()).toEqual(['a', 'fresh']);
	});

	it('shows a refused start by its code', async () => {
		const t = make();
		t.stream.open('d', 'dev.app.id');
		t.client.watches[0]!.reject(
			new CapabilityError({ code: 'DEVICE_LOGS_FAILED', message: 'not installed', nextStep: 'Install it.' }, false),
		);
		await settle();
		expect(t.state).toBe('error');
		expect(t.detail).toContain('DEVICE_LOGS_FAILED');
		expect(t.detail).toContain('Install it.');
	});

	it('restarts a stopped stream from where it left off on resume()', async () => {
		const t = await started();
		t.client.lines(0, [line(10, 'a')]);
		t.client.emit(0, 'device.logcat', 'stopped', { device: 'd', reason: 'exited' });
		t.stream.resume();
		expect(t.client.watches[1]!.params).toMatchObject({ since: line(10, '').time });
	});
});
