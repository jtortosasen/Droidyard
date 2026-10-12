import { redo, undo, undoDepth } from '@codemirror/commands';
import { SearchQuery, replaceAll, setSearchQuery } from '@codemirror/search';
import { EditorSelection, EditorState } from '@codemirror/state';
import { EditorView, keymap } from '@codemirror/view';
import { describe, expect, it } from 'vitest';
import { detectSeparator } from '../text';
import { CmBuffer, pasted } from './buffer';

const make = (text: string, readOnly = false) => new CmBuffer({ text, readOnly });

/** A user's typing at `at`: CM's own input inserts `state.lineBreak` for a break. */
function type(b: CmBuffer, at: number, insert: string) {
	b.dispatch({ changes: { from: at, insert }, selection: { anchor: at + insert.length }, userEvent: 'input.type' });
}

/** CM's paste path (doPaste): the filters, then the text split by the state's own separator. */
function paste(b: CmBuffer, text: string) {
	const s = b.state;
	const input = s.facet(EditorView.clipboardInputFilter).reduce((t, f) => f(t, s), text);
	b.dispatch({ ...s.replaceSelection(input), userEvent: 'input.paste' });
}

/** Runs a command on the buffer's state the way a view does, without a view. */
function run(b: CmBuffer, command: (v: { state: EditorState; dispatch: (spec: object) => void }) => boolean) {
	return command({ state: b.state, dispatch: (spec) => b.dispatch(spec as never) });
}

describe('CmBuffer round trip (D5)', () => {
	// Decision: a save writes the bytes it read plus the edits. LF, CRLF, a BOM and a file
	// with no break must come back from the buffer unchanged.
	it.each([
		['LF', 'one\ntwo\nthree\n'],
		['CRLF', 'one\r\ntwo\r\nthree\r\n'],
		['a BOM, kept as text', '﻿package a\nfun f() {}\n'],
		['a BOM and CRLF', '﻿a\r\nb'],
		['no break', 'just one line'],
		['empty', ''],
		['no final newline', 'a\nb'],
	])('returns %s byte for byte', (_n, text) => {
		expect(make(text).text()).toBe(text);
	});

	// Decision: the separator an edit takes is the file's: Enter in a CRLF file is CRLF.
	it('types a break with the file’s separator, so a CRLF file stays CRLF and an LF file LF', () => {
		const crlf = make('a\r\nb');
		type(crlf, 1, crlf.state.lineBreak);
		expect(crlf.text()).toBe('a\r\n\r\nb');
		const lf = make('a\nb');
		type(lf, 1, lf.state.lineBreak);
		expect(lf.text()).toBe('a\n\nb');
		expect(detectSeparator(crlf.text())).toBe('\r\n');
	});

	// Decision (D5): pasted breaks become the file's separator (the clipboard filter; the
	// transaction filter below is the net under it, for text that does not come by paste).
	it('keeps a CRLF file CRLF when LF is pasted into it, and an LF file LF when CRLF is', () => {
		const crlf = make('a\r\nb\r\n');
		paste(crlf, 'x\ny\nz');
		expect(crlf.text()).toBe('x\r\ny\r\nza\r\nb\r\n');
		expect(detectSeparator(crlf.text())).toBe('\r\n');

		const lf = make('a\nb\n');
		paste(lf, 'x\r\ny\rz');
		expect(lf.text()).toBe('x\ny\nza\nb\n');
		expect(detectSeparator(lf.text())).toBe('\n');
	});

	// Decision (D5, verifier): the separator is kept for every way text gets in, not only paste:
	// a replacement, an insertReplacementText, any transaction. Seen failing without the filter:
	// the CRLF file ends up with a lone LF (mixed, so the next read is read-only).
	it('turns a foreign break in any inserted text into the file’s separator', () => {
		const crlf = make('a b\r\nc\r\n');
		crlf.dispatch({ changes: { from: 1, to: 2, insert: 'x\ny\rz' }, userEvent: 'input.replace' });
		expect(crlf.text()).toBe('ax\r\ny\r\nzb\r\nc\r\n');
		expect(detectSeparator(crlf.text())).toBe('\r\n');

		const lf = make('a b\nc\n');
		lf.dispatch({ changes: { from: 1, to: 2, insert: 'x\r\ny\rz\r' }, userEvent: 'input.replace' });
		expect(lf.text()).toBe('ax\ny\nz\nb\nc\n');
		expect(detectSeparator(lf.text())).toBe('\n');
	});

	it('keeps the selection after the fixed text, and one history event', () => {
		const b = make('ab\r\n');
		b.dispatch({ changes: { from: 1, insert: 'x\ny' }, selection: { anchor: 4 }, userEvent: 'input.type' });
		expect(b.text()).toBe('ax\r\nyb\r\n');
		expect(b.state.selection.main.head).toBe(4);
		expect(undoDepth(b.state)).toBe(1);
		expect(run(b, undo)).toBe(true);
		expect(b.text()).toBe('ab\r\n');
	});

	it('leaves right-separator text and mixed (read-only) files alone', () => {
		const b = make('a\r\nb');
		b.dispatch({ changes: { from: 1, insert: 'x' + b.state.lineBreak + 'y' } });
		expect(b.text()).toBe('ax\r\ny\r\nb');
		// A mixed file has no fixed separator: nothing is rewritten.
		const mixed = make('a\r\nb\nc', true);
		mixed.dispatch({ changes: { from: 0, insert: 'x\ny\rz' } });
		expect(mixed.state.doc.toString()).toBe('x\ny\nza\nb\nc');
	});

	// Decision (D5): the real search-and-replace of CM, with a regular expression replacing by
	// "\n", must not corrupt a CRLF file (and a "\r" an LF one).
	it('keeps a CRLF file CRLF through replace-all with a break in the replacement', () => {
		const crlf = make('one,two,three\r\nfour\r\n');
		const view = { get state() { return crlf.state; }, dispatch: (spec: object) => crlf.dispatch(spec as never) };
		crlf.dispatch({ effects: setSearchQuery.of(new SearchQuery({ search: ',', replace: '\\n', regexp: true })) });
		expect(replaceAll(view as unknown as EditorView)).toBe(true);
		expect(crlf.text()).toBe('one\r\ntwo\r\nthree\r\nfour\r\n');

		const lf = make('one,two\n');
		const lfView = { get state() { return lf.state; }, dispatch: (spec: object) => lf.dispatch(spec as never) };
		lf.dispatch({ effects: setSearchQuery.of(new SearchQuery({ search: ',', replace: '\\r\\n', regexp: true })) });
		expect(replaceAll(lfView as unknown as EditorView)).toBe(true);
		expect(lf.text()).toBe('one\ntwo\n');
	});

	it('leaves pasted text alone in a file shown as it is (mixed endings)', () => {
		const mixed = make('a\r\nb\nc', true);
		expect(mixed.state.facet(EditorState.lineSeparator)).toBeUndefined();
		expect(pasted('x\ny\r\nz', mixed.state)).toBe('x\ny\r\nz');
	});
});

describe('CmBuffer dirty flag (D7)', () => {
	// Decision: dirty is "differs from the saved text", so undoing back is clean again.
	it('is dirty after an edit and clean after undoing it', () => {
		const b = make('abc');
		const seen: boolean[] = [];
		b.onChange(() => seen.push(b.dirty));
		expect(b.dirty).toBe(false);
		type(b, 3, 'd');
		expect(b.dirty).toBe(true);
		expect(run(b, undo)).toBe(true);
		expect(b.text()).toBe('abc');
		expect(b.dirty).toBe(false);
		expect(seen).toEqual([true, false]);
	});

	// Decision: what a save commits is the snapshot taken when it started, so an edit made
	// during the save is still unsaved afterwards.
	it('stays dirty for an edit made after the snapshot a save committed', () => {
		const b = make('abc');
		type(b, 3, 'd');
		const snap = b.snapshot();
		expect(snap.text).toBe('abcd');
		type(b, 4, 'e');
		snap.commit();
		expect(b.dirty).toBe(true);
		expect(b.text()).toBe('abcde');
		const second = b.snapshot();
		second.commit();
		expect(b.dirty).toBe(false);
	});

	it('is clean when the snapshot is all there is', () => {
		const b = make('abc');
		type(b, 3, 'd');
		b.snapshot().commit();
		expect(b.dirty).toBe(false);
	});
});

describe('CmBuffer load', () => {
	// Decision (D8): a clean buffer takes the disk's text without becoming dirty, without a
	// flicker of "dirty" in between, keeps a cursor outside the change, and the take is one undoable
	// event of its own: Undo brings back the text before it as a whole (never a mapped mixture).
	it('takes new text clean as one undoable event, keeping a cursor outside the change', () => {
		const b = make('one\ntwo\nthree\n');
		type(b, 0, 'X');
		run(b, undo);
		expect(undoDepth(b.state)).toBe(0);
		type(b, 0, 'Y');
		b.snapshot().commit();
		expect(undoDepth(b.state)).toBe(1);
		b.dispatch({ selection: { anchor: 2 } });
		const seen: boolean[] = [];
		b.onChange(() => seen.push(b.dirty));
		b.load('Yone\ntwo\nthree\nfour\n');
		expect(b.text()).toBe('Yone\ntwo\nthree\nfour\n');
		expect(b.dirty).toBe(false);
		expect(seen).toEqual([false]);
		expect(undoDepth(b.state)).toBe(2); // the typing, and the take
		expect(b.state.selection.main.head).toBe(2);
		run(b, undo);
		expect(b.text()).toBe('Yone\ntwo\nthree\n');
		expect(b.dirty).toBe(true); // against the disk's text it was loaded from
		run(b, redo);
		expect(b.text()).toBe('Yone\ntwo\nthree\nfour\n');
		expect(b.dirty).toBe(false);
	});

	// Decision (D8, review): a buffer undone to its saved text has a redo stack, which a silent take would garble.
	it('reports a redo stack, and setSaved(text) makes the buffer unsaved against that text', () => {
		const b = make('one\n');
		expect(b.hasRedo).toBe(false);
		type(b, 0, 'X');
		run(b, undo);
		expect(b.dirty).toBe(false);
		expect(b.hasRedo).toBe(true);
		b.setSaved('other\n');
		expect(b.dirty).toBe(true);
		expect(b.text()).toBe('one\n');
		b.setSaved('one\n');
		expect(b.dirty).toBe(false);
		b.setSaved(null);
		expect(b.dirty).toBe(true);
		const snap = b.snapshot();
		snap.commit();
		expect(b.dirty).toBe(false);
	});

	// Decision: a disk whose breaks differ from the buffer's separator is not the same bytes.
	it('counts a disk with another separator as different from a CRLF buffer', () => {
		const b = make('a\r\nb\r\n');
		b.setSaved('a\nb\n');
		expect(b.dirty).toBe(true);
		b.setSaved('a\r\nb\r\n');
		expect(b.dirty).toBe(false);
	});

	// Decision: a CRLF file read again stays CRLF byte for byte, including the span replaced.
	it('replaces a span of a CRLF file with the right positions', () => {
		const b = make('a\r\nb\r\nc\r\n');
		b.load('a\r\nB2\r\nc\r\n');
		expect(b.text()).toBe('a\r\nB2\r\nc\r\n');
		expect(b.dirty).toBe(false);
	});

	it('is made again when the separator changes', () => {
		const b = make('a\r\nb\r\n');
		b.load('a\nb\n');
		expect(b.text()).toBe('a\nb\n');
		expect(b.state.lineBreak).toBe('\n');
		b.load('a\nb\r\nc');
		expect(b.state.facet(EditorState.lineSeparator)).toBeUndefined();
		expect(b.dirty).toBe(false);
	});

	// A snapshot taken before a reload must not mark the reloaded text as the saved one.
	it('ignores a commit of a snapshot from before the reload', () => {
		const b = make('abc');
		type(b, 3, 'd');
		const snap = b.snapshot();
		b.load('xyz');
		snap.commit();
		expect(b.text()).toBe('xyz');
		expect(b.dirty).toBe(false);
		type(b, 3, 'w');
		expect(b.dirty).toBe(true);
	});
});

describe('CmBuffer state', () => {
	// Decision (D7): a rename changes the path and so the language, never the document or the history.
	it('keeps its history through a path change', async () => {
		const b = new CmBuffer({ text: 'val a = 1\n', readOnly: false, path: 'A.kt' });
		type(b, 0, 'x');
		b.setPath('B.kts');
		await new Promise((r) => setTimeout(r, 20));
		expect(b.text()).toBe('xval a = 1\n');
		expect(undoDepth(b.state)).toBe(1);
		expect(run(b, undo)).toBe(true);
		expect(b.text()).toBe('val a = 1\n');
		expect(run(b, redo)).toBe(true);
		expect(b.text()).toBe('xval a = 1\n');
	});

	it('switches the state’s read-only flag both ways', () => {
		const b = make('abc', true);
		expect(b.state.readOnly).toBe(true);
		b.setReadOnly(false);
		expect(b.state.readOnly).toBe(false);
	});

	// Decision (D9): the built-ins the plan names are bound: save, find, go to line, undo,
	// fold; and several selections are allowed (Cmd-click, rectangular).
	it('binds save, find, go to line, undo, fold and Tab, and allows several selections', () => {
		const state = make('x').state;
		const keys = state.facet(keymap).flat().map((k) => k.key);
		for (const k of ['Mod-s', 'Mod-f', 'Mod-Alt-g', 'Mod-z', 'Ctrl-Shift-[', 'Tab']) expect(keys).toContain(k);
		expect(state.facet(EditorState.allowMultipleSelections)).toBe(true);
		const b = make('abc def');
		b.dispatch({ selection: EditorSelection.create([EditorSelection.cursor(0), EditorSelection.cursor(4)]) });
		expect(b.state.selection.ranges).toHaveLength(2);
	});

	// Decision (D7): Mod-s asks the model to save, through the hook the mounted view sets.
	it('calls the save hook on Mod-s and swallows the key', () => {
		const b = make('abc');
		let saved = 0;
		b.onSave = () => saved++;
		const binding = b.state.facet(keymap).flat().find((k) => k.key === 'Mod-s')!;
		expect(binding.run!({} as EditorView)).toBe(true);
		expect(binding.preventDefault).toBe(true);
		expect(saved).toBe(1);
	});
});
