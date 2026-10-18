// The shared header fixture of the Go and the web tests (internal/domain/device/screen/testdata).
import { readFileSync } from 'node:fs';

export function goldenFrame(): Uint8Array {
	const url = new URL('../../../../internal/domain/device/screen/testdata/frame-golden.bin', import.meta.url);
	return new Uint8Array(readFileSync(url));
}

export function arrayBuffer(b: Uint8Array): ArrayBuffer {
	return b.buffer.slice(b.byteOffset, b.byteOffset + b.byteLength) as ArrayBuffer;
}
