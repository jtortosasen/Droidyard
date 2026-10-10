import { describe, expect, it } from 'vitest';
import { ConnectionError } from '../api/client';
import { FakeFrames } from '../logcat/testing';
import { ConsoleModel, PAGE, type ConsoleClient, type ConsoleSource } from './console';
import { CONSOLE_LINES } from './lines';
import { capError, FakeCalls, FakeStream, settle } from './testing';

// A console as the daemon holds it: bytes, read the way artifact.read reads them
// (internal/artifact/text.go): a negative offset starts at the next line, a positive one
// exactly where it says, and a read ends at the last complete line within the limit.
class FakeArtifact {
	bytes = new Uint8Array(0);
	growing = true;
	append(lines: string[]): number {
		const at = this.bytes.length;
		const add = new TextEncoder().encode(lines.map((l) => `${l}\n`).join(''));
		const next = new Uint8Array(at + add.length);
		next.set(this.bytes);
		next.set(add, at);
		this.bytes = next;
		return at;
	}
	get size(): number {
		return this.bytes.length;
	}
	get string(): string {
		return new TextDecoder().decode(this.bytes);
	}
	read(offset: number, limit: number) {
		limit = Math.min(Math.max(limit, 4), PAGE);
		const size = this.size;
		const text = (a: number, b: number) => new TextDecoder().decode(this.bytes.subarray(a, b));
		let start = offset < 0 ? size + offset : offset;
		start = Math.min(Math.max(start, 0), size);
		if (offset < 0 && start > 0 && start < size && this.bytes[start - 1] !== 10) {
			const i = this.bytes.subarray(start, start + limit).indexOf(10);
			if (i >= 0) start += i + 1;
		}
		const base = { ref: 'r', mediaType: 'text/plain', offset: start, size, growing: this.growing };
		if (start >= size) return { ...base, nextOffset: size, text: '' };
		const win = this.bytes.subarray(start, start + limit);
		const i = win.lastIndexOf(10);
		if (i >= 0) return { ...base, nextOffset: start + i + 1, text: text(start, start + i + 1) };
		if (start + win.length < size) return { ...base, nextOffset: start + win.length, text: text(start, start + win.length), truncated: true };
		return this.growing
			? { ...base, nextOffset: start, text: '' }
			: { ...base, nextOffset: start + win.length, text: text(start, start + win.length) };
	}
}

class FakeWs {
	id: string | null = 'w1';
	root = '/p';
	#l = new Set<() => void>();
	subscribe(l: () => void) {
		this.#l.add(l);
		return () => this.#l.delete(l);
	}
	set(id: string | null) {
		this.id = id;
		for (const l of this.#l) l();
	}
}

class FakeSource implements ConsoleSource {
	view = { open: true, last: null as { buildId: string; consoleRef: string } | null, mirror: { building: null as unknown } };
	#l = new Set<() => void>();
	subscribe(l: () => void) {
		this.#l.add(l);
		return () => this.#l.delete(l);
	}
	set(over: Partial<FakeSource['view']>) {
		this.view = { ...this.view, ...over };
		for (const l of this.#l) l();
	}
}

/** Eleven bytes with the newline: a page boundary lands inside a line. */
const L = (i: number) => `line-${String(i).padStart(5, '0')}`;
const range = (a: number, b: number) => Array.from({ length: b - a }, (_, i) => L(a + i));

function setup() {
	const art = new FakeArtifact();
	const calls = new FakeCalls();
	const reads: { offset: number; limit: number }[] = [];
	calls.on('artifact.read', (p) => {
		const q = p as { offset?: number; limit?: number };
		reads.push({ offset: q.offset ?? 0, limit: q.limit ?? 16384 });
		return art.read(q.offset ?? 0, q.limit ?? 16384);
	});
	const stream = new FakeStream('w');
	const subOpts: unknown[] = [];
	const client = {
		call: (name: string, params: unknown, opts?: unknown) =>
			name === 'events.unsubscribe'
				? stream.call(name as 'events.unsubscribe', params as never)
				: (calls.call as (n: string, p: unknown, o?: unknown) => Promise<never>)(name, params, opts),
		subscribe: (n: string, p: unknown, h: never, o: unknown) => {
			subOpts.push(o);
			return stream.subscribe(n, p, h);
		},
	};
	const src = new FakeSource();
	const ws = new FakeWs();
	const frames = new FakeFrames();
	const model = new ConsoleModel(client as unknown as ConsoleClient, src, ws, frames);
	let notified = 0;
	model.subscribe(() => notified++);
	// The panel reads the view when told, once per frame: the test runs the frame first.
	const view = () => {
		frames.tick();
		return model.view;
	};
	const text = () => {
		const v = view();
		return model.rows(0, v.count).map((r) => r.text);
	};
	return {
		art,
		calls,
		reads,
		stream,
		subOpts,
		src,
		ws,
		frames,
		model,
		text,
		view,
		get notified() {
			return notified;
		},
		/** The daemon answers watch `i` with the running builds. */
		async snapshot(i: number, builds: { buildId: string; consoleRef: string; offset: number }[]) {
			stream.answer(i, { subscriptionId: `w${i + 1}`, builds });
			await settle();
		},
		/** The daemon publishes lines at `offset` on watch `i`. */
		async lines(i: number, buildId: string, offset: number, lines: string[]) {
			stream.emit(i, {
				topic: 'build.output',
				type: 'lines',
				payload: { buildId, consoleRef: `ref-${buildId}`, offset, stream: 'stdout', lines },
			});
			await settle();
		},
	};
}

describe('the live console', () => {
	// Decision (ADR-0044 §1, the result's description): a snapshot offset is the console's
	// size when it was taken; the head is read up to it, an event that starts below it
	// repeats bytes already read and is dropped, and an event that starts past what is held
	// is preceded by the bytes in between, read with artifact.read.
	it('reads the head, drops an event below the snapshot and reads the gap before one past it', async () => {
		const t = setup();
		const head = Array.from({ length: 10 }, (_, i) => `head-0${i}`.padEnd(9, '.')); // 10 bytes each: 100
		t.art.append(head);
		expect(t.art.size).toBe(100);
		await t.snapshot(0, [{ buildId: 'b1', consoleRef: 'ref-b1', offset: 100 }]);
		expect(t.reads).toEqual([{ offset: 0, limit: 100 }]);
		expect(t.text()).toEqual(head);

		t.art.append(['mid-aaaaa', 'mid-bbbbb']); // [100, 120)
		t.art.append(['tail-aaaa', 'tail-bbbb']); // [120, 140)
		await t.lines(0, 'b1', 80, head.slice(8)); // repeats [80, 100)
		expect(t.text()).toEqual(head);
		expect(t.reads).toHaveLength(1);

		await t.lines(0, 'b1', 120, ['tail-aaaa', 'tail-bbbb']);
		expect(t.reads[1]).toEqual({ offset: 100, limit: 20 });
		expect(t.text()).toEqual([...head, 'mid-aaaaa', 'mid-bbbbb', 'tail-aaaa', 'tail-bbbb']);
		expect(t.text().join('\n') + '\n').toBe(t.art.string);
	});

	// Decision: an event that starts exactly where the console is held up to needs no read.
	it('applies a contiguous event without reading', async () => {
		const t = setup();
		t.art.append(['aaaa', 'bbbb']);
		await t.snapshot(0, [{ buildId: 'b1', consoleRef: 'ref-b1', offset: 10 }]);
		t.art.append(['cccc']);
		await t.lines(0, 'b1', 10, ['cccc']);
		expect(t.reads).toHaveLength(1);
		expect(t.text()).toEqual(['aaaa', 'bbbb', 'cccc']);
	});

	// Decision: offsets are bytes. An event of non-ASCII lines advances the position by
	// their UTF-8 bytes, so the next contiguous event is not mistaken for a gap.
	it('counts the position in bytes, not characters', async () => {
		const t = setup();
		await t.lines(0, 'b1', 0, ['año', '😀']); // 5 + 5 bytes
		t.art.append(['año', '😀', 'next']);
		await t.lines(0, 'b1', 10, ['next']);
		expect(t.reads).toHaveLength(0);
		expect(t.text()).toEqual(['año', '😀', 'next']);
	});

	// Decision: an event for a build first met in an event (it started after the watch
	// opened) is held from byte 0; one whose offset is past 0 is preceded by the start.
	it('reads what precedes the first event it sees of a build', async () => {
		const t = setup();
		t.art.append(['one', 'two']);
		t.art.append(['three']);
		await t.lines(0, 'b1', 8, ['three']);
		expect(t.reads).toEqual([{ offset: 0, limit: 8 }]);
		expect(t.text()).toEqual(['one', 'two', 'three']);
	});

	// Decision (ADR-0044 §3): after events.gap the client reads from its last offset. The
	// read can include bytes a later event repeats; no line is shown twice.
	it('reads on after a gap and shows no line twice', async () => {
		const t = setup();
		t.art.append(range(0, 3));
		await t.lines(0, 'b1', 0, range(0, 3));
		t.art.append(range(3, 6)); // events skipped
		t.art.append(range(6, 8)); // an event that is still on its way
		t.stream.emit(0, { topic: 'events.gap', type: 'gap', payload: { fromSeq: 2, toSeq: 3 } });
		await t.lines(0, 'b1', 6 * 11, range(6, 8));
		expect(t.reads.at(-1)).toEqual({ offset: 33, limit: PAGE });
		expect(t.text()).toEqual(range(0, 8));
		expect(t.text().join('\n') + '\n').toBe(t.art.string);
	});

	// Decision: a failed read changes nothing held, shows its code, and the next event
	// that skips ahead reads the same bytes again: the position moves only past bytes read.
	it('keeps its position when a read fails and recovers on the next event', async () => {
		const t = setup();
		t.art.append(range(0, 2));
		await t.lines(0, 'b1', 0, range(0, 2));
		t.art.append(range(2, 4));
		t.art.append(range(4, 5));
		let fail = true;
		t.calls.on('artifact.read', (p) => {
			if (fail) {
				fail = false;
				throw capError('ARTIFACT_NOT_FOUND');
			}
			const q = p as { offset: number; limit: number };
			return t.art.read(q.offset, q.limit);
		});
		await t.lines(0, 'b1', 44, range(4, 5));
		expect(t.view().error?.code).toBe('ARTIFACT_NOT_FOUND');
		expect(t.text()).toEqual(range(0, 2));
		await t.lines(0, 'b1', 44, range(4, 5));
		expect(t.view().error).toBeNull();
		expect(t.text()).toEqual(range(0, 5));
	});

	// Decision: a reconnect is a new session, so the watch starts again; the new snapshot
	// is ahead of what is held, and the lines written while the connection was down are read.
	it('watches again after a reconnect and catches up', async () => {
		const t = setup();
		t.art.append(range(0, 2));
		await t.snapshot(0, [{ buildId: 'b1', consoleRef: 'ref-b1', offset: 22 }]);
		t.stream.handlers[0]!.onEnd?.(new ConnectionError('lost'));
		expect(t.view().watch.state).toBe('waiting');
		t.art.append(range(2, 5)); // written while the connection was down
		t.model.onConnection('open');
		expect(t.stream.handlers).toHaveLength(2);
		await t.snapshot(1, [{ buildId: 'b1', consoleRef: 'ref-b1', offset: t.art.size }]);
		expect(t.text()).toEqual(range(0, 5));
		expect(t.view().watch.state).toBe('watching');
		expect(t.subOpts).toEqual([{ workspace: '/p' }, { workspace: '/p' }]);
		// A late event of the dead session is not applied.
		await t.lines(0, 'b1', 55, ['stale']);
		expect(t.text()).toEqual(range(0, 5));
	});

	// Decision: a build on screen that the new snapshot no longer lists ended while the
	// watch was down; what it wrote last is read, to the console's end, and it stops being live.
	it('reads the end of a build that finished while the watch was down', async () => {
		const t = setup();
		t.art.append(range(0, 2));
		await t.snapshot(0, [{ buildId: 'b1', consoleRef: 'ref-b1', offset: 22 }]);
		t.stream.handlers[0]!.onEnd?.(new ConnectionError('lost'));
		t.art.append(range(2, 4));
		t.art.growing = false;
		t.model.onConnection('open');
		await t.snapshot(1, []);
		expect(t.text()).toEqual(range(0, 4));
		expect(t.view().live).toBe(false);
	});

	// Decision: a tab opened mid-build shows the console's head and continues. A console
	// larger than a page is read from its last page (at a line start, never mid-line),
	// the cap keeps the last CONSOLE_LINES, and later events follow.
	it('shows the last lines of a large console opened mid-build and continues', async () => {
		const t = setup();
		t.art.append(range(0, 40_000)); // 440,000 bytes; a page back from the end lands inside a line
		await t.snapshot(0, [{ buildId: 'b1', consoleRef: 'ref-b1', offset: t.art.size }]);
		expect(t.reads).toEqual([{ offset: t.art.size - PAGE, limit: PAGE }]);
		expect(t.view().count).toBe(CONSOLE_LINES);
		expect(t.text()[0]).toBe(L(20_000));
		expect(t.text().at(-1)).toBe(L(39_999));
		expect(t.view().offset).toBe(20_000 * 11);
		expect(t.view().hasEarlier).toBe(true);

		t.art.append([L(40_000)]);
		await t.lines(0, 'b1', 40_000 * 11, [L(40_000)]);
		expect(t.reads).toHaveLength(1);
		expect(t.text().at(-1)).toBe(L(40_000));
		expect(t.view().count).toBe(CONSOLE_LINES);
		expect(t.text()[0]).toBe(L(20_001));
	});

	// Decision: the head of a console opened mid-build starts at a line. The page read back
	// from the snapshot starts at a byte, maybe inside a line, and the piece before the first
	// newline is not a line; the position of the first line is the one the lines really have.
	it('starts the head at a line, never inside one', async () => {
		const t = setup();
		const W = (i: number) => L(i) + 'x'.repeat(89); // 100 bytes with the newline
		t.art.append(Array.from({ length: 5_000 }, (_, i) => W(i))); // 500,000 bytes
		expect(t.art.size - PAGE).not.toBe(Math.floor((t.art.size - PAGE) / 100) * 100); // inside a line
		await t.snapshot(0, [{ buildId: 'b1', consoleRef: 'ref-b1', offset: t.art.size }]);
		const rows = t.text();
		const first = Number(rows[0]!.slice(5, 10));
		expect(rows[0]).toBe(W(first));
		expect(t.view().offset).toBe(first * 100);
		expect(rows).toEqual(Array.from({ length: 5_000 - first }, (_, i) => W(first + i)));
		expect(t.view().hasEarlier).toBe(true);
	});

	// Decision (D18, ADR-0044): the panel keeps the newest build's last 20,000 lines in
	// memory; the older ones are counted, not shown, and the position of the first line
	// held moves with them so "Earlier" can read them back.
	it('keeps the last 20,000 lines of a build', async () => {
		const t = setup();
		for (let k = 0; k < 5; k++) {
			await t.lines(0, 'b1', k * 5_000 * 11, range(k * 5_000, (k + 1) * 5_000));
		}
		expect(t.view().count).toBe(CONSOLE_LINES);
		expect(t.view().evicted).toBe(5_000);
		expect(t.view().startSeq).toBe(5_000);
		expect(t.view().offset).toBe(5_000 * 11);
		expect(t.text()[0]).toBe(L(5_000));
		expect(t.text().at(-1)).toBe(L(24_999));
		expect(t.view().hasEarlier).toBe(true);
	});

	// Decision (plan m2b D14, kept): "Earlier" pages back from the first line held, one
	// page at a time, each starting at a line, until byte 0.
	it('pages back to the start', async () => {
		const t = setup();
		t.art.append(range(0, 100_000)); // 1.1 MB: the pages back start inside lines
		t.art.growing = false;
		t.src.set({ last: { buildId: 'b1', consoleRef: 'ref-b1' } });
		await settle();
		expect(t.reads).toEqual([{ offset: -PAGE, limit: PAGE }]);
		expect(t.view().hasEarlier).toBe(true);
		for (let i = 0; i < 8 && t.view().hasEarlier; i++) await t.model.earlier();
		expect(t.view().offset).toBe(0);
		expect(t.view().hasEarlier).toBe(false);
		expect(t.text()).toEqual(range(0, 100_000));
		const before = t.reads.length;
		await t.model.earlier();
		expect(t.reads).toHaveLength(before);
	});

	// Decision: an MCP (or CLI) build is shown like the UI's own: the console follows the
	// workspace's builds whoever started them, with no help from the activity feed.
	it('shows the lines of a build another client started', async () => {
		const t = setup();
		expect(t.src.view.mirror.building).toBeNull();
		await t.lines(0, 'op-mcp', 0, ['> Task :app:compileDebugKotlin']);
		expect(t.view().buildId).toBe('op-mcp');
		expect(t.view().live).toBe(true);
		expect(t.text()).toEqual(['> Task :app:compileDebugKotlin']);
	});

	// Decision: the newest build is the one shown; a late event of an older build does
	// not bring it back.
	it('shows the newest build and ignores an older one\'s late events', async () => {
		const t = setup();
		await t.lines(0, 'b1', 0, ['old']);
		await t.lines(0, 'b2', 0, ['new']);
		await t.lines(0, 'b1', 4, ['late']);
		expect(t.view().buildId).toBe('b2');
		expect(t.text()).toEqual(['new']);
	});

	// Decision: when the build on screen ends and its result becomes the last build, the
	// lines held are kept: reading its console again would be a read nobody needs.
	it('keeps the lines of a build that ended instead of reading it again', async () => {
		const t = setup();
		t.src.set({ mirror: { building: { id: 'op' } } });
		await t.lines(0, 'b1', 0, ['a', 'b']);
		t.src.set({ mirror: { building: null } });
		expect(t.view().live).toBe(false);
		// Its console no longer grows: the status says so (level 5 showed "still growing").
		expect(t.view().growing).toBe(false);
		expect(t.view().size).toBe(4);
		t.src.set({ last: { buildId: 'b1', consoleRef: 'ref-b1' } });
		await settle();
		expect(t.reads).toHaveLength(0);
		expect(t.text()).toEqual(['a', 'b']);
	});

	// Decision: while a build runs, the last finished build is older and is not shown over
	// it, even when its result is read after the live lines started.
	it('does not show the previous build over a running one', async () => {
		const t = setup();
		t.src.set({ mirror: { building: { id: 'op' } } });
		await t.lines(0, 'b2', 0, ['live']);
		t.src.set({ last: { buildId: 'b1', consoleRef: 'ref-b1' } });
		t.src.set({ mirror: { building: null } });
		await settle();
		expect(t.reads).toHaveLength(0);
		expect(t.text()).toEqual(['live']);
	});

	// Decision: lines reach the panel once per animation frame, however many events arrived
	// in it (as Logcat's batcher, plan m2a D8).
	it('tells the panel once per frame', async () => {
		const t = setup();
		await t.lines(0, 'b1', 0, ['a']);
		const base = t.notified;
		let off = 2;
		for (let i = 0; i < 5; i++) {
			await t.lines(0, 'b1', off, ['b']);
			off += 2;
		}
		expect(t.notified).toBe(base);
		expect(t.frames.pending).toBe(1);
		t.frames.tick();
		expect(t.notified).toBe(base + 1);
		expect(t.view().count).toBe(6);
	});

	// Decision: the watch belongs to the open workspace: none while none is open, started
	// with the workspace's root, and ended in the daemon when the workspace changes.
	it('watches the open workspace only', async () => {
		const t = setup();
		t.ws.set(null);
		expect(t.view().watch.state).toBe('idle');
		expect(t.stream.handlers).toHaveLength(1); // the one the first workspace started
		t.ws.set('w2');
		expect(t.stream.handlers).toHaveLength(2);
		expect(t.subOpts.at(-1)).toEqual({ workspace: '/p' });
		await t.snapshot(1, []);
		expect(t.view().watch.state).toBe('watching');
		t.ws.set('w3');
		expect(t.stream.unsubscribed).toContain('w2');
		expect(t.view().hasBuild).toBe(false);
	});

	// Decision: an answer that arrives after the watch was replaced is ended at once.
	it('ends a watch whose answer came after it was replaced', async () => {
		const t = setup();
		t.ws.set('w2');
		await t.snapshot(0, []);
		expect(t.stream.unsubscribed).toEqual(['w1']);
		expect(t.view().watch.state).toBe('starting');
	});

	// Decision (plan m2b D14, kept): the console of a build that ended before the page
	// loaded is read from its end, and a failed read shows its code without inventing text.
	it('reads the last build\'s console from the end and shows a failed read as an error', async () => {
		const t = setup();
		t.art.append(['x']);
		t.art.growing = false;
		t.src.set({ last: { buildId: 'b1', consoleRef: 'ref-b1' } });
		await settle();
		expect(t.text()).toEqual(['x']);
		const f = setup();
		f.calls.on('artifact.read', () => {
			throw capError('ARTIFACT_NOT_FOUND');
		});
		f.src.set({ last: { buildId: 'b1', consoleRef: 'ref-b1' } });
		await settle();
		expect(f.view().error?.code).toBe('ARTIFACT_NOT_FOUND');
		expect(f.text()).toEqual([]);
	});
});
