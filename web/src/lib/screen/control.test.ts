import { describe, expect, it } from 'vitest';
import { encodeKey, encodeScroll, encodeTouch, MAX_NOTCHES, NOTCH_UNITS, type TouchAction, type WireKey } from './control';
import { controlGolden, hex } from './golden';

// Decision (ADR-0117 §3): the web encoder and the daemon's reader share one golden, so a
// byte moved on either side fails both. The encoder is run on the event each row means.
describe('the input wire against the golden', () => {
	const cases = controlGolden();

	it('has the rows this test relies on', () => {
		expect(new Set(cases.map((c) => c.event.kind))).toEqual(new Set(['touch', 'key', 'scroll']));
	});

	for (const c of cases) {
		const e = c.event;
		// A row whose event is past the bound is the daemon's saturation; the client never
		// produces it (it holds each axis to 16 notches), so the bound row checks it apart.
		const beyond = e.kind === 'scroll' && (Math.abs(e.h) > MAX_NOTCHES * NOTCH_UNITS || Math.abs(e.v) > MAX_NOTCHES * NOTCH_UNITS);
		if (beyond) continue;
		it(`encodes: ${c.name}`, () => {
			let got: Uint8Array;
			if (e.kind === 'touch') got = encodeTouch(e.action as TouchAction, e.x / 65535, e.y / 65535);
			else if (e.kind === 'key') got = encodeKey(e.key as WireKey);
			else got = encodeScroll(e.x / 65535, e.y / 65535, e.h / NOTCH_UNITS, e.v / NOTCH_UNITS);
			expect(hex(got)).toBe(c.wire);
		});
	}

	// Decision: a scroll axis is held to +-16 notches, as the bound row says.
	it('holds a scroll to the bound the golden pins', () => {
		const bound = cases.find((c) => c.name === 'scroll at the bound saturates')!;
		expect(hex(encodeScroll(100 / 65535, 200 / 65535, 100, -100))).toBe(bound.wire);
	});
});

describe('the encoder', () => {
	// Decision: a point outside the frame is held at its edge, never wrapped.
	it('clamps the coordinates to the frame', () => {
		expect(hex(encodeTouch('move', -0.5, 2))).toBe('01020000ffff');
		expect(hex(encodeTouch('down', NaN, 1))).toBe('01000000ffff');
	});
});
