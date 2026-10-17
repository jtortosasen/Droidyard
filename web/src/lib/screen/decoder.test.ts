import { describe, expect, it } from 'vitest';
import { VideoPacketDecoder, type CodecApi, type DecodedFrame } from './decoder';
import type { Packet } from './stream';

function fake() {
	const log: string[] = [];
	let init!: { output(f: DecodedFrame): void; error(e: Error): void };
	const api: CodecApi = {
		createDecoder: (i) => {
			init = i;
			return {
				configure: (c) => log.push(`configure ${c.codec} ${c.optimizeForLatency}`),
				decode: (c) => log.push(`decode ${c.type} ${c.timestamp}`),
				close: () => log.push('close'),
			};
		},
	};
	const frames: DecodedFrame[] = [];
	const errors: string[] = [];
	const d = new VideoPacketDecoder(api, { frame: (f) => frames.push(f), error: (m) => errors.push(m) });
	return { d, log, frames, errors, fail: (m: string) => init.error(new Error(m)) };
}

const sps = (profile: number) => [0, 0, 0, 1, 0x67, profile, 0xc0, 0x29, 0x8d];
const key = (profile: number, pts = 1n): Packet => ({
	key: true,
	config: true,
	pts,
	data: Uint8Array.from([...sps(profile), 0, 0, 0, 1, 0x68, 0xce, 0, 0, 0, 1, 0x65, 1]),
});
const delta = (pts: bigint): Packet => ({ key: false, config: false, pts, data: Uint8Array.from([0, 0, 0, 1, 0x41, 1]) });

describe('VideoPacketDecoder', () => {
	// Decision (plan m2p step 5): no description, optimizeForLatency, and a codec string
	// from the SPS; a delta frame before any key frame is not fed to the decoder.
	it('configures from the SPS and waits for a key frame', () => {
		const t = fake();
		t.d.push(delta(0n));
		t.d.push(key(0x42, 5n));
		t.d.push(delta(6n));
		expect(t.log).toEqual(['configure avc1.42c029 true', 'decode key 5', 'decode delta 6']);
	});

	// Decision: a new SPS (codec or size) reconfigures; the same SPS in a later key frame does not.
	it('reconfigures only when the SPS changes', () => {
		const t = fake();
		t.d.push(key(0x42));
		t.d.push(key(0x42, 2n));
		t.d.push(key(0x64, 3n));
		expect(t.log.filter((l) => l.startsWith('configure'))).toEqual(['configure avc1.42c029 true', 'configure avc1.64c029 true']);
	});

	it('reports a decoder error once and then takes nothing', () => {
		const t = fake();
		t.d.push(key(0x42));
		t.fail('boom');
		t.fail('again');
		t.d.push(delta(9n));
		expect(t.errors).toEqual(['boom']);
		expect(t.log.filter((l) => l.startsWith('decode'))).toHaveLength(1);
	});
});
