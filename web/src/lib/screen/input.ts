// Input in the live view (plan m2p step 7; ADR-0079): pure. A pointer on the canvas
// becomes a point of the stream's frame, and the pointer's gesture becomes one of the
// calls `device.tap`, `device.swipe` or `device.pressKey`. No DOM here: a test drives it.
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError } from '../api/client';

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

/** A tap moves under this many pixels, and a tap that is not held lasts under this many ms. */
export const TAP_SLOP_PX = 10;
export const TAP_MS = 300;
/** The contract's limits (device.tap, device.swipe), and the sampling between them. */
export const MAX_HOLD_MS = 2000;
export const MAX_SWIPE_MS = 5000;
export const MAX_POINTS = 64;
export const SAMPLE_MS = 16;

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

export type Gesture =
	| { kind: 'tap'; x: number; y: number; durationMs?: number }
	| { kind: 'swipe'; path: { x: number; y: number; t: number }[] };

interface Sample {
	cx: number;
	cy: number;
	t: number;
}

/** Collects one pointer's samples, in client pixels and ms, and classifies them at the lift. */
export class GestureRecorder {
	#samples: Sample[] = [];
	#box: Box;
	#frame: Size;

	/** Null when the pointer went down on a bar: nothing is recorded then. */
	static start(cx: number, cy: number, t: number, box: Box, frame: Size): GestureRecorder | null {
		return toFrame(cx, cy, box, frame) === null ? null : new GestureRecorder(cx, cy, t, box, frame);
	}

	private constructor(cx: number, cy: number, t: number, box: Box, frame: Size) {
		this.#samples.push({ cx, cy, t });
		this.#box = box;
		this.#frame = frame;
	}

	move(cx: number, cy: number, t: number): void {
		this.#samples.push({ cx, cy, t });
	}

	/** The gesture the samples make, the lift at (cx, cy) included. */
	end(cx: number, cy: number, t: number): Gesture {
		this.#samples.push({ cx, cy, t });
		const s = this.#samples;
		const first = s[0]!;
		const t0 = first.t;
		const held = Math.max(0, t - t0);
		const moved = s.some((p) => Math.hypot(p.cx - first.cx, p.cy - first.cy) >= TAP_SLOP_PX);
		const at = (p: Sample) => toFrame(p.cx, p.cy, this.#box, this.#frame, true)!;
		if (!moved) {
			const p = at(first);
			return held < TAP_MS
				? { kind: 'tap', x: p.x, y: p.y }
				: { kind: 'tap', x: p.x, y: p.y, durationMs: Math.min(MAX_HOLD_MS, Math.round(held)) };
		}
		return { kind: 'swipe', path: this.#path(at, t0) };
	}

	#path(at: (p: Sample) => Point, t0: number): { x: number; y: number; t: number }[] {
		// Samples within the 5 s the contract allows; the lift after it is dropped.
		const within = this.#samples.filter((p) => p.t - t0 <= MAX_SWIPE_MS);
		const last = within[within.length - 1]!;
		// Keep a sample only when 16 ms have passed since the one kept before it.
		let kept: Sample[] = [within[0]!];
		for (const p of within.slice(1, -1)) {
			if (p.t - kept[kept.length - 1]!.t >= SAMPLE_MS) kept.push(p);
		}
		if (kept.length > MAX_POINTS - 1) kept = pick(kept, MAX_POINTS - 1);
		kept.push(last);
		const out: { x: number; y: number; t: number }[] = [];
		for (const p of kept) {
			const t = Math.round(p.t - t0);
			const prev = out[out.length - 1];
			const pt = at(p);
			if (prev !== undefined && t <= prev.t) {
				// Same millisecond as the point before: the later position wins, one ms on if it is the lift.
				if (p === last && out.length === 1) out.push({ ...pt, t: prev.t + 1 });
				else if (p === last) out[out.length - 1] = { ...pt, t: prev.t };
				continue;
			}
			out.push({ ...pt, t });
		}
		return out;
	}
}

/** `n` items from `xs`, evenly spread, the first and the last among them. */
function pick<T>(xs: T[], n: number): T[] {
	if (xs.length <= n) return xs;
	const out: T[] = [];
	for (let i = 0; i < n; i++) out.push(xs[Math.round((i * (xs.length - 1)) / (n - 1))]!);
	return out;
}

export type NavKey = 'back' | 'home' | 'appSwitch';

/** The three buttons under the screen, and the key each one presses. */
export const NAV_KEYS: readonly { key: NavKey; label: string }[] = [
	{ key: 'back', label: 'Back' },
	{ key: 'home', label: 'Home' },
	{ key: 'appSwitch', label: 'Recents' },
];

export interface InputClient {
	call(name: 'device.tap', params: Params<'device.tap'>): Promise<Result<'device.tap'>>;
	call(name: 'device.swipe', params: Params<'device.swipe'>): Promise<Result<'device.swipe'>>;
	call(name: 'device.pressKey', params: Params<'device.pressKey'>): Promise<Result<'device.pressKey'>>;
	call(name: 'device.inputText', params: Params<'device.inputText'>): Promise<Result<'device.inputText'>>;
}

type Delivery = { warnings?: string[] };

/** Sends the view's input to `device()`. Each method resolves to the text of what went wrong, or null. */
export class InputSender {
	#client: InputClient;
	#device: () => string;

	constructor(client: InputClient, device: () => string) {
		this.#client = client;
		this.#device = device;
	}

	gesture(g: Gesture): Promise<string | null> {
		const device = this.#device();
		return this.#run(device, () =>
			g.kind === 'swipe'
				? this.#client.call('device.swipe', { device, path: g.path as Params<'device.swipe'>['path'] })
				: this.#client.call('device.tap', {
						device,
						x: g.x,
						y: g.y,
						...(g.durationMs === undefined ? {} : { durationMs: g.durationMs }),
					}),
		);
	}

	key(key: NavKey): Promise<string | null> {
		const device = this.#device();
		return this.#run(device, () => this.#client.call('device.pressKey', { device, key }));
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
