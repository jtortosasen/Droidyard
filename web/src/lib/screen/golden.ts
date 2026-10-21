// The shared fixtures of the Go and the web tests (internal/domain/device/screen/testdata).
import { readFileSync } from 'node:fs';

export function goldenFrame(): Uint8Array {
	const url = new URL('../../../../internal/domain/device/screen/testdata/frame-golden.bin', import.meta.url);
	return new Uint8Array(readFileSync(url));
}

export function arrayBuffer(b: Uint8Array): ArrayBuffer {
	return b.buffer.slice(b.byteOffset, b.byteOffset + b.byteLength) as ArrayBuffer;
}

export interface ControlCase {
	name: string;
	/** The input frame, in hex. */
	wire: string;
	event: { kind: 'touch' | 'key' | 'scroll'; action: string; x: number; y: number; key: string; h: number; v: number };
}

/** The input wire's golden (ADR-0117 §3): each case's frame and the event the daemon reads it as. */
export function controlGolden(): ControlCase[] {
	const url = new URL('../../../../internal/domain/device/screen/testdata/control-wire.json', import.meta.url);
	return (JSON.parse(new TextDecoder().decode(readFileSync(url))) as { cases: ControlCase[] }).cases;
}

export function hex(b: Uint8Array): string {
	return [...b].map((x) => x.toString(16).padStart(2, '0')).join('');
}
