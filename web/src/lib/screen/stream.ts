// The screen socket (`GET /v1/screen`, ADR-0078 §4, §7): the first text frame names the
// token and the stream within 5 s; every message after it is binary,
// `[flags u8][pts u64 BE][Annex B]`, flags 1 = key frame, 2 = config. The client only
// sends binary frames after it, the input wire's (control.ts), when the stream's result
// said `control: true`; a text frame or a malformed one makes the daemon close 1003.
import type { SocketLike } from '../api/client';
import type { Frame } from './control';

export const FLAG_KEY = 1;
export const FLAG_CONFIG = 2;
const HEADER = 9;

export interface Packet {
	key: boolean;
	config: boolean;
	/** The presentation time in microseconds, as the device stamped it. */
	pts: bigint;
	/** Annex B; a config is merged into its key frame, so a message with `config` carries the SPS and PPS first. */
	data: Uint8Array;
}

/** A message of the socket, or null when it cannot be one (no payload past its header). */
export function parsePacket(buf: ArrayBuffer): Packet | null {
	if (buf.byteLength <= HEADER) return null;
	const view = new DataView(buf);
	const flags = view.getUint8(0);
	return {
		key: (flags & FLAG_KEY) !== 0,
		config: (flags & FLAG_CONFIG) !== 0,
		pts: view.getBigUint64(1, false),
		data: new Uint8Array(buf, HEADER),
	};
}

/** The slice of a browser WebSocket the player uses. */
export type ScreenWire = Omit<SocketLike, 'send'> & { binaryType: string; send(data: string | Frame): void };

export interface ScreenSocketHandlers {
	packet(p: Packet): void;
	/** The socket closed by the daemon (or the network), with this close code. */
	closed(code: number): void;
}

export interface ScreenSocketOptions {
	url: string;
	token: string;
	streamId: string;
	createSocket: (url: string) => ScreenWire;
}

export class ScreenSocket {
	#socket: ScreenWire;
	#done = false;
	#authed = false;

	constructor(opts: ScreenSocketOptions, handlers: ScreenSocketHandlers) {
		const s = opts.createSocket(opts.url);
		s.binaryType = 'arraybuffer';
		s.onopen = () => {
			s.send(JSON.stringify({ auth: opts.token, stream: opts.streamId }));
			this.#authed = true;
		};
		s.onmessage = (ev) => {
			if (this.#done || !(ev.data instanceof ArrayBuffer)) return;
			const p = parsePacket(ev.data);
			if (p !== null) handlers.packet(p);
		};
		s.onclose = (ev) => {
			if (this.#done) return;
			this.#done = true;
			handlers.closed(ev.code);
		};
		this.#socket = s;
	}

	/** One input frame; nothing before the auth frame went out or after the close (a no-op, not a throw). */
	send(bytes: Frame): void {
		if (!this.#authed || this.#done) return;
		this.#socket.send(bytes);
	}

	/** Closes our side; no `closed` call follows. */
	close(): void {
		if (this.#done) return;
		this.#done = true;
		this.#socket.onopen = null;
		this.#socket.onmessage = null;
		this.#socket.onclose = null;
		this.#socket.close(1000);
	}
}
