import { EditorState, type TransactionSpec } from '@codemirror/state';
import { EditorView } from '@codemirror/view';
import { describe, expect, it } from 'vitest';
import { sha256Hex } from '../text';
import { EditorModel, type LoadBuffers, type ViewerClient } from '../viewer';
import { CmBuffer } from './buffer';
import { compareSides, mirror } from './merge';

const make = (text: string, readOnly = false) => new CmBuffer({ text, readOnly });

/** The buffer's side of a Compare view, as the merge view makes its state from the config. */
function sideB(b: CmBuffer, disk: string): EditorState {
	const { b: cfg } = compareSides(b, disk);
	return EditorState.create(cfg);
}

/** What the merge view's update listener does: apply an edit on the side, then mirror it. */
function edit(b: CmBuffer, side: EditorState, spec: TransactionSpec): EditorState {
	const tr = side.update(spec);
	mirror(b, tr.startState, tr.state, tr.changes);
	return tr.state;
}

describe('Compare’s two sides (D8)', () => {
	// Decision (D5, N4): the buffer's side is built with the tab's own separator, so what it
	// holds, and what it hands back, is the bytes read plus the edits.
	it('holds a CRLF buffer with CRLF and the disk’s text as read', () => {
		const b = make('a\r\nb\r\n');
		const { a } = compareSides(b, 'a\nB\n');
		const side = sideB(b, 'a\nB\n');
		expect(side.facet(EditorState.lineSeparator)).toBe('\r\n');
		expect(side.sliceDoc()).toBe('a\r\nb\r\n');
		expect(side.doc.lines).toBe(3);
		expect(EditorState.create(a).sliceDoc()).toBe('a\nB\n');
	});

	// Decision: the disk's side cannot be edited.
	it('makes the disk’s side read-only', () => {
		const { a } = compareSides(make('x'), 'y');
		const state = EditorState.create(a);
		expect(state.readOnly).toBe(true);
		expect(state.facet(EditorView.editable)).toBe(false);
	});

	// Decision (N4): a CRLF buffer through Compare comes back byte for byte, edits and breaks typed or
	// pasted on its side included, and an LF disk beside it changes nothing about that.
	it('returns a CRLF buffer byte for byte through Compare, with typed and pasted breaks', () => {
		const b = make('one\r\ntwo\r\n');
		let side = sideB(b, 'one\ntwo\nthree\n');
		side = edit(b, side, { changes: { from: 0, insert: 'X' }, userEvent: 'input.type' });
		expect(b.text()).toBe('Xone\r\ntwo\r\n');
		// A paste of LF text takes the file's separator (the clipboard filter is on the side too).
		const filters = side.facet(EditorView.clipboardInputFilter);
		expect(filters.length).toBeGreaterThan(0);
		const pastedText = filters.reduce((t, f) => f(t, side), 'p\nq');
		expect(pastedText).toBe('p\r\nq');
		side = edit(b, side, { changes: { from: side.doc.length, insert: pastedText }, userEvent: 'input.paste' });
		// A replacement carrying a bare LF (a search and replace with \n) is turned into CRLF in the same transaction.
		const at = side.doc.toString().indexOf('two'); // positions count a break as one
		side = edit(b, side, { changes: { from: at, to: at + 3, insert: 't\nw' }, userEvent: 'input.type' });
		expect(side.sliceDoc()).toBe('Xone\r\nt\r\nw\r\np\r\nq');
		expect(b.text()).toBe('Xone\r\nt\r\nw\r\np\r\nq');
		expect(b.dirty).toBe(true);
	});

	// Decision: the edits made beside the disk are the buffer's own; leaving Compare keeps them (nothing to copy back).
	it('mirrors every edit into the buffer, and undoing them back makes it clean again', () => {
		const b = make('abc\n');
		let side = sideB(b, 'abc\nmore\n');
		side = edit(b, side, { changes: { from: 3, insert: 'd' }, userEvent: 'input.type' });
		expect(b.text()).toBe('abcd\n');
		expect(b.dirty).toBe(true);
		side = edit(b, side, { changes: { from: 3, to: 4 }, userEvent: 'delete.backward' });
		expect(b.text()).toBe('abc\n');
		expect(b.dirty).toBe(false);
		void side;
	});

	// Decision: if the two documents ever drift, the buffer takes the side's text rather than throw
	// or apply the change to the wrong text; equal lengths do not hide the drift.
	it.each([
		['of another length', { changes: { from: 0, insert: 'ZZ' } }],
		['of the same length', { changes: { from: 0, to: 1, insert: 'Z' } }],
	])('resynchronises the buffer when it drifted %s', (_n, drift) => {
		const b = make('abc\n');
		const side = sideB(b, 'abc\n');
		b.dispatch(drift); // the buffer moved on behind the side's back
		const tr = side.update({ changes: { from: 3, insert: 'd' } });
		expect(mirror(b, tr.startState, tr.state, tr.changes)).toBe(false);
		expect(b.text()).toBe(tr.state.sliceDoc());
		expect(b.text()).toBe('abcd\n');
	});
});

describe('Compare then save (D8)', () => {
	// Decision: Mod-s from Compare saves the buffer: the model snapshots the same document, with the file's separator.
	it('saves the CRLF text of the buffer after an edit made on Compare’s side', async () => {
		let disk = 'one\r\ntwo\r\n';
		const writes: { content: string; ifRevision: string }[] = [];
		const client = {
			call: async (name: string, p: { content: string; ifRevision: string }) => {
				if (name === 'file.write') {
					writes.push(p);
					disk = p.content;
					return { path: 'a.kt', size: disk.length, revision: await sha256Hex(disk) };
				}
				return { path: 'a.kt', size: disk.length, modTime: 't', offset: 0, nextOffset: disk.length, text: disk, revision: await sha256Hex(disk) };
			},
		} as unknown as ViewerClient;
		const load: LoadBuffers = async () => ({ createBuffer: (o) => new CmBuffer(o) });
		const m = new EditorModel(client, { root: '/p' }, 'a.kt', load);
		await m.load();
		const b = m.view.buffer as CmBuffer;
		const side = sideB(b, 'one\ntwo\n');
		edit(b, side, { changes: { from: 4, insert: 'TWO ' }, userEvent: 'input.type' });
		await m.save();
		expect(writes).toHaveLength(1);
		expect(writes[0]!.content).toBe('one\r\nTWO two\r\n');
		expect(disk).toBe('one\r\nTWO two\r\n');
		expect(m.view.dirty).toBe(false);
	});
});

describe('the merge view’s colours (D2, D8)', () => {
	const SOURCES = import.meta.glob<string>('./merge.ts', { query: '?raw', import: 'default', eager: true });

	// Decision: the diff colours come from the tokens; a literal in merge.ts would not follow the
	// light and dark schemes. (The library's own theme has literals; ours overrides them.)
	it('has no colour literal in merge.ts', () => {
		const src = Object.values(SOURCES)[0]!;
		expect(src.length).toBeGreaterThan(1000);
		const code = src.replace(/\/\/.*$/gm, '').replace(/\/\*[\s\S]*?\*\//g, '');
		expect(code).not.toMatch(/#[0-9a-fA-F]{3,8}\b/);
		expect(code).not.toMatch(/\b(rgb|rgba|hsl|hsla|oklch|lab|hwb)\(/);
		expect(code).toMatch(/var\(--danger\)/);
		expect(code).toMatch(/var\(--success\)/);
	});
});
