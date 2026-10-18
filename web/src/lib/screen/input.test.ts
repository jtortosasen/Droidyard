import { describe, expect, it } from 'vitest';
import { CapabilityError } from '../api/client';
import {
	GestureRecorder,
	InputSender,
	MAX_POINTS,
	NAV_KEYS,
	toFrame,
	type Box,
	type Gesture,
	type InputClient,
} from './input';

// A 1000x500 canvas box at (100, 50) showing a 100x200 frame: contain makes it 250 px
// wide, centred, with 375 px bars at each side.
const box: Box = { left: 100, top: 50, width: 1000, height: 500 };
const frame = { width: 100, height: 200 };
const inFrameX = (u: number) => 100 + 375 + u * 250;
const inFrameY = (v: number) => 50 + v * 500;

describe('the pointer to the frame', () => {
	// Decision: coordinates are the frame's (0..1), measured inside the letterboxed drawing;
	// the bars are not the frame. A mapping against the whole box would give other numbers.
	it('maps inside the drawn frame and ignores the bars', () => {
		expect(toFrame(inFrameX(0.5), inFrameY(0.25), box, frame)).toEqual({ x: 0.5, y: 0.25 });
		expect(toFrame(inFrameX(0), inFrameY(1), box, frame)).toEqual({ x: 0, y: 1 });
		expect(toFrame(300, 300, box, frame)).toBeNull(); // on the left bar
		expect(toFrame(1000, 300, box, frame)).toBeNull(); // on the right bar
		expect(toFrame(inFrameX(0.5), 40, box, frame)).toBeNull(); // above the box
	});

	// Decision: a drag that leaves the frame is held at its edge, not dropped.
	it('clamps a drag that leaves the frame', () => {
		expect(toFrame(1000, 700, box, frame, true)).toEqual({ x: 1, y: 1 });
	});

	// Decision: a gesture that starts on a bar is not a gesture.
	it('records nothing for a pointer that goes down on a bar', () => {
		expect(GestureRecorder.start(300, 300, 0, box, frame)).toBeNull();
	});
});

const X = inFrameX(0.5);
const Y = inFrameY(0.5);

function run(points: [number, number, number][]): Gesture {
	const [first, ...rest] = points;
	const r = GestureRecorder.start(first![0], first![1], first![2], box, frame)!;
	for (const p of rest.slice(0, -1)) r.move(p[0], p[1], p[2]);
	const last = rest[rest.length - 1] ?? first!;
	return r.end(last[0], last[1], last[2]);
}

describe('the gesture', () => {
	// Decision: under 10 px and under 300 ms is a tap, with no duration.
	it('is a tap when short and still', () => {
		const g = run([[X, Y, 1000], [X + 3, Y + 2, 1100], [X + 3, Y + 2, 1150]]);
		expect(g).toMatchObject({ kind: 'tap' });
		expect(g.kind === 'tap' && g.durationMs).toBeUndefined();
		expect(run([[X, Y, 0], [X, Y, 299]]).kind).toBe('tap');
	});

	// Decision: held in place for 300 ms or more is a tap with its duration, at most 2000.
	it('is a long tap when held in place', () => {
		expect(run([[X, Y, 0], [X, Y, 800]])).toMatchObject({ kind: 'tap', durationMs: 800 });
		expect(run([[X, Y, 0], [X, Y, 9000]])).toMatchObject({ kind: 'tap', durationMs: 2000 });
	});

	// Decision: 10 px of travel at any moment makes it a swipe, even if the pointer comes back.
	it('is a swipe once it has moved 10 px, even if it comes back', () => {
		expect(run([[X, Y, 0], [X + 12, Y, 50], [X, Y, 100]]).kind).toBe('swipe');
	});

	// Decision: the swipe is sampled at 16 ms or more, t is from the first point, strictly
	// increasing, first and last always kept.
	it('decimates to 16 ms and keeps the first and the last', () => {
		const pts: [number, number, number][] = [];
		for (let t = 0; t <= 200; t += 4) pts.push([X, Y - t, 5000 + t]);
		const g = run(pts);
		if (g.kind !== 'swipe') throw new Error('swipe expected');
		expect(g.path[0]!.t).toBe(0);
		expect(g.path[g.path.length - 1]!.t).toBe(200);
		for (let i = 1; i < g.path.length - 1; i++) expect(g.path[i]!.t - g.path[i - 1]!.t).toBeGreaterThanOrEqual(16);
		expect(g.path.length).toBeLessThan(20);
		for (let i = 1; i < g.path.length; i++) expect(g.path[i]!.t).toBeGreaterThan(g.path[i - 1]!.t);
	});

	// Decision: at most 64 points and 5 s, whatever the pointer did.
	it('caps the path at 64 points and 5 seconds', () => {
		const pts: [number, number, number][] = [];
		for (let t = 0; t <= 8000; t += 20) pts.push([X + (t % 50), inFrameY(0.9) - t / 20, t]);
		const g = run(pts);
		if (g.kind !== 'swipe') throw new Error('swipe expected');
		expect(g.path.length).toBeLessThanOrEqual(MAX_POINTS);
		expect(g.path[g.path.length - 1]!.t).toBeLessThanOrEqual(5000);
		expect(g.path[0]!.t).toBe(0);
		for (let i = 1; i < g.path.length; i++) expect(g.path[i]!.t).toBeGreaterThan(g.path[i - 1]!.t);
	});

	// Decision: a swipe is at least two points with strictly increasing t, even when every
	// event carries the same timestamp.
	it('gives two points with increasing t for a swipe with no elapsed time', () => {
		const g = run([[X, Y, 10], [X + 50, Y, 10]]);
		if (g.kind !== 'swipe') throw new Error('swipe expected');
		expect(g.path.map((p) => p.t)).toEqual([0, 1]);
	});
});

function fake(over: Record<string, () => unknown> = {}) {
	const calls: { name: string; params: unknown }[] = [];
	const client = {
		call: (name: string, params: unknown) => {
			calls.push({ name, params });
			const h = over[name];
			return Promise.resolve(h ? h() : { delivered: true });
		},
	} as unknown as InputClient;
	return { calls, client };
}

describe('the sender', () => {
	// Decision (ADR-0079): Back, Home and Recents are the keys back, home and appSwitch.
	it('presses back, home and appSwitch for the three buttons', async () => {
		const f = fake();
		const s = new InputSender(f.client, () => 'emulator-5554');
		for (const k of NAV_KEYS) expect(await s.key(k.key)).toBeNull();
		expect(f.calls).toEqual([
			{ name: 'device.pressKey', params: { device: 'emulator-5554', key: 'back' } },
			{ name: 'device.pressKey', params: { device: 'emulator-5554', key: 'home' } },
			{ name: 'device.pressKey', params: { device: 'emulator-5554', key: 'appSwitch' } },
		]);
		expect(NAV_KEYS.map((k) => k.label)).toEqual(['Back', 'Home', 'Recents']);
	});

	// Decision: the text goes to the focused field, so the call carries no target.
	it('types text with no target', async () => {
		const f = fake();
		await new InputSender(f.client, () => 'd').text('ñandú');
		expect(f.calls).toEqual([{ name: 'device.inputText', params: { device: 'd', text: 'ñandú' } }]);
	});

	it('sends a tap, a long tap and a swipe to their capabilities', async () => {
		const f = fake();
		const s = new InputSender(f.client, () => 'd');
		await s.gesture({ kind: 'tap', x: 0.1, y: 0.2 });
		await s.gesture({ kind: 'tap', x: 0.1, y: 0.2, durationMs: 500 });
		await s.gesture({ kind: 'swipe', path: [{ x: 0, y: 0, t: 0 }, { x: 1, y: 1, t: 9 }] });
		expect(f.calls.map((c) => c.name)).toEqual(['device.tap', 'device.tap', 'device.swipe']);
		expect(f.calls[0]!.params).toEqual({ device: 'd', x: 0.1, y: 0.2 });
		expect(f.calls[1]!.params).toEqual({ device: 'd', x: 0.1, y: 0.2, durationMs: 500 });
	});

	// Decision: a failure shows as text (code and message); a partial delivery shows its warnings.
	it('turns a refusal and a warning into text', async () => {
		const refusing = {
			call: () => Promise.reject(new CapabilityError({ code: 'DEVICE_NO_STREAM', message: 'no live view' }, false)),
		} as unknown as InputClient;
		expect(await new InputSender(refusing, () => 'd').gesture({ kind: 'tap', x: 0, y: 0 })).toBe(
			'DEVICE_NO_STREAM: no live view',
		);
		const warn = fake({ 'device.pressKey': () => ({ delivered: true, warnings: ['the view failed'] }) });
		expect(await new InputSender(warn.client, () => 'd').key('home')).toBe('the view failed');
	});

	it('sends nothing without a device', async () => {
		const f = fake();
		expect(await new InputSender(f.client, () => '').key('back')).toBe('Select a device first.');
		expect(f.calls).toEqual([]);
	});
});
