// Input in the live view (ADR-0117): pure. A pointer on the canvas becomes a point of the
// stream's frame, and each pointer and wheel event becomes one frame of the input wire
// (control.ts) at once. Text keeps the call `device.inputText`. No DOM here: a test drives it.
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError } from '../api/client';
import { encodeScroll, encodeTouch, type Frame, MAX_NOTCHES, NOTCH_UNITS, units } from './control';

export interface Box {
	left: number;
	top: number;
	width: number;
	height: number;
}
export interface Size {
	width: number;
	height: number;
}
export interface Point {
	x: number;
	y: number;
}

/**
 * Where the frame is drawn inside the canvas's box: the canvas is letterboxed with
 * `object-fit: contain`, so the frame is as large as fits and centred; the rest is bars.
 */
export function frameRect(box: Box, frame: Size): Box {
	if (frame.width <= 0 || frame.height <= 0 || box.width <= 0 || box.height <= 0) {
		return { left: box.left, top: box.top, width: 0, height: 0 };
	}
	const scale = Math.min(box.width / frame.width, box.height / frame.height);
	const width = frame.width * scale;
	const height = frame.height * scale;
	return { left: box.left + (box.width - width) / 2, top: box.top + (box.height - height) / 2, width, height };
}

/**
 * A client position as a point of the frame, 0..1 on each axis. A position on the bars is
 * null; with `clamp`, it is the nearest point of the frame (a drag that leaves the frame).
 */
export function toFrame(clientX: number, clientY: number, box: Box, frame: Size, clamp = false): Point | null {
	const r = frameRect(box, frame);
	if (r.width === 0 || r.height === 0) return null;
	const x = (clientX - r.left) / r.width;
	const y = (clientY - r.top) / r.height;
	if (!clamp && (x < 0 || x > 1 || y < 0 || y > 1)) return null;
	return { x: Math.min(1, Math.max(0, x)), y: Math.min(1, Math.max(0, y)) };
}

export type NavKey = 'back' | 'home' | 'appSwitch';

/** The three buttons under the screen, and the key each one presses. */
export const NAV_KEYS: readonly { key: NavKey; label: string }[] = [
	{ key: 'back', label: 'Back' },
	{ key: 'home', label: 'Home' },
	{ key: 'appSwitch', label: 'Recents' },
];

/**
 * Where the view's input frames go: `send` while `enabled()` (the stream said `control`),
 * nowhere otherwise, so a watch-only view sends nothing whatever the pointer does.
 */
export function gated(enabled: () => boolean, send: (frame: Frame) => void): (frame: Frame) => void {
	return (frame) => {
		if (enabled()) send(frame);
	};
}

const BOUND = MAX_NOTCHES * NOTCH_UNITS;

/** What a wheel's delta means in notches: a notch is 100 px; a line is one; a page is 16 (the bound). */
export function wheelNotches(delta: number, deltaMode: number): number {
	if (deltaMode === 1) return delta;
	if (deltaMode === 2) return delta * 16;
	return delta / 100;
}

/**
 * The touch the pointer is making: down, every move, up. One pointer at a time; a pointer
 * that goes down on a bar sends nothing and starts nothing. A cancel goes only while a
 * gesture is open, so the release that follows an up is not one.
 */
export class PointerInput {
	#send: (frame: Frame) => void;
	#id: number | null = null;
	#last: Point = { x: 0, y: 0 };

	constructor(send: (frame: Frame) => void) {
		this.#send = send;
	}

	get open(): boolean {
		return this.#id !== null;
	}

	/** True when the gesture began, so the caller captures the pointer. */
	down(id: number, cx: number, cy: number, box: Box, frame: Size): boolean {
		if (this.#id !== null) return false;
		const p = toFrame(cx, cy, box, frame);
		if (p === null) return false;
		this.#id = id;
		this.#last = p;
		this.#send(encodeTouch('down', p.x, p.y));
		return true;
	}

	/** Every move while down, held to the frame. */
	move(id: number, cx: number, cy: number, box: Box, frame: Size): void {
		if (id !== this.#id) return;
		const p = toFrame(cx, cy, box, frame, true);
		if (p === null) return;
		this.#last = p;
		this.#send(encodeTouch('move', p.x, p.y));
	}

	up(id: number, cx: number, cy: number, box: Box, frame: Size): void {
		if (id !== this.#id) return;
		const p = toFrame(cx, cy, box, frame, true) ?? this.#last;
		this.#id = null;
		this.#send(encodeTouch('up', p.x, p.y));
	}

	/** The pointer was cancelled or lost its capture: a cancel, if a gesture is open. */
	cancel(id?: number): void {
		if (this.#id === null || (id !== undefined && id !== this.#id)) return;
		this.#id = null;
		this.#send(encodeTouch('cancel', this.#last.x, this.#last.y));
	}

	/** Forgets the gesture without sending: the stream it belonged to is gone. */
	reset(): void {
		this.#id = null;
	}
}

/**
 * The wheel's deltas, gathered between animation frames: a trackpad fires many small events,
 * and one scroll frame carries their sum. The browser's positive `deltaY` (down) is a negative
 * `v`; positive `deltaX` (right) is a positive `h`. Each axis is held to the wire's bound.
 */
export class WheelAccumulator {
	#h = 0;
	#v = 0;
	#at: Point = { x: 0, y: 0 };

	add(deltaX: number, deltaY: number, deltaMode: number, at: Point): void {
		this.#h += wheelNotches(deltaX, deltaMode);
		this.#v -= wheelNotches(deltaY, deltaMode);
		this.#at = at;
	}

	/** The scroll frame for what was gathered, or null when it rounds to nothing (it is kept for the next). */
	take(): Frame | null {
		const h = units(this.#h);
		const v = units(this.#v);
		if (h === 0 && v === 0) return null;
		// A saturated axis drops its excess; the rest keeps its remainder for the next frame.
		this.#h = Math.abs(h) === BOUND ? 0 : this.#h - h / NOTCH_UNITS;
		this.#v = Math.abs(v) === BOUND ? 0 : this.#v - v / NOTCH_UNITS;
		return encodeScroll(this.#at.x, this.#at.y, h / NOTCH_UNITS, v / NOTCH_UNITS);
	}

	reset(): void {
		this.#h = 0;
		this.#v = 0;
	}
}

export interface InputClient {
	call(name: 'device.inputText', params: Params<'device.inputText'>): Promise<Result<'device.inputText'>>;
}

type Delivery = { warnings?: string[] };

/** Sends the view's text to `device()`: the one input that still travels as a call. Resolves to the text of what went wrong, or null. */
export class InputSender {
	#client: InputClient;
	#device: () => string;

	constructor(client: InputClient, device: () => string) {
		this.#client = client;
		this.#device = device;
	}

	/** Into whatever holds focus: no target. */
	text(text: string): Promise<string | null> {
		const device = this.#device();
		return this.#run(device, () => this.#client.call('device.inputText', { device, text }));
	}

	async #run(device: string, send: () => Promise<Delivery>): Promise<string | null> {
		if (device === '') return 'Select a device first.';
		try {
			const r = await send();
			return r.warnings && r.warnings.length > 0 ? r.warnings.join(' ') : null;
		} catch (e) {
			if (e instanceof CapabilityError) return `${e.code}: ${e.message}`;
			return e instanceof Error ? e.message : 'the input could not be sent';
		}
	}
}
