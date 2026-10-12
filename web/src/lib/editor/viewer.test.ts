import { describe, expect, it, vi } from 'vitest';
import { CapabilityError } from '../api/client';
import type { Params, Result } from '../api/capabilities.gen';
import { capError } from '../build/testing';
import { FileOps, type OpsClient } from '../files/ops';
import { CAP, EditorModel, PAGE, RESTARTS, ViewerSet, formatSize, readOnlyText, type LoadBuffers, type ViewerClient } from './viewer';
import { MAX_WRITE, sha256Hex } from './text';

type Page = Result<'file.read'>;
type CmBuffer = import('./cm/buffer').CmBuffer;

/** The real buffers, reached the way the app reaches them: by a dynamic import. */
const cmBuffers: LoadBuffers = async () => {
	const m = await import('./cm/buffer');
	return { createBuffer: (o) => new m.CmBuffer(o) };
};

const enc = new TextEncoder();
// ignoreBOM: keep a leading U+FEFF as text, as the daemon's JSON does.
const dec = new TextDecoder('utf-8', { ignoreBOM: true });

/** A daemon with files: pages by the offset and limit asked for, ending at a line, each with the whole file's revision. */
class Daemon implements ViewerClient {
	files = new Map<string, string>();
	binary = new Set<string>();
	noRevision = new Set<string>();
	reads: Params<'file.read'>[] = [];
	writes: Params<'file.write'>[] = [];
	// A hook to change a page on its way out (a call's number counts the reads of the path).
	tamper: ((page: Page, n: number) => Page) | null = null;
	// A write that has replaced the file waits for this before it answers, so a test can act while it is in flight.
	writeGate: Promise<void> | null = null;
	writeFails: CapabilityError | null = null;
	#n = 0;

	call = (async (name: string, p: Params<'file.read'> & Params<'file.write'>) => {
		if (name === 'file.write') return this.#write(p);
		return this.#read(p);
	}) as ViewerClient['call'];

	async revision(path: string): Promise<string> {
		const t = this.files.get(path);
		return t === undefined ? 'absent' : sha256Hex(t);
	}

	async #read(p: Params<'file.read'>): Promise<Page> {
		this.reads.push(p);
		const text = this.files.get(p.path);
		if (text === undefined) throw capError('FILE_NOT_FOUND', 'no such file');
		const bytes = enc.encode(text);
		const offset = p.offset ?? 0;
		const revision = this.noRevision.has(p.path) ? undefined : await sha256Hex(text);
		if (this.binary.has(p.path)) {
			return { path: p.path, size: bytes.length, modTime: 't', offset, nextOffset: bytes.length, binary: true, ...(revision ? { revision } : {}) };
		}
		let end = Math.min(bytes.length, offset + (p.limit ?? 16384));
		if (end < bytes.length) {
			while (end > offset && bytes[end - 1] !== 10) end--;
			if (end === offset) end = Math.min(bytes.length, offset + (p.limit ?? 16384));
		}
		const out: Page = {
			path: p.path,
			size: bytes.length,
			modTime: 't',
			offset,
			nextOffset: end,
			text: dec.decode(bytes.slice(offset, end)),
			...(revision ? { revision } : {}),
		};
		return this.tamper ? this.tamper(out, this.#n++) : out;
	}

	async #write(p: Params<'file.write'>): Promise<Result<'file.write'>> {
		this.writes.push(p);
		if (this.writeFails) throw this.writeFails;
		if (p.ifRevision !== (await this.revision(p.path))) throw capError('FILE_REVISION_CONFLICT', 'the file changed');
		this.files.set(p.path, p.content);
		// The file is replaced; the answer is on its way until the gate opens.
		await this.writeGate;
		return { path: p.path, size: enc.encode(p.content).length, revision: await sha256Hex(p.content) };
	}
}

const ws = { root: '/proj' };

/** The deferred promise a test releases by hand. */
function gate() {
	let open!: () => void;
	const promise = new Promise<void>((r) => (open = r));
	return { promise, open };
}

async function ready(m: EditorModel) {
	await vi.waitFor(() => expect(m.view.state).not.toBe('loading'));
}

async function opened(text: string, path = 'a.kt', configure?: (d: Daemon) => void) {
	const d = new Daemon();
	d.files.set(path, text);
	configure?.(d);
	const m = new EditorModel(d, ws, path, cmBuffers);
	await m.load();
	return { d, m };
}

/** A user's typing into the model's buffer. */
function type(m: EditorModel, at: number, insert: string, del = 0) {
	(m.view.buffer as unknown as CmBuffer).dispatch({ changes: { from: at, to: at + del, insert }, userEvent: 'input.type' });
}

const text = (m: EditorModel) => m.view.buffer!.text();

describe('EditorModel read (D5, D6, D26)', () => {
	// Decision (D26): pages are read in order and joined at their offsets, so the text
	// shown is the file's, not a page's.
	it('joins the pages of a file larger than one page, in order', async () => {
		const line = 'x'.repeat(99) + '\n';
		const n = Math.ceil((PAGE * 2.5) / 100);
		const { d, m } = await opened(line.repeat(n), 'a.txt');
		expect(m.view.state).toBe('ready');
		expect(text(m)).toBe(line.repeat(n));
		expect(m.view.capped).toBe(false);
		expect(d.reads.length).toBeGreaterThanOrEqual(3);
		const offs = d.reads.map((c) => c.offset);
		expect(offs[0]).toBe(0);
		expect(offs.every((o, i) => i === 0 || o! > offs[i - 1]!)).toBe(true);
		expect(d.reads.every((c) => c.limit === PAGE)).toBe(true);
		// 640 KiB over three pages is complete and checked, but over 512 KiB: shown, not edited.
		expect(m.view.readOnly).toBe('large');
	});

	// Decision (D5, D26): over 512 KiB the file is shown and never saved; past the cap the
	// read stops and says so, with its own reason.
	it('opens a file over 512 KiB read-only, and a capped read read-only', async () => {
		const line = 'y'.repeat(99) + '\n';
		const big = await opened(line.repeat(Math.ceil((MAX_WRITE * 1.5) / 100)), 'big.log');
		expect(big.m.view.readOnly).toBe('large');
		expect(big.m.view.capped).toBe(false);
		expect(big.m.view.buffer!.dirty).toBe(false);

		const huge = await opened(line.repeat(Math.ceil((CAP * 2) / 100)), 'huge.log');
		expect(huge.m.view.capped).toBe(true);
		expect(huge.m.view.readOnly).toBe('capped');
		expect(huge.m.view.read).toBeGreaterThanOrEqual(CAP);
		expect(huge.m.view.read).toBeLessThan(CAP + PAGE);
		expect(huge.m.view.size).toBeGreaterThan(CAP);
		expect(huge.d.reads.length).toBeLessThanOrEqual(CAP / PAGE + 1);
		// The incomplete text is not hashed against the whole file's revision: no 'unverified'.
		expect(huge.m.view.readOnly).not.toBe('unverified');
	});

	// Decision (Q4): a binary file is a placeholder with its size and holds no buffer.
	it('keeps no buffer for a binary file', async () => {
		const { m } = await opened('PNG\u0000data', 'a.png', (d) => d.binary.add('a.png'));
		expect(m.view.state).toBe('binary');
		expect(m.view.buffer).toBeNull();
		expect(m.view.size).toBe(8);
	});

	// Decision (D5): mixed endings and a lone CR are shown and never saved; a revision-less
	// read neither; and none of them is called unverified, since their text is complete and
	// (or) there is nothing to check it against.
	it('opens mixed endings, a lone CR and a revision-less read read-only with their own reasons', async () => {
		const mixed = await opened('a\r\nb\nc\n', 'm.txt');
		expect(mixed.m.view.readOnly).toBe('mixed');
		const cr = await opened('a\rb\r', 'cr.txt');
		expect(cr.m.view.readOnly).toBe('mixed');
		const none = await opened('plain\n', 'n.txt', (d) => d.noRevision.add('n.txt'));
		expect(none.m.view.readOnly).toBe('norevision');
		expect(none.m.base).toBeUndefined();
		for (const reason of ['large', 'capped', 'norevision', 'mixed', 'unverified'] as const) expect(readOnlyText(reason)).toMatch(/^Read-only/);
	});

	// Decision (D6): a complete read's revision is the buffer's base, and it equals the SHA-256 of the text read.
	it('takes the complete, checked read’s revision as the base', async () => {
		const { d, m } = await opened('hello\n');
		expect(m.view.readOnly).toBeNull();
		expect(m.base).toBe(await d.revision('a.kt'));
	});

	// Decision (D6): a page whose revision differs from the first page's means the file
	// changed under the read: start over; at most three times, then fail with a message.
	it('restarts when a page carries another revision, and fails after three restarts', async () => {
		const line = 'z'.repeat(99) + '\n';
		const body = line.repeat(Math.ceil((PAGE * 1.5) / 100));
		let first = true;
		const once = await opened(body, 'c.txt', (d) => {
			d.tamper = (page) => {
				if (first && page.offset > 0) {
					first = false;
					return { ...page, revision: 'f'.repeat(64) };
				}
				return page;
			};
		});
		expect(once.m.view.state).toBe('ready');
		expect(once.d.reads.filter((c) => c.offset === 0).length).toBe(2);

		const always = await opened(body, 'c.txt', (d) => {
			d.tamper = (page) => (page.offset > 0 ? { ...page, revision: 'e'.repeat(64) } : page);
		});
		expect(always.m.view.state).toBe('error');
		expect(always.m.view.error).toContain('kept changing');
		expect(always.d.reads.filter((c) => c.offset === 0).length).toBe(RESTARTS + 1);
	});

	// Decision (D6, old): a page that is not at the offset asked for restarts the read too.
	it('restarts when a page is not at the offset asked for', async () => {
		const line = 'z'.repeat(99) + '\n';
		let first = true;
		const { d, m } = await opened(line.repeat(Math.ceil((PAGE * 1.5) / 100)), 'c.txt', (d) => {
			d.tamper = (page) => {
				if (!first || page.offset === 0) return page;
				first = false;
				return { ...page, offset: page.offset + 1 };
			};
		});
		expect(m.view.state).toBe('ready');
		expect(d.reads.filter((c) => c.offset === 0).length).toBe(2);
	});

	// Decision (D6): every page agrees but the text does not hash to the revision (a final
	// line cut mid-rune comes back with U+FFFD): open read-only, not an error and not editable.
	it('opens read-only when the complete read does not hash to its revision', async () => {
		const { m } = await opened('hello\n', 'a.kt', (d) => {
			d.tamper = (page) => ({ ...page, text: 'hell�\n' });
		});
		expect(m.view.state).toBe('ready');
		expect(m.view.readOnly).toBe('unverified');
	});

	it('shows a failure as text', async () => {
		const d = new Daemon();
		const m = new EditorModel(d, ws, 'missing', cmBuffers);
		await m.load();
		expect(m.view.state).toBe('error');
		expect(m.view.error).toContain('FILE_NOT_FOUND');
	});
});

describe('EditorModel separators (D5)', () => {
	// Decision: a save writes the bytes it read plus the edits, for LF, CRLF, a BOM and a
	// file with no break alike.
	it.each([
		['LF', 'one\ntwo\n', 'one\nTWO\n'],
		['CRLF', 'one\r\ntwo\r\n', 'one\r\nTWO\r\n'],
		['a BOM', '﻿one\ntwo\n', '﻿one\nTWO\n'],
		['no break', 'one two', 'one TWO'],
	])('round-trips %s byte for byte, with the edit inside it', async (_n, original, expected) => {
		const { d, m } = await opened(original);
		expect(text(m)).toBe(original);
		const at = original.indexOf('two');
		type(m, original.slice(0, at).replace(/\r\n/g, '\n').length, 'TWO', 3);
		expect(m.view.dirty).toBe(true);
		await m.save();
		expect(d.files.get('a.kt')).toBe(expected);
		expect(m.view.dirty).toBe(false);
	});
});

describe('EditorModel save (D7)', () => {
	// Decision: the save names the revision the buffer is based on, and the result's
	// revision is the next base. Seen failing without ifRevision (the conflict test below).
	it('sends ifRevision = base, then adopts the revision the result returns', async () => {
		const { d, m } = await opened('abc\n');
		const base = m.base!;
		type(m, 3, 'd');
		await m.save();
		expect(d.writes).toEqual([{ path: 'a.kt', content: 'abcd\n', ifRevision: base }]);
		expect(m.base).toBe(await sha256Hex('abcd\n'));
		expect(m.base).not.toBe(base);
		expect(m.view.dirty).toBe(false);
		expect(m.view.saveError).toBeNull();
		// The adopted base is what the next save names.
		type(m, 4, 'e');
		await m.save();
		expect(d.writes[1]!.ifRevision).toBe(await sha256Hex('abcd\n'));
	});

	it('does not call when nothing changed or the file is read-only', async () => {
		const { d, m } = await opened('abc\n');
		await m.save();
		const ro = await opened('a\r\nb\nc\n', 'm.txt');
		await ro.m.save();
		expect(d.writes).toEqual([]);
		expect(ro.d.writes).toEqual([]);
	});

	// Decision: an edit typed while the save is in flight is not covered by it and stays dirty.
	it('keeps an edit made during the save dirty', async () => {
		const { d, m } = await opened('abc\n');
		const g = gate();
		d.writeGate = g.promise;
		type(m, 3, 'd');
		const saving = m.save();
		await vi.waitFor(() => expect(d.writes).toHaveLength(1));
		expect(m.view.saving).toBe(true);
		type(m, 4, 'e');
		g.open();
		await saving;
		expect(d.files.get('a.kt')).toBe('abcd\n');
		expect(text(m)).toBe('abcde\n');
		expect(m.view.dirty).toBe(true);
		expect(m.view.saving).toBe(false);
	});

	// Decision: a second save during one waits (never concurrent) and goes out with the new base.
	it('queues a second save behind the first and sends the new base', async () => {
		const { d, m } = await opened('abc\n');
		const g = gate();
		d.writeGate = g.promise;
		type(m, 3, 'd');
		const first = m.save();
		await vi.waitFor(() => expect(d.writes).toHaveLength(1));
		type(m, 4, 'e');
		const second = m.save();
		await new Promise((r) => setTimeout(r, 20));
		expect(d.writes).toHaveLength(1); // not concurrent
		d.writeGate = null;
		g.open();
		await Promise.all([first, second]);
		expect(d.writes).toHaveLength(2);
		expect(d.writes[1]).toEqual({ path: 'a.kt', content: 'abcde\n', ifRevision: await sha256Hex('abcd\n') });
		expect(d.files.get('a.kt')).toBe('abcde\n');
		expect(m.view.dirty).toBe(false);
	});

	it('does not send a queued save when nothing is left to save', async () => {
		const { d, m } = await opened('abc\n');
		const g = gate();
		d.writeGate = g.promise;
		type(m, 3, 'd');
		const first = m.save();
		await vi.waitFor(() => expect(d.writes).toHaveLength(1));
		const second = m.save();
		g.open();
		await Promise.all([first, second]);
		expect(d.writes).toHaveLength(1);
	});

	// Decision (D7): a refused request is a message and no call: over 512 KiB, or JSON over 1,000,000 bytes.
	it('makes no call for a save over 512 KiB or whose escaped JSON is too large', async () => {
		const near = 'a'.repeat(MAX_WRITE - 10);
		const big = await opened(near);
		type(big.m, 5, 'b'.repeat(100));
		await big.m.save();
		expect(big.d.writes).toEqual([]);
		expect(big.m.view.saveError).toMatch(/512 KiB/);
		expect(big.m.view.dirty).toBe(true);

		const esc = await opened('x');
		type(esc.m, 1, '\u0001'.repeat(200_000));
		await esc.m.save();
		expect(esc.d.writes).toEqual([]);
		expect(esc.m.view.saveError).toMatch(/1,000,000/);
	});

	// Decision: a conflict keeps the buffer as it is, shows the daemon's message and sets the
	// conflict state; the next save is not retried blindly.
	it('sets the conflict state when the daemon answers FILE_REVISION_CONFLICT, keeping the buffer', async () => {
		const { d, m } = await opened('abc\n');
		d.files.set('a.kt', 'someone else\n'); // the disk moved on after the read
		type(m, 3, 'd');
		await m.save();
		expect(d.writes).toHaveLength(1);
		expect(m.view.conflict).toBe(true);
		expect(m.view.saveError).toContain('FILE_REVISION_CONFLICT');
		expect(m.view.dirty).toBe(true);
		expect(text(m)).toBe('abcd\n');
		expect(d.files.get('a.kt')).toBe('someone else\n');
		expect(m.view.saving).toBe(false);
	});

	it('shows another failure as text without the conflict state', async () => {
		const { d, m } = await opened('abc\n');
		d.writeFails = capError('FILE_IO_FAILED', 'disk full');
		type(m, 3, 'd');
		await m.save();
		expect(m.view.saveError).toContain('FILE_IO_FAILED');
		expect(m.view.conflict).toBe(false);
		expect(m.view.dirty).toBe(true);
	});

	// Decision (D8): the revision of the write in flight is our own echo. Here the buffer was
	// undone back to the base text while the save was out, so a clean buffer meets an event
	// carrying the saved revision; it must not swap the text to the saved text.
	it('treats a watch event carrying the in-flight save’s revision as its echo', async () => {
		const d = new Daemon();
		d.files.set('a.kt', 'abc\n');
		const set = new ViewerSet(d, ws, {}, cmBuffers);
		const model = set.get('a.kt');
		await ready(model);
		const g = gate();
		d.writeGate = g.promise;
		type(model, 3, 'd');
		const saving = model.save();
		await vi.waitFor(() => expect(d.writes).toHaveLength(1));
		type(model, 3, '', 1); // back to the base text: clean while the write is out
		expect(model.view.dirty).toBe(false);
		expect(d.files.get('a.kt')).toBe('abcd\n'); // the daemon has already replaced the file
		const readsBefore = d.reads.length;
		set.changes([{ path: 'a.kt', kind: 'modified', type: 'file' }]);
		await vi.waitFor(() => expect(d.reads.length).toBeGreaterThan(readsBefore));
		await new Promise((r) => setTimeout(r, 20));
		expect(text(model)).toBe('abc\n'); // not swapped to the saved text
		const readsAfterEcho = d.reads.length;
		g.open();
		await saving;
		await new Promise((r) => setTimeout(r, 20));
		// The result arrives: the base is the saved text, so the undone buffer differs from it.
		expect(model.view.dirty).toBe(true);
		expect(text(model)).toBe('abc\n');
		expect(model.base).toBe(await sha256Hex('abcd\n'));
		// And no read was queued behind the echo.
		expect(d.reads.length).toBe(readsAfterEcho);
	});
});

describe('ViewerSet external changes (D8, step 6)', () => {
	async function open(text = 'one\ntwo\n') {
		const d = new Daemon();
		d.files.set('a.kt', text);
		const hooks: [string, boolean][] = [];
		const set = new ViewerSet(d, ws, { dirty: (p, v) => hooks.push([p, v]) }, cmBuffers);
		const m = set.get('a.kt');
		await ready(m);
		return { d, set, m, hooks };
	}

	// Decision: a `modified` event for the path reads it again, and no other.
	it('takes the disk’s text silently into a clean buffer, for its path only', async () => {
		const { d, set, m } = await open();
		d.files.set('a.kt', 'one\ntwo\nthree\n');
		set.changes([{ path: 'other.kt', kind: 'modified', type: 'file' }]);
		await new Promise((r) => setTimeout(r, 20));
		expect(text(m)).toBe('one\ntwo\n');
		set.changes([{ path: 'a.kt', kind: 'modified', type: 'file' }]);
		await vi.waitFor(() => expect(text(m)).toBe('one\ntwo\nthree\n'));
		expect(m.view.dirty).toBe(false);
		expect(m.base).toBe(await d.revision('a.kt'));
	});

	// Decision (D8, step 6 part): a dirty buffer is never reloaded by an event or a re-sync.
	// Seen failing when the dirty check is removed from the reload.
	it('does not reload a dirty buffer on a modified event or a re-sync', async () => {
		const { d, set, m } = await open();
		type(m, 0, 'MINE ');
		const base = m.base;
		d.files.set('a.kt', 'theirs\n');
		const before = d.reads.length;
		set.changes([{ path: 'a.kt', kind: 'modified', type: 'file' }]);
		await vi.waitFor(() => expect(d.reads.length).toBeGreaterThan(before));
		await new Promise((r) => setTimeout(r, 20));
		expect(text(m)).toBe('MINE one\ntwo\n');
		set.resync();
		await new Promise((r) => setTimeout(r, 20));
		expect(text(m)).toBe('MINE one\ntwo\n');
		expect(m.view.dirty).toBe(true);
		expect(m.base).toBe(base);
	});

	// Decision: the save of the dirty buffer is what catches the difference.
	it('lets the save of a dirty buffer meet the changed disk as a conflict', async () => {
		const { d, set, m } = await open();
		type(m, 0, 'MINE ');
		d.files.set('a.kt', 'theirs\n');
		set.changes([{ path: 'a.kt', kind: 'modified', type: 'file' }]);
		await m.save();
		expect(m.view.conflict).toBe(true);
		expect(d.files.get('a.kt')).toBe('theirs\n');
	});

	// Decision: our own write's echo (same revision as the base) changes nothing.
	it('treats an event with the revision the buffer is based on as an echo', async () => {
		const { d, set, m } = await open();
		type(m, 0, 'X');
		await m.save();
		const buffer = m.view.buffer;
		const before = d.reads.length;
		set.changes([{ path: 'a.kt', kind: 'modified', type: 'file' }]);
		await vi.waitFor(() => expect(d.reads.length).toBeGreaterThan(before));
		await new Promise((r) => setTimeout(r, 20));
		expect(m.view.buffer).toBe(buffer);
		expect(text(m)).toBe('Xone\ntwo\n');
		expect(m.view.dirty).toBe(false);
	});

	// Decision: a read without a revision is never an echo (undefined is not equal to
	// undefined): the clean read-only view takes the new text.
	it('replaces a clean read-only view of a revision-less file', async () => {
		const d = new Daemon();
		d.files.set('n.txt', 'one\n');
		d.noRevision.add('n.txt');
		const set = new ViewerSet(d, ws, {}, cmBuffers);
		const m = set.get('n.txt');
		await ready(m);
		expect(m.view.readOnly).toBe('norevision');
		d.files.set('n.txt', 'two\n');
		set.changes([{ path: 'n.txt', kind: 'modified', type: 'file' }]);
		await vi.waitFor(() => expect(text(m)).toBe('two\n'));
		expect(m.view.readOnly).toBe('norevision');
	});

	// Decision (D7): a `created` whose revision equals the base clears a `deleted` mark (a move's own events).
	it('clears the deleted mark when the file returns with the same revision', async () => {
		const { set, m } = await open();
		set.changes([{ path: 'a.kt', kind: 'deleted', type: 'file' }]);
		expect(m.view.state).toBe('deleted');
		expect(text(m)).toBe('one\ntwo\n'); // the last text stays
		set.changes([{ path: 'a.kt', kind: 'created', type: 'file' }]);
		await vi.waitFor(() => expect(m.view.state).toBe('ready'));
		expect(text(m)).toBe('one\ntwo\n');
	});

	it('drops a read that a deletion overtook', async () => {
		const { d, set, m } = await open();
		d.files.set('a.kt', 'new\n');
		set.changes([{ path: 'a.kt', kind: 'modified', type: 'file' }]);
		set.changes([{ path: 'a.kt', kind: 'deleted', type: 'file' }]);
		await new Promise((r) => setTimeout(r, 30));
		expect(m.view.state).toBe('deleted');
		expect(text(m)).toBe('one\ntwo\n');
	});

	it('reads every open file again on a re-sync', async () => {
		const { d, set, m } = await open();
		d.files.set('a.kt', 'changed\n');
		set.resync();
		await vi.waitFor(() => expect(text(m)).toBe('changed\n'));
	});

	it('forgets the models of closed tabs, but not one with unsaved changes', async () => {
		const { set, m } = await open();
		set.keep([]);
		expect(set.get('a.kt')).not.toBe(m);
		const second = set.get('a.kt');
		await ready(second);
		type(second, 0, 'X');
		set.keep([]);
		expect(set.get('a.kt')).toBe(second);
		set.discard(['a.kt']);
		expect(set.get('a.kt')).not.toBe(second);
	});

	it('tells its owner when a buffer becomes dirty and clean', async () => {
		const { m, hooks } = await open();
		type(m, 0, 'X');
		await m.save();
		expect(hooks).toEqual([
			['a.kt', true],
			['a.kt', false],
		]);
	});
});

describe('ViewerSet fix round (step 6 verifier)', () => {
	async function open(text = 'abc\n') {
		const d = new Daemon();
		d.files.set('a.kt', text);
		const set = new ViewerSet(d, ws, {}, cmBuffers);
		const m = set.get('a.kt');
		await ready(m);
		return { d, set, m };
	}

	// Decision: during a save only the in-flight write's revision is our echo. The text the
	// buffer is based on appearing on disk then is somebody writing the old bytes back (an
	// agent), which must not be swallowed: the file is read again after the save. Seen failing
	// with `=== this.#base` counted as an echo (the buffer kept the saved text).
	it('re-reads after a save when the old text is written back during it', async () => {
		const { d, set, m } = await open('abc\n');
		const g = gate();
		d.writeGate = g.promise;
		type(m, 3, 'd');
		const saving = m.save();
		await vi.waitFor(() => expect(d.writes).toHaveLength(1));
		d.files.set('a.kt', 'abc\n'); // an agent overwrites our write with the old bytes
		set.changes([{ path: 'a.kt', kind: 'modified', type: 'file' }]);
		await new Promise((r) => setTimeout(r, 30));
		g.open();
		await saving;
		await vi.waitFor(() => expect(text(m)).toBe('abc\n'));
		expect(m.view.dirty).toBe(false);
		expect(m.base).toBe(await sha256Hex('abc\n'));
	});

	// Decision: a clean silent reload clears the failed save's message and conflict flag.
	it('clears the save error and the conflict when a clean buffer takes the disk’s text', async () => {
		const { d, set, m } = await open('abc\n');
		d.files.set('a.kt', 'theirs\n');
		type(m, 3, 'd');
		await m.save();
		expect(m.view.conflict).toBe(true);
		type(m, 3, '', 1); // back to the text read: clean
		expect(m.view.dirty).toBe(false);
		set.changes([{ path: 'a.kt', kind: 'modified', type: 'file' }]);
		await vi.waitFor(() => expect(text(m)).toBe('theirs\n'));
		expect(m.view.conflict).toBe(false);
		expect(m.view.saveError).toBeNull();
	});

	// Decision: a dirty buffer whose file comes back is not "deleted" any more; its text is kept.
	it('clears the deleted mark of a dirty buffer when its file returns different', async () => {
		const { d, set, m } = await open('abc\n');
		type(m, 0, 'MINE ');
		set.changes([{ path: 'a.kt', kind: 'deleted', type: 'file' }]);
		expect(m.view.state).toBe('deleted');
		d.files.set('a.kt', 'back different\n');
		set.changes([{ path: 'a.kt', kind: 'created', type: 'file' }]);
		await vi.waitFor(() => expect(m.view.state).toBe('ready'));
		expect(text(m)).toBe('MINE abc\n');
		expect(m.view.dirty).toBe(true);
	});

	// Decision (D7): a move's events may reach the model before the move's reply: it was marked
	// deleted at the old path, and the rename must read the new path to clear the mark.
	it('re-reads a model renamed while marked deleted', async () => {
		const { d, set, m } = await open('abc\n');
		set.changes([{ path: 'a.kt', kind: 'deleted', type: 'file' }]);
		expect(m.view.state).toBe('deleted');
		d.files.set('b.kt', 'abc\n');
		d.files.delete('a.kt');
		set.moved('a.kt', 'b.kt');
		await vi.waitFor(() => expect(m.view.state).toBe('ready'));
		expect(m.path).toBe('b.kt');
	});

	it('keeps a renamed model marked deleted when the new path does not exist either', async () => {
		const { d, set, m } = await open('abc\n');
		set.changes([{ path: 'a.kt', kind: 'deleted', type: 'file' }]);
		d.files.delete('a.kt');
		set.moved('a.kt', 'b.kt');
		await new Promise((r) => setTimeout(r, 30));
		expect(m.view.state).toBe('deleted');
	});
});

describe('ViewerSet moves (D7)', () => {
	async function twoOpen() {
		const d = new Daemon();
		d.files.set('dir/a.kt', 'alpha\n');
		d.files.set('dir/sub/b.kt', 'beta\n');
		d.files.set('dir2/c.kt', 'gamma\n');
		const set = new ViewerSet(d, ws, {}, cmBuffers);
		const a = set.get('dir/a.kt');
		const b = set.get('dir/sub/b.kt');
		const c = set.get('dir2/c.kt');
		for (const m of [a, b, c]) await ready(m);
		return { d, set, a, b, c };
	}

	// Decision (D7, review B3): a rename keeps the open buffer with its edits, base and
	// history. Seen failing with `moved` doing nothing: the file's new path then had a fresh model.
	it('re-keys a renamed dirty file keeping the buffer, its base and its edits', async () => {
		const { set, a } = await twoOpen();
		type(a, 0, 'EDIT ');
		const buffer = a.view.buffer;
		const base = a.base;
		set.moved('dir/a.kt', 'dir/renamed.kt');
		set.keep(['dir/renamed.kt']);
		const m = set.get('dir/renamed.kt');
		expect(m).toBe(a);
		expect(m.path).toBe('dir/renamed.kt');
		expect(m.view.buffer).toBe(buffer);
		expect(m.view.dirty).toBe(true);
		expect(m.base).toBe(base);
		expect(text(m)).toBe('EDIT alpha\n');
	});

	it('re-keys the files under a renamed folder, and not a sibling that shares the prefix', async () => {
		const { d, set, a, b, c } = await twoOpen();
		type(a, 0, 'A');
		type(b, 0, 'B');
		// The daemon moves the files, then the tree tells the set.
		d.files.set('moved/a.kt', d.files.get('dir/a.kt')!);
		d.files.set('moved/sub/b.kt', d.files.get('dir/sub/b.kt')!);
		set.moved('dir', 'moved');
		set.keep(['moved/a.kt', 'moved/sub/b.kt', 'dir2/c.kt']);
		expect(set.get('moved/a.kt')).toBe(a);
		expect(set.get('moved/sub/b.kt')).toBe(b);
		expect(set.get('dir2/c.kt')).toBe(c);
		expect(a.view.dirty && b.view.dirty).toBe(true);
		// The saves go to the new paths.
		await set.saveAll(['moved/a.kt', 'moved/sub/b.kt']);
		expect(d.writes.map((w) => w.path)).toEqual(['moved/a.kt', 'moved/sub/b.kt']);
	});

	it('replaces a clean model at the target by the moved one', async () => {
		const { set, a } = await twoOpen();
		const there = set.get('dir/there.kt');
		set.moved('dir/a.kt', 'dir/there.kt');
		expect(set.get('dir/there.kt')).toBe(a);
		expect(set.get('dir/there.kt')).not.toBe(there);
	});

	it('knows which paths hold unsaved changes, a folder included', async () => {
		const { set, b } = await twoOpen();
		type(b, 0, 'B');
		expect(set.holdsDirty('dir/sub/b.kt')).toBe(true);
		expect(set.holdsDirty('dir')).toBe(true);
		expect(set.holdsDirty('dir/sub')).toBe(true);
		expect(set.holdsDirty('dir/a.kt')).toBe(false);
		expect(set.holdsDirty('di')).toBe(false);
		expect(set.dirtyPaths()).toEqual(['dir/sub/b.kt']);
	});

	// Decision (D7, review N-B1): a move onto the path of an open, dirty, deleted tab makes no
	// call and keeps both buffers.
	it('is refused by FileOps, with no call, when the target is an open dirty tab', async () => {
		const { set, a, b } = await twoOpen();
		type(a, 0, 'A');
		type(b, 0, 'B');
		set.changes([{ path: 'dir/sub/b.kt', kind: 'deleted', type: 'file' }]);
		const called: string[] = [];
		const client = {
			call: (name: string) => {
				called.push(name);
				return Promise.reject(new Error('must not be called'));
			},
		} as unknown as OpsClient;
		const moves: string[] = [];
		const ops = new FileOps(client, ws, { moved: (f, t) => moves.push(`${f}>${t}`), unsaved: (p) => set.holdsDirty(p) });

		const renamed = await ops.rename('dir/a.kt', 'sub'); // dir/sub holds a dirty buffer
		expect(renamed.ok).toBe(false);
		await ops.move([{ from: 'dir/a.kt', to: 'dir/sub/b.kt' }]);
		await ops.copy([{ from: 'dir/a.kt', to: 'dir/sub/b.kt' }]);
		expect(called).toEqual([]);
		expect(moves).toEqual([]);
		expect(ops.notice?.text).toMatch(/unsaved changes/);
		expect(a.view.dirty && b.view.dirty).toBe(true);
		expect(text(a)).toBe('Aalpha\n');
		expect(text(b)).toBe('Bbeta\n');
		expect(set.get('dir/a.kt')).toBe(a);
		expect(set.get('dir/sub/b.kt')).toBe(b);
	});
});

describe('ViewerSet safety nets (step 6 verifier)', () => {
	it('knows a deleted model with unsaved text in a folder, only directly in it', async () => {
		const d = new Daemon();
		d.files.set('dir/a.kt', 'alpha\n');
		d.files.set('dir/sub/b.kt', 'beta\n');
		d.files.set('top.kt', 'top\n');
		const set = new ViewerSet(d, ws, {}, cmBuffers);
		const [a, b, top] = [set.get('dir/a.kt'), set.get('dir/sub/b.kt'), set.get('top.kt')];
		for (const m of [a, b, top]) await ready(m);
		type(a, 0, 'A');
		type(b, 0, 'B');
		type(top, 0, 'T');
		expect(set.goneDirtyIn('dir')).toBe(false); // alive: its path is taken, no free name equals it
		set.changes([
			{ path: 'dir/a.kt', kind: 'deleted', type: 'file' },
			{ path: 'top.kt', kind: 'deleted', type: 'file' },
		]);
		expect(set.goneDirtyIn('dir')).toBe(true);
		expect(set.goneDirtyIn('dir/sub')).toBe(false);
		expect(set.goneDirtyIn('')).toBe(true);
		expect(set.goneDirtyIn('other')).toBe(false);
	});

	// Decision (D7): when two models with unsaved text meet at a target, neither is lost.
	it('keeps both buffers when two dirty models meet in a move', async () => {
		const d = new Daemon();
		d.files.set('a.kt', 'alpha\n');
		d.files.set('b.kt', 'beta\n');
		const set = new ViewerSet(d, ws, {}, cmBuffers);
		const [a, b] = [set.get('a.kt'), set.get('b.kt')];
		for (const m of [a, b]) await ready(m);
		type(a, 0, 'A');
		type(b, 0, 'B');
		set.moved('a.kt', 'b.kt');
		expect(set.get('a.kt')).toBe(a);
		expect(set.get('b.kt')).toBe(b);
		expect(text(a)).toBe('Aalpha\n');
		expect(text(b)).toBe('Bbeta\n');
	});
});

describe('formatSize', () => {
	it('says bytes, kB and MB', () => {
		expect(formatSize(999)).toBe('999 B');
		expect(formatSize(1200000)).toBe('1.2 MB');
		expect(formatSize(52000)).toBe('52 kB');
	});
});
