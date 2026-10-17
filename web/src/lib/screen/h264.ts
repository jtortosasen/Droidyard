// The two things the player needs from H.264 in Annex B (ADR-0078 §4): the SPS NAL
// unit of a message, and the WebCodecs codec string it names. Nothing else is parsed:
// the decoder takes Annex B with no `description` and finds the picture size itself.

/** The NAL units of an Annex B buffer, start codes removed (3- or 4-byte codes). */
export function nalUnits(data: Uint8Array): Uint8Array[] {
	const starts: number[] = []; // the index of the first byte after each start code
	for (let i = 0; i + 2 < data.length; i++) {
		if (data[i] === 0 && data[i + 1] === 0 && data[i + 2] === 1) {
			starts.push(i + 3);
			i += 2;
		}
	}
	const out: Uint8Array[] = [];
	for (let k = 0; k < starts.length; k++) {
		const begin = starts[k]!;
		let end = k + 1 < starts.length ? starts[k + 1]! - 3 : data.length;
		// A 4-byte start code leaves its leading zero on the unit before it; a NAL never ends in 0x00.
		while (end > begin && data[end - 1] === 0) end--;
		if (end > begin) out.push(data.subarray(begin, end));
	}
	return out;
}

/** The first SPS (NAL type 7) of the buffer, header byte included, or null. */
export function findSps(data: Uint8Array): Uint8Array | null {
	for (const nal of nalUnits(data)) {
		if ((nal[0]! & 0x1f) === 7) return nal;
	}
	return null;
}

/** `avc1.PPCCLL`: profile, constraint flags and level, the three bytes after the NAL header. */
export function codecString(sps: Uint8Array): string {
	if (sps.length < 4) throw new Error('an SPS is at least four bytes');
	const hex = (b: number) => b.toString(16).padStart(2, '0');
	return `avc1.${hex(sps[1]!)}${hex(sps[2]!)}${hex(sps[3]!)}`;
}

export function sameBytes(a: Uint8Array | null, b: Uint8Array | null): boolean {
	if (a === null || b === null) return a === b;
	return a.length === b.length && a.every((v, i) => v === b[i]);
}
