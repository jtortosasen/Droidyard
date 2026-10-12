import { redo, undo, undoDepth } from '@codemirror/commands';
import { describe, expect, it, vi } from 'vitest';
import { capError } from '../../build/testing';
import { sha256Hex } from '../text';
import { EditorModel, type LoadBuffers, type ViewerClient } from '../viewer';
import { CmBuffer } from './buffer';

const buffers: LoadBuffers = async () => ({ createBuffer: (o) => new CmBuffer(o) });

/** One file `a.kt` on a daemon that answers whole reads and conditional writes. */
function daemon(initial: string) {
	const d = { disk: initial as string | null, binary: false, writes: [] as { content: string; ifRevision: string }[] };
	const revision = async () => (d.disk === null ? 'absent' : sha256Hex(d.disk));
	const client = {
		call: async (name: string, p: { content?: string; ifRevision?: string }) => {
			if (name === 'file.write') {
				d.writes.push({ content: p.content!, ifRevision: p.ifRevision! });
				if (p.ifRevision !== (await revision())) throw capError('FILE_REVISION_CONFLICT', 'the file changed');
				d.disk = p.content!;
				return { path: 'a.kt', size: d.disk.length, revision: await sha256Hex(d.disk) };
			}
			if (d.disk === null) throw capError('FILE_NOT_FOUND', 'no such file');
			if (d.binary) return { path: 'a.kt', size: d.disk.length, modTime: 't', offset: 0, nextOffset: d.disk.length, binary: true, revision: await revision() };
			return { path: 'a.kt', size: d.disk.length, modTime: 't', offset: 0, nextOffset: d.disk.length, text: d.disk, revision: await revision() };
		},
	} as unknown as ViewerClient;
	return { d, client, revision };
}

async function opened(initial: string) {
	const x = daemon(initial);
	const m = new EditorModel(x.client, { root: '/p' }, 'a.kt', buffers);
	await m.load();
	return { ...x, m, b: m.view.buffer as CmBuffer };
}

/** A command as a view runs it, on the buffer (so its listeners fire). */
function run(b: CmBuffer, command: (v: { state: CmBuffer['state']; dispatch: (spec: object) => void }) => boolean) {
	return command({ state: b.state, dispatch: (spec) => b.dispatch(spec as never) });
}

const type = (b: CmBuffer, at: number, insert: string, del = 0) =>
	b.dispatch({ changes: { from: at, to: at + del, insert }, userEvent: 'input.type' });

describe('the echo of our own save (D8)', () => {
	// Decision (review B2): an event whose revision is the base is the echo of our write. Here the
	// user typed more after the save, so the buffer is dirty: without the echo rule the disk's
	// version (the very text we saved) would be held behind a bar, as if somebody else wrote it.
	// Seen failing with the echo condition replaced by `false`.
	it('changes nothing when it arrives after more typing: no bar, still dirty, history intact', async () => {
		const { m, b } = await opened('one\n');
		type(b, 0, 'X');
		await m.save();
		type(b, 0, 'Y');
		expect(m.view.dirty).toBe(true);
		const depth = undoDepth(b.state);
		await m.load(); // what the `modified` event for our own write does
		expect(m.view.external).toBeNull();
		expect(m.view.dirty).toBe(true);
		expect(b.text()).toBe('YXone\n');
		expect(undoDepth(b.state)).toBe(depth);
	});
});

describe('undone to clean with the bar up (D8, review B1)', () => {
	// Decision: while a disk version is held, undoing to a clean-looking buffer neither takes it nor
	// drops the bar, so Redo brings the user's text back intact (the old rule took the disk's text
	// at the undo and Redo then garbled the edit).
	it('keeps the bar and the user’s text through undo and redo, on the same line', async () => {
		const { d, m, b } = await opened('hello world\n');
		type(b, 6, 'planet', 5);
		d.disk = 'hello there\n';
		await m.load();
		expect(m.view.external?.text).toBe('hello there\n');
		run(b, undo);
		expect(b.text()).toBe('hello world\n');
		expect(m.view.dirty).toBe(false);
		await m.load(); // an event or re-sync meanwhile still finds the bar up
		expect(m.view.external?.text).toBe('hello there\n');
		expect(b.text()).toBe('hello world\n');
		run(b, redo);
		expect(b.text()).toBe('hello planet\n');
		expect(m.view.dirty).toBe(true);
		expect(m.view.external).not.toBeNull();
		m.keepMine();
		await m.save();
		expect(d.disk).toBe('hello planet\n');
	});

	// Decision: the same with a disk of another line separator (where a take rebuilds the state).
	it('keeps the user’s text when the disk turned CRLF', async () => {
		const { d, m, b } = await opened('hello world\n');
		type(b, 6, 'planet', 5);
		d.disk = 'hello there\r\n';
		await m.load();
		run(b, undo);
		await m.load();
		expect(m.view.external?.text).toBe('hello there\r\n');
		expect(b.text()).toBe('hello world\n');
		run(b, redo);
		expect(b.text()).toBe('hello planet\n');
		expect(m.view.external).not.toBeNull();
	});

	// Decision: a file that turned binary does not drop the buffer, undone or not.
	it('keeps the buffer when the disk turned binary', async () => {
		const { d, m, b } = await opened('hello world\n');
		type(b, 6, 'planet', 5);
		d.binary = true;
		await m.load();
		run(b, undo);
		await m.load();
		expect(m.view.state).toBe('ready');
		expect(m.view.buffer).toBe(b);
		expect(m.view.external?.binary).toBe(true);
		run(b, redo);
		expect(b.text()).toBe('hello planet\n');
	});

	// Decision: a buffer with something to redo is not clean enough for a silent take: it enters the bar.
	it('puts a clean buffer with a redo stack behind the bar when the disk changes, and Redo still works', async () => {
		const { d, m, b } = await opened('hello world\n');
		type(b, 0, 'X');
		run(b, undo);
		expect(m.view.dirty).toBe(false);
		expect(b.hasRedo).toBe(true);
		d.disk = 'agent wrote this\n';
		await m.load();
		expect(m.view.external?.text).toBe('agent wrote this\n');
		expect(b.text()).toBe('hello world\n');
		run(b, redo);
		expect(b.text()).toBe('Xhello world\n');
		// And Reload is the user's choice: it takes the disk, and Undo brings the typing back whole.
		m.reload();
		expect(b.text()).toBe('agent wrote this\n');
		run(b, undo);
		expect(b.text()).toBe('Xhello world\n');
	});

	// Decision: Keep mine on a clean-looking buffer makes it unsaved against the disk's text.
	it('lets Keep mine on an undone buffer write it knowingly', async () => {
		const { d, m, b, revision } = await opened('hello world\n');
		type(b, 0, 'X');
		run(b, undo);
		d.disk = 'agent wrote this\n';
		await m.load();
		const rev = await revision();
		m.keepMine();
		expect(m.base).toBe(rev);
		expect(m.view.dirty).toBe(true);
		await m.save();
		expect(d.writes.at(-1)).toEqual({ content: 'hello world\n', ifRevision: rev });
		expect(d.disk).toBe('hello world\n');
	});
});

describe('a take is one undoable event (D8, review B1)', () => {
	// Decision: a clean buffer taking the disk's text silently, and Reload, are history events of
	// their own: Undo returns the exact text before them (and the history is not mapped through them).
	it('Undo after a silent take gives back the text before it, whole', async () => {
		const { d, m, b } = await opened('one\ntwo\nthree\n');
		d.disk = 'one\nTWO and more\nthree\nfour\n';
		await m.load();
		expect(b.text()).toBe('one\nTWO and more\nthree\nfour\n');
		expect(m.view.dirty).toBe(false);
		run(b, undo);
		expect(b.text()).toBe('one\ntwo\nthree\n');
		run(b, redo);
		expect(b.text()).toBe('one\nTWO and more\nthree\nfour\n');
	});

	it('Undo after Reload gives back the dirty text before it, whole', async () => {
		const { d, m, b } = await opened('one\ntwo\n');
		type(b, 0, 'MINE ');
		d.disk = 'theirs\n';
		await m.load();
		m.reload();
		expect(b.text()).toBe('theirs\n');
		run(b, undo);
		expect(b.text()).toBe('MINE one\ntwo\n');
		expect(m.view.dirty).toBe(true);
	});
});

describe('a deleted file with the bar up (D8, review)', () => {
	it('is held as gone, so Reload is off', async () => {
		const { d, m, b } = await opened('one\n');
		type(b, 0, 'X');
		d.disk = 'theirs\n';
		await m.load();
		m.markDeleted();
		expect(m.view.external).toMatchObject({ gone: true, canReload: false });
		await vi.waitFor(() => expect(m.view.state).toBe('deleted'));
	});
});
