import { EditorState, Transaction, type TransactionSpec } from '@codemirror/state';
import { EditorView } from '@codemirror/view';
import { describe, expect, it } from 'vitest';
import { sha256Hex } from '../text';
import { EditorModel, type LoadBuffers, type ViewerClient } from '../viewer';
import { CmBuffer } from './buffer';
import { compareSides, follow, followed, mirror, sideCaptions } from './merge';

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

	// Decision (plan m2l step 5): the drift branch takes document positions, so a CRLF buffer
	// that drifted comes back whole; string offsets from sliceDoc() count each CRLF as two.
	it('resynchronises a CRLF buffer that drifted without corrupting it', () => {
		const b = make('a\r\nb\r\nc\r\n');
		const side = sideB(b, 'a\r\nb\r\nc\r\n');
		b.dispatch({ changes: { from: 0, insert: 'X' } }); // the buffer moved on behind the side's back
		const tr = side.update({ changes: { from: 3, insert: 'B' } }); // after "b": a break counts once
		expect(mirror(b, tr.startState, tr.state, tr.changes)).toBe(false);
		expect(b.text()).toBe(tr.state.sliceDoc());
		expect(b.text()).toBe('a\r\nbB\r\nc\r\n');
	});
});

describe('the Diff tab’s captions (plan m2l step 5)', () => {
	const ariaLabels = (state: EditorState) =>
		state
			.facet(EditorView.contentAttributes)
			.map((a) => (typeof a === 'function' ? null : (a as Record<string, string>)['aria-label']))
			.filter((l): l is string => l !== null && l !== undefined);

	// Decision: Compare as m2i built it keeps its captions; the Diff tab names its sides HEAD and Working tree.
	it('captions the sides by the options, else On disk and Yours', () => {
		expect(sideCaptions({})).toEqual(['On disk', 'Yours']);
		expect(sideCaptions({ labels: { a: 'HEAD', b: 'Working tree' } })).toEqual(['HEAD', 'Working tree']);
	});

	// Decision: HEAD's text is not announced as "File contents on disk"; the read-only side takes the caller's name.
	it('names the read-only side by the option, else File contents on disk', () => {
		const b = make('x');
		expect(ariaLabels(EditorState.create(compareSides(b, 'y').a))).toEqual(['File contents on disk']);
		expect(ariaLabels(EditorState.create(compareSides(b, 'y', 'File contents at HEAD').a))).toEqual(['File contents at HEAD']);
		expect(ariaLabels(EditorState.create(compareSides(b, 'y', 'File contents at HEAD').a))).not.toContain('File contents on disk');
	});
});

/** What the merge view does with a follow: nothing, or the transaction on its side. */
function followSide(b: CmBuffer, side: EditorState): EditorState {
	const r = follow(side, b);
	if (r === 'remount') throw new Error('expected a transaction, got a remount');
	return r === null ? side : r.state;
}

describe('the buffer’s side follows the buffer (plan m2l Q2 §2, review B1)', () => {
	// Decision: a change the side did not make (a silent reload, an edit in the Editor tab) reaches it, so its next
	// keystroke mirrors onto the text the buffer holds; without it the stale side's drift branch writes its own
	// stale text back over the buffer. LF and CRLF: the CRLF case fails if the follower takes string offsets
	// (sliceDoc) where document positions are meant, since a break counts twice in the first and once in the second.
	it.each([
		['LF', '\n', 'a\nb\nc\n'],
		['CRLF', '\r\n', 'a\r\nb\r\nc\r\n'],
	])('keeps a %s buffer’s load, edit and the side’s keystroke', (_n, br, text) => {
		const b = make(text);
		let side = sideB(b, 'a\nb\nc\n');
		b.load(`a${br}BB${br}c${br}`); // a clean buffer takes the disk's new text silently
		b.dispatch({ changes: { from: 0, insert: '>>' }, userEvent: 'input.type' }); // an edit in the Editor tab
		side = followSide(b, side);
		expect(side.doc.toString()).toBe(b.state.doc.toString());
		side = edit(b, side, { changes: { from: side.doc.length, insert: 'Z' }, userEvent: 'input.type' }); // a keystroke on the side
		expect(b.text()).toBe(`>>a${br}BB${br}c${br}Z`);
		expect(side.sliceDoc()).toBe(b.text());
	});

	// Decision: the change is not the user's on the side: it carries the mirror's skip mark and stays out of the side's undo history.
	it('marks its transaction as followed and not undoable', () => {
		const b = make('abc\n');
		const side = sideB(b, 'abc\n');
		b.dispatch({ changes: { from: 1, insert: 'X' } });
		const tr = follow(side, b);
		expect(tr).not.toBeNull();
		expect(tr).not.toBe('remount');
		const t = tr as Transaction;
		expect(t.annotation(followed)).toBe(true);
		expect(t.annotation(Transaction.addToHistory)).toBe(false);
	});

	// Decision: the side already holding the buffer's text is left alone (the buffer's own onChange fires for every edit, the side's included).
	it('does nothing when the side holds the buffer’s text', () => {
		const b = make('abc\r\n');
		expect(follow(sideB(b, 'abc\n'), b)).toBeNull();
	});

	// Decision: a state made with the other separator, or with the other read-only, cannot be patched: the side is made again.
	it('asks for a remount when the separator or read-only changed', () => {
		const lf = make('a\nb\n');
		const sideLf = sideB(lf, 'a\nb\n');
		lf.load('a\r\nb\r\n');
		expect(follow(sideLf, lf)).toBe('remount');
		const ro = make('a\nb\n');
		const sideRo = sideB(ro, 'a\nb\n');
		ro.setReadOnly(true);
		expect(follow(sideRo, ro)).toBe('remount');
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
