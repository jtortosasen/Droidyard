// WebCodecs `VideoDecoder` behind an interface (plan m2p step 5; proven by the spike's
// scratch page): Annex B with no `description`, `optimizeForLatency`, the codec string
// from the SPS. The decoder is configured when an SPS arrives that differs from the one
// it holds (a new codec string, or a new picture size, which changes the SPS bytes), and
// takes nothing but a key frame after a configure.
import { codecString, findSps, sameBytes } from './h264';
import type { Packet } from './stream';

export interface DecodedFrame {
	displayWidth: number;
	displayHeight: number;
	close(): void;
}

export interface DecoderSink {
	frame(f: DecodedFrame): void;
	/** The decoder failed; it is dead and takes no more. */
	error(message: string): void;
}

export interface PacketDecoder {
	push(p: Packet): void;
	close(): void;
}

/** What the decoder needs of WebCodecs, so a test passes fakes. */
export interface CodecApi {
	createDecoder(init: { output(f: DecodedFrame): void; error(e: Error): void }): {
		configure(c: { codec: string; optimizeForLatency: boolean }): void;
		decode(c: { type: 'key' | 'delta'; timestamp: number; data: Uint8Array }): void;
		close(): void;
	};
}

/** Null where the browser has no WebCodecs video decoding (the panel then says so). */
export function browserCodec(): CodecApi | null {
	if (typeof VideoDecoder === 'undefined' || typeof EncodedVideoChunk === 'undefined') return null;
	return {
		createDecoder: (init) => {
			const d = new VideoDecoder({ output: init.output, error: init.error });
			return {
				configure: (c) => d.configure(c),
				decode: (c) => d.decode(new EncodedVideoChunk(c)),
				close: () => {
					if (d.state !== 'closed') d.close();
				},
			};
		},
	};
}

export class VideoPacketDecoder implements PacketDecoder {
	#d: ReturnType<CodecApi['createDecoder']>;
	#sink: DecoderSink;
	#sps: Uint8Array | null = null;
	#needKey = true;
	#dead = false;

	constructor(api: CodecApi, sink: DecoderSink) {
		this.#sink = sink;
		this.#d = api.createDecoder({
			output: (f) => {
				if (this.#dead) f.close();
				else sink.frame(f);
			},
			error: (e) => this.#fail(e.message),
		});
	}

	push(p: Packet): void {
		if (this.#dead) return;
		try {
			const sps = p.key ? findSps(p.data) : null;
			if (sps !== null && !sameBytes(sps, this.#sps)) {
				this.#d.configure({ codec: codecString(sps), optimizeForLatency: true });
				this.#sps = sps.slice();
				this.#needKey = true;
			}
			if (this.#sps === null || (this.#needKey && !p.key)) return;
			this.#needKey = false;
			this.#d.decode({ type: p.key ? 'key' : 'delta', timestamp: Number(p.pts), data: p.data });
		} catch (e) {
			this.#fail(e instanceof Error ? e.message : 'the decoder refused the packet');
		}
	}

	close(): void {
		this.#dead = true;
		this.#d.close();
	}

	#fail(message: string): void {
		if (this.#dead) return;
		this.#dead = true;
		this.#sink.error(message);
	}
}
