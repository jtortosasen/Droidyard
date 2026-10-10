import { describe, expect, it } from 'vitest';
import { CapabilityError } from '../api/client';
import type { Params, Result } from '../api/capabilities.gen';
import { settle } from '../build/testing';
import { CAP, FileViewer, PAGE, ViewerSet, formatSize, type ViewerClient } from './viewer';

type Page = Result<'file.read'>;

/** A daemon with one file: pages by the offset and limit asked for, ending at a line. */
class Files implements ViewerClient {
	files = new Map<string, string>();
	binary = new Set<string>();
	calls: Params<'file.read'>[] = [];
	// A hook to change the file between pages.
	between: (() => void) | null = null;
	call(_n: 'file.read', p: Params<'file.read'>): Promise<Page> {
		this.calls.push(p);
		const text = this.files.get(p.path);
		if (text === undefined) return Promise.reject(new CapabilityError({ code: 'FILE_NOT_FOUND', message: 'no such file' }, false));
		const bytes = new TextEncoder().encode(text);
		const offset = p.offset ?? 0;
		if (this.binary.has(p.path)) {
			return Promise.resolve({ path: p.path, size: bytes.length, modTime: 't', offset, nextOffset: bytes.length, binary: true });
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
			text: new TextDecoder().decode(bytes.slice(offset, end)),
		};
		this.between?.();
		return Promise.resolve(out);
	}
}

const ws = { root: '/proj' };

describe('FileViewer', () => {
	// Decision (D26): pages are read in order and joined at their offsets, so the text
	// shown is the file's, not a page's.
	it('joins the pages of a file larger than one page, in order', async () => {
		const f = new Files();
		const line = 'x'.repeat(99) + '\n';
		const n = Math.ceil((PAGE * 2.5) / 100);
		f.files.set('a.txt', line.repeat(n));
		const v = new FileViewer(f, ws, 'a.txt');
		await v.load();
		expect(v.view.state).toBe('ready');
		expect(v.view.lines.length).toBe(n);
		expect(v.view.capped).toBe(false);
		expect(f.calls.length).toBeGreaterThanOrEqual(3);
		// Each page asked for starts where the previous one ended.
		const offs = f.calls.map((c) => c.offset);
		expect(offs[0]).toBe(0);
		expect(offs.every((o, i) => i === 0 || o! > offs[i - 1]!)).toBe(true);
		expect(f.calls.every((c) => c.limit === PAGE)).toBe(true);
	});

	// Decision (D26): the viewer reads at most CAP bytes and says so.
	it('stops at the cap and states that the file is longer', async () => {
		const f = new Files();
		const line = 'y'.repeat(99) + '\n';
		f.files.set('big.log', line.repeat(Math.ceil((CAP * 2) / 100)));
		const v = new FileViewer(f, ws, 'big.log');
		await v.load();
		expect(v.view.capped).toBe(true);
		expect(v.view.read).toBeGreaterThanOrEqual(CAP);
		expect(v.view.read).toBeLessThan(CAP + PAGE);
		expect(v.view.size).toBeGreaterThan(CAP);
		expect(f.calls.length).toBeLessThanOrEqual(CAP / PAGE + 1);
	});

	// Decision (Q4): a binary file is a placeholder with its size and holds no text.
	it('keeps no text for a binary file', async () => {
		const f = new Files();
		f.files.set('a.png', 'PNG\u0000data');
		f.binary.add('a.png');
		const v = new FileViewer(f, ws, 'a.png');
		await v.load();
		expect(v.view.state).toBe('binary');
		expect(v.view.lines).toEqual([]);
		expect(v.view.size).toBe(8);
	});

	// Decision: a page that is not the one asked for means the file changed under the read;
	// the read starts over rather than joining text that never existed.
	it('restarts when a page is not at the offset asked for', async () => {
		const f = new Files();
		const line = 'z'.repeat(99) + '\n';
		f.files.set('c.txt', line.repeat(Math.ceil((PAGE * 1.5) / 100)));
		let first = true;
		const real = f.call.bind(f);
		f.call = (n, p) => {
			if (!first || p.offset === 0) return real(n, p);
			first = false;
			return real(n, p).then((r) => ({ ...r, offset: r.offset + 1 }));
		};
		const v = new FileViewer(f, ws, 'c.txt');
		await v.load();
		expect(v.view.state).toBe('ready');
		expect(f.calls.filter((c) => c.offset === 0).length).toBe(2);
	});

	it('shows a failure as text', async () => {
		const f = new Files();
		const v = new FileViewer(f, ws, 'missing');
		await v.load();
		expect(v.view.state).toBe('error');
		expect(v.view.error).toContain('FILE_NOT_FOUND');
	});
});

describe('ViewerSet', () => {
	async function open() {
		const f = new Files();
		f.files.set('a.kt', 'one\ntwo\n');
		const set = new ViewerSet(f, ws);
		const v = set.get('a.kt');
		await settle();
		return { f, set, v };
	}

	// Decision (plan step 6): a `modified` event for the path reads it again.
	it('re-reads a file the watch reports as modified, and no other', async () => {
		const { f, set, v } = await open();
		expect(v.view.lines).toEqual(['one', 'two']);
		f.files.set('a.kt', 'one\ntwo\nthree\n');
		set.changes([{ path: 'other.kt', kind: 'modified', type: 'file' }]);
		await settle();
		expect(v.view.lines).toHaveLength(2);
		set.changes([{ path: 'a.kt', kind: 'modified', type: 'file' }]);
		await settle();
		expect(v.view.lines).toEqual(['one', 'two', 'three']);
	});

	// Decision: a deleted file is marked and its last text stays; it comes back on `created`.
	it('marks a deleted file, keeps its text, and reads it again if it returns', async () => {
		const { f, set, v } = await open();
		set.changes([{ path: 'a.kt', kind: 'deleted', type: 'file' }]);
		expect(v.view.state).toBe('deleted');
		expect(v.view.lines).toEqual(['one', 'two']);
		f.files.set('a.kt', 'back\n');
		set.changes([{ path: 'a.kt', kind: 'created', type: 'file' }]);
		await settle();
		expect(v.view.state).toBe('ready');
		expect(v.view.lines).toEqual(['back']);
	});

	// Decision: an answer for a read that a deletion overtook is dropped.
	it('drops a read that a deletion overtook', async () => {
		const { f, set, v } = await open();
		f.files.set('a.kt', 'new\n');
		set.changes([{ path: 'a.kt', kind: 'modified', type: 'file' }]);
		set.changes([{ path: 'a.kt', kind: 'deleted', type: 'file' }]);
		await settle();
		expect(v.view.state).toBe('deleted');
		expect(v.view.lines).toEqual(['one', 'two']);
	});

	// Decision: after a re-sync (events may have been missed) every open file is read again.
	it('re-reads every open file on a re-sync', async () => {
		const { f, set, v } = await open();
		f.files.set('a.kt', 'changed\n');
		set.resync();
		await settle();
		expect(v.view.lines).toEqual(['changed']);
	});

	it('forgets the viewers of closed tabs', async () => {
		const { set, v } = await open();
		set.keep([]);
		expect(set.get('a.kt')).not.toBe(v);
	});
});

describe('formatSize', () => {
	it('says bytes, kB and MB', () => {
		expect(formatSize(999)).toBe('999 B');
		expect(formatSize(1200000)).toBe('1.2 MB');
		expect(formatSize(52000)).toBe('52 kB');
	});
});
