import { describe, expect, it } from 'vitest';
import { arrayBuffer, goldenFrame } from './golden';
import { parsePacket, ScreenSocket, type ScreenWire } from './stream';

class FakeWire implements ScreenWire {
	binaryType = '';
	sent: (string | Uint8Array)[] = [];
	closedWith: number | undefined;
	onopen: ((ev: Event) => void) | null = null;
	onmessage: ((ev: MessageEvent) => void) | null = null;
	onclose: ((ev: CloseEvent) => void) | null = null;
	send(d: string | Uint8Array<ArrayBuffer>) {
		this.sent.push(d);
	}
	close(code?: number) {
		this.closedWith = code;
	}
}

describe('screen packets', () => {
	// Decision (ADR-0078 §4): [flags][pts u64 BE][Annex B]; the golden file is the Go
	// side's own output (flags 3, pts 1234567, the 33-byte config, a 9-byte key NAL).
	it('parses the shared golden message', () => {
		const p = parsePacket(arrayBuffer(goldenFrame()))!;
		expect(p.key).toBe(true);
		expect(p.config).toBe(true);
		expect(p.pts).toBe(1234567n);
		expect(p.data.length).toBe(42);
		expect([...p.data.subarray(0, 5)]).toEqual([0, 0, 0, 1, 0x67]);
	});

	it('reads the flags apart', () => {
		const m = Uint8Array.from([2, 0, 0, 0, 0, 0, 0, 0, 9, 0, 0, 1, 0x41]);
		const p = parsePacket(arrayBuffer(m))!;
		expect([p.key, p.config, p.pts]).toEqual([false, true, 9n]);
		expect(parsePacket(arrayBuffer(m.subarray(0, 9)))).toBeNull();
	});
});

describe('ScreenSocket', () => {
	// Decision (ADR-0078 §7): the one text frame after open carries the token and the
	// stream; the client sends nothing else.
	it('authenticates once it is open and passes binary messages on', () => {
		const wire = new FakeWire();
		const got: bigint[] = [];
		const closes: number[] = [];
		new ScreenSocket(
			{ url: 'ws://x/v1/screen', token: 'T', streamId: 'S1', createSocket: () => wire },
			{ packet: (p) => got.push(p.pts), closed: (c) => closes.push(c) },
		);
		expect(wire.binaryType).toBe('arraybuffer');
		wire.onopen?.(new Event('open'));
		expect(wire.sent).toEqual(['{"auth":"T","stream":"S1"}']);
		wire.onmessage?.({ data: arrayBuffer(goldenFrame()) } as MessageEvent);
		wire.onmessage?.({ data: 'text' } as MessageEvent);
		expect(got).toEqual([1234567n]);
		wire.onclose?.({ code: 4410 } as CloseEvent);
		expect(closes).toEqual([4410]);
	});

	it('does not report a close it asked for', () => {
		const wire = new FakeWire();
		const closes: number[] = [];
		const s = new ScreenSocket(
			{ url: 'u', token: 'T', streamId: 'S', createSocket: () => wire },
			{ packet: () => {}, closed: (c) => closes.push(c) },
		);
		s.close();
		expect(wire.closedWith).toBe(1000);
		expect(wire.onclose).toBeNull();
		expect(closes).toEqual([]);
	});
});

describe('ScreenSocket.send', () => {
	function open() {
		const wire = new FakeWire();
		const s = new ScreenSocket(
			{ url: 'u', token: 'T', streamId: 'S', createSocket: () => wire },
			{ packet: () => {}, closed: () => {} },
		);
		return { wire, s };
	}

	// Decision (ADR-0117 §3): the daemon reads the first frame as the auth; an input frame
	// sent before it would be read as one, so nothing goes out until it has.
	it('sends nothing before the auth frame went out', () => {
		const { wire, s } = open();
		s.send(Uint8Array.of(2, 1));
		expect(wire.sent).toEqual([]);
		wire.onopen?.(new Event('open'));
		s.send(Uint8Array.of(2, 1));
		expect(wire.sent).toEqual(['{"auth":"T","stream":"S"}', Uint8Array.of(2, 1)]);
	});

	// Decision: after our close, or the daemon's, sending is a no-op, not a throw.
	it('sends nothing after a close, ours or the daemon\'s', () => {
		const a = open();
		a.wire.onopen?.(new Event('open'));
		a.s.close();
		expect(() => a.s.send(Uint8Array.of(2, 1))).not.toThrow();
		expect(a.wire.sent).toHaveLength(1);
		const b = open();
		b.wire.onopen?.(new Event('open'));
		b.wire.onclose?.({ code: 1006 } as CloseEvent);
		b.s.send(Uint8Array.of(2, 1));
		expect(b.wire.sent).toHaveLength(1);
	});
});
