import { describe, expect, it } from 'vitest';
import { codecString, findSps, nalUnits } from './h264';
import { arrayBuffer, goldenFrame } from './golden';
import { parsePacket } from './stream';

describe('h264', () => {
	// Decision (plan m2p step 5): the codec string is the three bytes after the SPS's NAL
	// header, as the spike's scratch page built it; the real SPS of step 1 gives 42c029.
	it('names the codec from the real SPS', () => {
		const packet = parsePacket(arrayBuffer(goldenFrame()))!;
		const sps = findSps(packet.data)!;
		expect(sps[0]! & 0x1f).toBe(7);
		expect(codecString(sps)).toBe('avc1.42c029');
	});

	// Decision: a unit ends where the next start code begins, whichever length it has,
	// and a 4-byte code's leading zero does not stay on the unit before it.
	it('splits Annex B on 3- and 4-byte start codes', () => {
		const data = Uint8Array.from([0, 0, 0, 1, 0x67, 1, 2, 3, 0, 0, 1, 0x68, 9, 0, 0, 0, 1, 0x65, 7]);
		expect(nalUnits(data).map((n) => [...n])).toEqual([[0x67, 1, 2, 3], [0x68, 9], [0x65, 7]]);
	});

	it('finds no SPS in a delta frame', () => {
		expect(findSps(Uint8Array.from([0, 0, 0, 1, 0x41, 1, 2]))).toBeNull();
	});
});
