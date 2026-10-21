import { describe, expect, it } from 'vitest';
import { CapabilityError } from '../api/client';
import { hex } from './golden';
import { encodeKey } from './control';
import { gated, InputSender, NAV_KEYS, PointerInput, toFrame, WheelAccumulator, wheelNotches, type Box, type InputClient } from './input';

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
});

function pointer() {
	const sent: string[] = [];
	return { sent, p: new PointerInput((b) => sent.push(hex(b))) };
}
const at = (u: number, v: number): [number, number] => [inFrameX(u), inFrameY(v)];

describe('the pointer input', () => {
	// Decision (ADR-0117 §3): down, every move and the up reach the wire in order, each as
	// its own frame; nothing is sampled away or merged into a swipe.
	it('sends the down, every move and the up, in order', () => {
		const { sent, p } = pointer();
		expect(p.down(1, ...at(0.5, 0.5), box, frame)).toBe(true);
		p.move(1, ...at(0.5, 0.4), box, frame);
		p.move(1, ...at(0.5, 0.25), box, frame);
		p.move(1, ...at(0.5, 0.25), box, frame); // the same place again is still a move
		p.up(1, ...at(0.5, 0.2), box, frame);
		expect(sent).toEqual([
			'010080008000', // down in the middle
			'010280006666', // move to y 0.4
			'010280004000', // move to y 0.25
			'010280004000',
			'010180003333', // up at y 0.2
		]);
	});

	// Decision: a drag that leaves the frame is held at its edge on the wire.
	it('holds a move that leaves the frame at the edge', () => {
		const { sent, p } = pointer();
		p.down(1, ...at(0.5, 0.5), box, frame);
		p.move(1, 5000, 5000, box, frame);
		expect(sent[1]).toBe('0102ffffffff');
	});

	// Decision: losing the pointer's capture or a pointercancel cancels the open gesture,
	// at the last place it was, so the device is never left with a finger held down.
	it('cancels an open gesture, at its last point', () => {
		const { sent, p } = pointer();
		p.down(1, ...at(0.5, 0.5), box, frame);
		p.move(1, ...at(0.25, 0.5), box, frame);
		p.cancel(1);
		expect(sent[sent.length - 1]).toBe('010340008000');
		expect(p.open).toBe(false);
	});

	// Decision: a normal up releases the capture too, and that release (lostpointercapture)
	// must not follow the up with a cancel.
	it('does not cancel after an up, nor with no gesture open', () => {
		const { sent, p } = pointer();
		p.down(1, ...at(0.5, 0.5), box, frame);
		p.up(1, ...at(0.5, 0.5), box, frame);
		p.cancel(1);
		p.cancel();
		expect(sent).toHaveLength(2);
		const idle = pointer();
		idle.p.cancel(1);
		expect(idle.sent).toEqual([]);
	});

	// Decision: a gesture that starts on a bar is not a gesture: nothing is sent, nothing
	// is open, and what follows it is not a gesture either.
	it('sends nothing for a pointer that goes down on a bar', () => {
		const { sent, p } = pointer();
		expect(p.down(1, 300, 300, box, frame)).toBe(false);
		p.move(1, ...at(0.5, 0.5), box, frame);
		p.up(1, ...at(0.5, 0.5), box, frame);
		p.cancel(1);
		expect(sent).toEqual([]);
	});

	// Decision: one finger at a time; a second pointer neither starts nor ends the first.
	it('follows the pointer that went down', () => {
		const { sent, p } = pointer();
		p.down(1, ...at(0.5, 0.5), box, frame);
		expect(p.down(2, ...at(0.1, 0.1), box, frame)).toBe(false);
		p.move(2, ...at(0.9, 0.9), box, frame);
		p.up(2, ...at(0.9, 0.9), box, frame);
		expect(sent).toHaveLength(1);
		expect(p.open).toBe(true);
	});

	// Decision: a stream that ends drops its gesture without writing to the closed socket.
	it('forgets a gesture on reset without sending', () => {
		const { sent, p } = pointer();
		p.down(1, ...at(0.5, 0.5), box, frame);
		p.reset();
		p.cancel(1);
		expect(sent).toHaveLength(1);
	});
});

describe('the wheel', () => {
	// Decision: a pixel delta is 100 px a notch, a line delta one notch a line, a page 16.
	it('reads a notch from each delta mode', () => {
		expect(wheelNotches(100, 0)).toBe(1);
		expect(wheelNotches(50, 0)).toBe(0.5);
		expect(wheelNotches(3, 1)).toBe(3);
		expect(wheelNotches(1, 2)).toBe(16);
	});

	// Decision: the browser's positive deltaY (scroll down) is a NEGATIVE v (Android's
	// AXIS_VSCROLL is positive up); positive deltaX (right) is a positive h.
	it('turns down into a negative v and right into a positive h', () => {
		const down = new WheelAccumulator();
		down.add(0, 100, 0, { x: 0.5, y: 0.5 });
		expect(hex(down.take()!)).toBe('03' + '8000' + '8000' + '0000' + 'ff00'); // h 0, v -256
		const right = new WheelAccumulator();
		right.add(100, 0, 0, { x: 0.5, y: 0.5 });
		expect(hex(right.take()!)).toBe('03' + '8000' + '8000' + '0100' + '0000'); // h 256, v 0
	});

	// Decision: small events add up, and a frame is sent only when the sum reaches a unit
	// of the wire (1/256 notch); the remainder is kept, not lost.
	it('accumulates small deltas and keeps the remainder', () => {
		const w = new WheelAccumulator();
		w.add(0, 0.1, 0, { x: 0, y: 0 }); // 0.001 notch: under a unit
		expect(w.take()).toBeNull();
		for (let i = 0; i < 49; i++) w.add(0, 0.1, 0, { x: 0, y: 0 });
		// 5 px in all: 0.05 notch = 12.8 units, so 13 units sent, v negative
		expect(hex(w.take()!)).toBe('03' + '0000' + '0000' + '0000' + 'fff3');
		expect(w.take()).toBeNull();
	});

	// Decision: each axis is held to 16 notches, and the excess is dropped, not queued.
	it('holds an axis to the bound and drops the excess', () => {
		const w = new WheelAccumulator();
		w.add(0, 100_000, 0, { x: 0, y: 0 });
		expect(hex(w.take()!)).toBe('03' + '0000' + '0000' + '0000' + 'f000');
		expect(w.take()).toBeNull();
	});
});

describe('a watch-only view', () => {
	// Decision (ADR-0117 §3): with `control` false the view sends nothing, whatever the pointer,
	// the wheel or a button does; the same input sends once `control` is true.
	it('sends nothing with control false and everything with control true', () => {
		for (const control of [false, true]) {
			const sent: string[] = [];
			const emit = gated(() => control, (b) => sent.push(hex(b)));
			const p = new PointerInput(emit);
			p.down(1, ...at(0.5, 0.5), box, frame);
			p.move(1, ...at(0.4, 0.4), box, frame);
			p.up(1, ...at(0.4, 0.4), box, frame);
			const w = new WheelAccumulator();
			w.add(0, 100, 0, { x: 0.5, y: 0.5 });
			emit(w.take()!);
			emit(encodeKey('back'));
			expect(sent.length).toBe(control ? 5 : 0);
		}
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
	// Decision: Back, Home and Recents are the keys back, home and appSwitch (they travel
	// the stream now; the list is the buttons).
	it('lists back, home and appSwitch for the three buttons', () => {
		expect(NAV_KEYS.map((k) => k.key)).toEqual(['back', 'home', 'appSwitch']);
		expect(NAV_KEYS.map((k) => k.label)).toEqual(['Back', 'Home', 'Recents']);
	});

	// Decision: the text goes to the focused field, so the call carries no target.
	it('types text with no target', async () => {
		const f = fake();
		await new InputSender(f.client, () => 'd').text('ñandú');
		expect(f.calls).toEqual([{ name: 'device.inputText', params: { device: 'd', text: 'ñandú' } }]);
	});

	// Decision: a failure shows as text (code and message); a partial delivery shows its warnings.
	it('turns a refusal and a warning into text', async () => {
		const refusing = {
			call: () => Promise.reject(new CapabilityError({ code: 'DEVICE_NO_STREAM', message: 'no live view' }, false)),
		} as unknown as InputClient;
		expect(await new InputSender(refusing, () => 'd').text('x')).toBe('DEVICE_NO_STREAM: no live view');
		const warn = fake({ 'device.inputText': () => ({ delivered: true, warnings: ['the view failed'] }) });
		expect(await new InputSender(warn.client, () => 'd').text('x')).toBe('the view failed');
	});

	it('sends nothing without a device', async () => {
		const f = fake();
		expect(await new InputSender(f.client, () => '').text('x')).toBe('Select a device first.');
		expect(f.calls).toEqual([]);
	});
});
