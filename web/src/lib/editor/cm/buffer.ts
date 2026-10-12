// One file's editing state in CodeMirror (plan m2i D5, D7, D9). A `CmBuffer` holds the
// `EditorState` (document, selection, undo history) of an open file. It outlives any
// mounted view: a view shows it (`attach`), and when that view goes the last state is kept,
// so switching tabs, or a tab renamed in the tree, keeps the buffer and its history.
//
// The state is made without a DOM, so everything here runs in Node and is tested there.
//
//   - Separator (D5): a file whose breaks are all LF or all CRLF is edited with that
//     separator (`EditorState.lineSeparator`), so `text()` is the bytes read plus the edits;
//     pasted and dropped text is converted to it by `clipboardInputFilter`. A mixed file is
//     read-only and shown with CM's default splitting; its `text()` is not its bytes, which
//     is why it is never saved.
//   - Dirty (D7): the document differs from the saved one (`snapshot().commit()` moves it),
//     so undoing back to the saved text is clean again, and an edit made while a save was in
//     flight stays dirty because the saved document is the snapshot's, not the latest.
import { defaultKeymap, history, historyKeymap, indentWithTab, isolateHistory, redoDepth } from '@codemirror/commands';
import { foldGutter, foldKeymap, indentUnit, syntaxHighlighting } from '@codemirror/language';
import { highlightSelectionMatches, search, searchKeymap } from '@codemirror/search';
import { Compartment, EditorState, type Extension, type StateEffect, type Text, type TransactionSpec } from '@codemirror/state';
import {
	EditorView,
	drawSelection,
	highlightActiveLine,
	highlightActiveLineGutter,
	keymap,
	lineNumbers,
	rectangularSelection,
	type ViewUpdate,
} from '@codemirror/view';
import type { BufferOptions, BufferSnapshot, EditorBuffer } from '../surface';
import { detectSeparator, normalizeBreaks, type Separator } from '../text';
import { minimalChange } from './change';
import { highlightStyle } from './highlight';
import { languageFor } from './languages';
import { theme } from './theme';

const readOnlySlot = new Compartment();
const languageSlot = new Compartment();

/** D5: pasted or dropped text takes the file's separator; a file shown with CM's own splitting is left alone. */
export function pasted(text: string, state: EditorState): string {
	const sep = state.facet(EditorState.lineSeparator);
	return sep === '\n' || sep === '\r\n' ? normalizeBreaks(text, sep) : text;
}

/**
 * D5, for every way text gets in: an insertion whose lines hold a break that is not the file's
 * (a "\n" in a CRLF file, a CR in an LF one: a search-and-replace with `\n`, an
 * insertReplacementText, anything the clipboard filter does not see) has it turned into the
 * file's separator, in the same transaction. Text with only the right breaks passes untouched.
 */
export const separatorFilter = EditorState.transactionFilter.of((tr) => {
	if (!tr.docChanged) return tr;
	const sep = tr.startState.facet(EditorState.lineSeparator);
	if (sep !== '\n' && sep !== '\r\n') return tr;
	const foreign = sep === '\n' ? /\r/g : /[\r\n]/g;
	const fixes: { from: number; to: number; insert: string }[] = [];
	tr.changes.iterChanges((_fa, _ta, fromB, _tb, inserted) => {
		for (let n = 1; n <= inserted.lines; n++) {
			const line = inserted.line(n);
			foreign.lastIndex = 0;
			for (let m = foreign.exec(line.text); m !== null; m = foreign.exec(line.text)) {
				const at = fromB + line.from + m.index;
				// An LF file's "\r" ending a line that a break follows is half of a CRLF: dropped, not doubled.
				const half = sep === '\n' && n < inserted.lines && m.index === line.text.length - 1;
				fixes.push({ from: at, to: at + 1, insert: half ? '' : sep });
			}
		}
	});
	if (fixes.length === 0) return tr;
	return [tr, { changes: fixes, sequential: true }];
});

export class CmBuffer implements EditorBuffer {
	/** The current state; while a view is attached it is that view's. */
	state: EditorState;
	/** Where the last view was scrolled, applied by the next one. */
	scroll: StateEffect<unknown> | null = null;
	/** Called when Mod-s is pressed in an attached view. */
	onSave: (() => void) | null = null;

	/** The document the disk is taken to hold; null when it holds none (a gone or non-text file): always unsaved. */
	#saved: Text | null;
	#dirty = false;
	#sep: Separator | null;
	#readOnly: boolean;
	#lang: Extension = [];
	#ticket = 0;
	#epoch = 0;
	#loading = false;
	#view: EditorView | null = null;
	#listeners = new Set<() => void>();

	constructor(opts: BufferOptions) {
		this.#readOnly = opts.readOnly;
		this.#sep = detectSeparator(opts.text);
		this.state = this.#create(opts.text);
		this.#saved = this.state.doc;
		if (opts.path !== undefined) this.setPath(opts.path);
	}

	get dirty(): boolean {
		return this.#dirty;
	}

	get hasRedo(): boolean {
		return redoDepth(this.state) > 0;
	}

	setSaved(text: string | null): void {
		// toText splits by this buffer's separator, so a disk whose breaks differ is not "the same".
		this.#saved = text === null ? null : this.state.toText(text);
		this.#recompute();
	}

	text(): string {
		return this.state.sliceDoc();
	}

	snapshot(): BufferSnapshot {
		const doc = this.state.doc;
		const epoch = this.#epoch;
		return {
			text: this.state.sliceDoc(),
			commit: () => {
				if (epoch !== this.#epoch) return;
				this.#saved = doc;
				this.#recompute();
			},
		};
	}

	load(text: string): void {
		const sep = detectSeparator(text);
		this.#epoch++;
		// The edits below are the disk's, not the user's: nobody is told the buffer changed until it is clean again.
		this.#loading = true;
		try {
			this.#replace(text, sep);
		} finally {
			this.#loading = false;
		}
		this.#recompute();
	}

	#replace(text: string, sep: Separator | null): void {
		if (sep !== null && sep === this.#sep) {
			// The same separator: only the span that differs is replaced, so the cursor and the
			// scroll outside it stay. The replacement is a history event of its own (isolated from
			// the edits around it), so Undo brings back the text before it as a whole and never a
			// mapped mixture. Another separator rebuilds the state below, and the history starts again.
			const next = this.state.toText(text);
			const change = minimalChange(this.state.doc.toString(), next.toString());
			if (change !== null) {
				this.dispatch({
					changes: { from: change.from, to: change.to, insert: next.slice(change.from, change.from + change.insert.length) },
					annotations: isolateHistory.of('full'),
				});
			}
			this.#saved = this.state.doc;
			return;
		}
		// Another separator, or a mixed file: the document is made again.
		this.#sep = sep;
		this.state = this.#create(text);
		this.#saved = this.state.doc;
		this.#view?.setState(this.state);
	}

	setReadOnly(readOnly: boolean): void {
		this.#readOnly = readOnly;
		this.dispatch({ effects: readOnlySlot.reconfigure(EditorState.readOnly.of(readOnly)) });
	}

	// The language's code arrives by its own dynamic import; until then (and for a path with
	// no language, or if the chunk fails) the text is plain, which is a lesser result and not
	// an error worth a banner. A later path wins over an earlier one still loading.
	setPath(path: string): void {
		const ticket = ++this.#ticket;
		const entry = languageFor(path);
		const set = (ext: Extension) => {
			if (ticket !== this.#ticket) return;
			this.#lang = ext;
			this.dispatch({ effects: languageSlot.reconfigure(ext) });
		};
		if (entry === null) set([]);
		else entry.load().then(set, () => set([]));
	}

	onChange(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** A view shows this buffer from now on. */
	attach(view: EditorView): void {
		this.#view = view;
	}

	/** The view is going: its last state and scroll are kept for the next one. */
	detach(view: EditorView): void {
		if (this.#view !== view) return;
		this.state = view.state;
		this.scroll = view.scrollSnapshot();
		this.#view = null;
	}

	#create(text: string): EditorState {
		return EditorState.create({
			doc: text,
			extensions: [this.extensions(), EditorView.updateListener.of((u) => this.#updated(u))],
		});
	}

	/** The file's separator, or null for a mixed file (shown with CM's own splitting). */
	get separator(): Separator | null {
		return this.#sep;
	}

	/** The language extension on show now (plain text until its chunk has loaded). */
	get language(): Extension {
		return this.#lang;
	}

	/**
	 * Everything a view of this buffer needs but the link to the buffer: the file's separator,
	 * history, keymap with Mod-s, the paste and separator filters, the language and the theme.
	 * A state made from it behaves as the buffer does, which is how Compare's editable side
	 * (m2i D8) is built; it does not report its changes here, its owner mirrors them.
	 */
	extensions(): Extension[] {
		const sep = this.#sep;
		return [
			sep === null ? [] : EditorState.lineSeparator.of(sep),
			EditorState.allowMultipleSelections.of(true),
			readOnlySlot.of(EditorState.readOnly.of(this.#readOnly)),
			EditorState.tabSize.of(4),
			indentUnit.of('    '),
			history(),
			lineNumbers(),
			foldGutter(),
			highlightActiveLine(),
			highlightActiveLineGutter(),
			drawSelection(),
			rectangularSelection(),
			highlightSelectionMatches(),
			search({ top: true }),
			keymap.of([
				{
					key: 'Mod-s',
					preventDefault: true,
					run: () => {
						this.onSave?.();
						return true;
					},
				},
				...defaultKeymap,
				...historyKeymap,
				...searchKeymap,
				...foldKeymap,
				indentWithTab,
			]),
			EditorView.clipboardInputFilter.of(pasted),
			separatorFilter,
			EditorView.contentAttributes.of({ 'aria-label': 'File contents', 'aria-multiline': 'true' }),
			syntaxHighlighting(highlightStyle),
			languageSlot.of(this.#lang),
			theme,
		];
	}

	#updated(u: ViewUpdate): void {
		this.state = u.state;
		if (u.docChanged && !this.#loading) this.#recompute();
	}

	/** Applies a transaction to the state, through the attached view if there is one. */
	dispatch(spec: TransactionSpec): void {
		if (this.#view !== null) {
			this.#view.dispatch(spec); // the update listener keeps `state` current
			return;
		}
		const tr = this.state.update(spec);
		this.state = tr.state;
		if (tr.docChanged && !this.#loading) this.#recompute();
	}

	#recompute(): void {
		const doc = this.state.doc;
		this.#dirty = this.#saved === null || doc.length !== this.#saved.length || !doc.eq(this.#saved);
		for (const l of [...this.#listeners]) l();
	}
}
