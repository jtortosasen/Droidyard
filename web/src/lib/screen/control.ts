// The live view's input wire (ADR-0117 §3): pure, no DOM. A pointer or wheel sample,
// normalised to the stream's frame, becomes one binary frame of the screen socket. The
// byte layouts are the contract; `control-wire.json` (the Go side's golden) pins them.
//
//   touch  [01][action u8][x u16 BE][y u16 BE]            action: down 0, up 1, move 2, cancel 3
//   key    [02][key u8]                                    back 1, home 2, appSwitch 3, enter 4, delete 5
//   scroll [03][x u16 BE][y u16 BE][h i16 BE][v i16 BE]    h and v in 1/256 notch, positive v up

/** One frame of the wire. */
export type Frame = Uint8Array<ArrayBuffer>;
export type TouchAction = 'down' | 'up' | 'move' | 'cancel';
export type WireKey = 'back' | 'home' | 'appSwitch' | 'enter' | 'delete';

const TOUCH = 1;
const KEY = 2;
const SCROLL = 3;
const ACTIONS: Record<TouchAction, number> = { down: 0, up: 1, move: 2, cancel: 3 };
const KEYS: Record<WireKey, number> = { back: 1, home: 2, appSwitch: 3, enter: 4, delete: 5 };

/** The scroll's per-axis bound, in notches, and the wire's unit. */
export const MAX_NOTCHES = 16;
export const NOTCH_UNITS = 256;

/** A point of the frame, 0..1, as u16 0..65535 rounded; outside the frame is held at its edge. */
function u16(n: number): number {
	if (!(n > 0)) return 0; // also NaN
	return n >= 1 ? 65535 : Math.round(n * 65535);
}

/** Notches as i16 units of 1/256, rounded and held to the bound. */
export function units(notches: number): number {
	if (!Number.isFinite(notches)) return 0;
	const u = Math.round(notches * NOTCH_UNITS);
	const max = MAX_NOTCHES * NOTCH_UNITS;
	return Math.max(-max, Math.min(max, u));
}

export function encodeTouch(action: TouchAction, x: number, y: number): Frame {
	const b = new Uint8Array(6);
	const v = new DataView(b.buffer);
	b[0] = TOUCH;
	b[1] = ACTIONS[action];
	v.setUint16(2, u16(x), false);
	v.setUint16(4, u16(y), false);
	return b;
}

export function encodeKey(key: WireKey): Frame {
	return Uint8Array.of(KEY, KEYS[key]);
}

/** `h` and `v` in notches (positive `v` is up, as Android's AXIS_VSCROLL), each held to ±16. */
export function encodeScroll(x: number, y: number, h: number, v: number): Frame {
	const b = new Uint8Array(9);
	const d = new DataView(b.buffer);
	b[0] = SCROLL;
	d.setUint16(1, u16(x), false);
	d.setUint16(3, u16(y), false);
	d.setInt16(5, units(h), false);
	d.setInt16(7, units(v), false);
	return b;
}
