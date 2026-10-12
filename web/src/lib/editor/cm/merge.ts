// Compare (plan m2i D8): the disk's text, read-only, beside the editable buffer, in a
// `@codemirror/merge` MergeView mounted in the same shadow-root host as the editor (so the CSP
// answer is the editor's: constructed style sheets, nothing written into a <style> element).
//
//   - The buffer's side is built from the buffer's own extensions, so it has the tab's line
//     separator, the separator and paste filters, the history and the keymap with Mod-s. Its
//     edits are mirrored into the buffer as they are made: the buffer is the one document, a
//     save from here writes it, and leaving Compare keeps every edit with nothing to copy back.
//   - The buffer's side FOLLOWS the buffer (`follow`): a change the side did not make (an edit in
//     the Editor tab, a clean buffer's silent reload) is applied to it, so its next edit mirrors
//     back onto the text the buffer holds and never writes a stale one over it (plan m2l Q2 §2).
//   - Above them, "On disk" and "Yours" (or the caller's captions) say which side is which (text; colours from tokens).
//   - The disk's side is read-only and takes the disk's text as read. The diff is by line text
//     (a Text joins its lines with "\n"), so a CRLF buffer against an LF disk is not "all changed".
//   - Colours come from the UI's tokens (ui/tokens.css), in the theme below and nowhere else.
import { MergeView, type DiffConfig, type DirectMergeConfig } from '@codemirror/merge';
import { syntaxHighlighting } from '@codemirror/language';
import { Annotation, EditorState, Transaction, type ChangeSet, type EditorStateConfig } from '@codemirror/state';
import { EditorView, lineNumbers } from '@codemirror/view';
import type { CompareOptions, EditorDiagnostic, EditorHandle } from '../surface';
import { CmBuffer } from './buffer';
import { minimalChange } from './change';
import { highlightStyle } from './highlight';
import { isSaveKey } from './savekey';
import { theme } from './theme';

// A mix of a token with transparency keeps the diff colours in the two schemes with no literal.
export const mix = (token: string, pct: number): string => `color-mix(in srgb, var(${token}) ${pct}%, transparent)`;
const underline = (token: string): string => `linear-gradient(${mix(token, 60)}, ${mix(token, 60)}) bottom/100% 2px no-repeat`;

/** The merge view's colours over the tokens: the disk's side (a) in the danger colour, the buffer's (b) in the success one. */
export const mergeTheme = EditorView.theme({
	'&.cm-merge-a .cm-changedLine, .cm-deletedChunk': { backgroundColor: mix('--danger', 12) },
	'&.cm-merge-b .cm-changedLine, .cm-inlineChangedLine': { backgroundColor: mix('--success', 12) },
	'&.cm-merge-a .cm-changedText, .cm-deletedChunk .cm-deletedText': { background: underline('--danger') },
	'&.cm-merge-b .cm-changedText': { background: underline('--success') },
	'&.cm-merge-b .cm-deletedText': { background: mix('--danger', 20) },
	'&.cm-merge-a .cm-changedLineGutter, .cm-deletedLineGutter': { background: 'var(--danger)' },
	'&.cm-merge-b .cm-changedLineGutter': { background: 'var(--success)' },
	'.cm-inlineChangedLineGutter': { background: 'var(--accent)' },
});

/**
 * The one diff configuration of the Diff tab (ADR-0067 §1): MergeView in Classic and `Chunk.build` /
 * `Chunk.updateB` in Review take this very object, so the two modes cannot disagree about what changed.
 * It is the merge view's own default, written out.
 */
export const DIFF_CONFIG: DiffConfig = { scanLimit: 500 };

const DISK_LABEL = 'File contents on disk';

/** The two captions above the sides: the options' own, else `On disk` and `Yours`. */
export function sideCaptions(opts: Pick<CompareOptions, 'labels'>): [string, string] {
	return [opts.labels?.a ?? 'On disk', opts.labels?.b ?? 'Yours'];
}

/** The buffer's text as an editable side: its own separator, selection, history and keymap, plus the merge colours. Compare's `b` and Review's left. */
export function bufferSide(buffer: CmBuffer): EditorStateConfig {
	return {
		doc: buffer.text(),
		selection: buffer.state.selection,
		extensions: [buffer.extensions(), EditorView.contentAttributes.of({ 'aria-label': 'Your version' }), mergeTheme],
	};
}

/** The merge view's configuration over two side configs; its `diffConfig` is `DIFF_CONFIG`. */
export function mergeConfig(a: EditorStateConfig, b: EditorStateConfig, root: ShadowRoot): DirectMergeConfig {
	return { a, b, parent: root, root, diffConfig: DIFF_CONFIG };
}

/** The two documents of a Compare view: the disk's text read-only, the buffer's text editable with its own separator. */
export function compareSides(buffer: CmBuffer, disk: string, sideLabel = DISK_LABEL): { a: EditorStateConfig; b: EditorStateConfig } {
	return {
		a: {
			doc: disk,
			extensions: [
				EditorState.readOnly.of(true),
				EditorView.editable.of(false),
				lineNumbers(),
				syntaxHighlighting(highlightStyle),
				buffer.language,
				EditorView.contentAttributes.of({ 'aria-label': sideLabel, 'aria-readonly': 'true' }),
				theme,
				mergeTheme,
			],
		},
		b: bufferSide(buffer),
	};
}

/**
 * Carries the buffer's side's edit into the buffer. The two started as the same document, so the
 * change set applies as it is when the buffer still holds exactly the document the edit started
 * from; if they have drifted apart (the length alone proves nothing), the buffer takes the
 * side's text as the one smallest replacement. Returns whether the change set applied.
 */
export function mirror(buffer: CmBuffer, before: EditorState, after: EditorState, changes: ChangeSet): boolean {
	if (before.doc.eq(buffer.state.doc)) {
		buffer.dispatch({ changes, userEvent: 'input' });
		return true;
	}
	// Document positions, as `follow` takes them: `sliceDoc()` counts a CRLF as two.
	const change = minimalChange(buffer.state.doc.toString(), after.doc.toString());
	if (change !== null) {
		const insert = after.doc.slice(change.from, change.from + change.insert.length);
		buffer.dispatch({ changes: { from: change.from, to: change.to, insert }, userEvent: 'input' });
	}
	return false;
}

/** Marks the side's own transaction that carries a change of the buffer's in: the `mirrored` listener must not send it back. */
export const followed = Annotation.define<boolean>();

/**
 * What the buffer's side must do to hold the buffer's text again: null when it already does,
 * 'remount' when its line separator or read-only differs (a state made with the other
 * separator cannot be patched, and the side's extensions are fixed at its creation), else the
 * transaction that brings it there.
 *
 * The change is the smallest one over DOCUMENT positions (`doc.toString()`, where a break counts
 * once, whatever the separator), and its insert is taken from the buffer's own document as
 * `CmBuffer.#replace` takes it. `sliceDoc()` counts a CRLF as two, so offsets taken from it
 * corrupt a CRLF file. The transaction is not undoable on the side (the change is not the
 * user's) and carries `followed`.
 */
export function follow(side: EditorState, buffer: CmBuffer): Transaction | 'remount' | null {
	const bs = buffer.state;
	if (side.facet(EditorState.lineSeparator) !== bs.facet(EditorState.lineSeparator) || side.readOnly !== bs.readOnly) return 'remount';
	if (side.doc.eq(bs.doc)) return null;
	const change = minimalChange(side.doc.toString(), bs.doc.toString());
	if (change === null) return null;
	return side.update({
		changes: { from: change.from, to: change.to, insert: bs.doc.slice(change.from, change.from + change.insert.length) },
		annotations: [followed.of(true), Transaction.addToHistory.of(false)],
	});
}

/** The captions above the sides, shared with Review. */
export const SIDES_CSS = `
.droidyard-sides { flex: none; display: grid; grid-template-columns: 1fr 1fr; background: var(--bg-raised); border-bottom: 1px solid var(--border); color: var(--text-2); font-size: var(--fs-small); }
.droidyard-sides > span { padding: var(--sp-2) var(--sp-5); }
.droidyard-sides > span + span { border-left: 1px solid var(--border); }
`;
/** `.cm-mergeView` is the scroller (the merge view does not scroll by itself); a flex child of the host. */
const SHEET = `
.cm-mergeView { flex: 1 1 0; min-height: 0; overflow-y: auto; }
${SIDES_CSS}`;

class CmCompare implements EditorHandle {
	#merge!: MergeView;
	#buffer: CmBuffer;
	#opts: CompareOptions;
	#save: () => void;
	#prevSave: (() => void) | null;
	#root: ShadowRoot;
	#sheet: CSSStyleSheet | null = null;
	#labels: HTMLElement | null = null;
	#onKey: (e: KeyboardEvent) => void;
	#offChange: () => void;
	#gone = false;

	constructor(host: HTMLElement, opts: CompareOptions) {
		if (!(opts.buffer instanceof CmBuffer)) throw new Error('the buffer was not made by this editor');
		const buffer = opts.buffer;
		const root = host.shadowRoot ?? host.attachShadow({ mode: 'open' });
		root.replaceChildren();
		this.#buffer = buffer;
		this.#opts = opts;
		this.#root = root;
		// As in the editor: Mod-s in the search panel's inputs, outside the content's keymap, would open the browser's Save dialog.
		this.#onKey = (e) => {
			if (!isSaveKey(e)) return;
			e.preventDefault();
			opts.onSave();
		};
		root.addEventListener('keydown', this.#onKey as EventListener);
		if (typeof CSSStyleSheet !== 'undefined' && 'adoptedStyleSheets' in root) {
			const sheet = new CSSStyleSheet();
			sheet.replaceSync(SHEET);
			root.adoptedStyleSheets = [...root.adoptedStyleSheets, sheet];
			this.#sheet = sheet;
		}
		// Which side is which, as text above the two editors (equal columns, as the merge view lays them out).
		this.#labels = document.createElement('div');
		this.#labels.className = 'droidyard-sides';
		for (const label of sideCaptions(opts)) {
			const span = document.createElement('span');
			span.textContent = label;
			this.#labels.append(span);
		}
		root.append(this.#labels);
		this.#mountMerge();
		// The side follows the buffer: a change made elsewhere (the Editor tab, a silent reload) reaches it.
		this.#offChange = buffer.onChange(() => this.#follow());
		// Each mount installs its own handler and puts back the one it found, so a late mount
		// destroyed at once cannot leave the buffer with none (step 7 re-review N1).
		this.#prevSave = buffer.onSave;
		this.#save = () => opts.onSave();
		buffer.onSave = this.#save;
	}

	#mountMerge(): void {
		const buffer = this.#buffer;
		const { a, b } = compareSides(buffer, this.#opts.disk, this.#opts.sideLabel);
		// An edit made on this side goes to the buffer; one `follow` made is the buffer's already.
		const mirrored = EditorView.updateListener.of((u) => {
			if (u.docChanged && !u.transactions.some((t) => t.annotation(followed) === true)) mirror(buffer, u.startState, u.state, u.changes);
		});
		this.#merge = new MergeView(mergeConfig(a, { ...b, extensions: [b.extensions ?? [], mirrored] }, this.#root));
	}

	#follow(): void {
		if (this.#gone) return;
		const next = follow(this.#merge.b.state, this.#buffer);
		if (next === null) return;
		if (next === 'remount') {
			// The separator or read-only changed: the side is made again from the buffer, with its own selection kept by the buffer's state.
			this.#merge.destroy();
			this.#mountMerge();
			return;
		}
		this.#merge.b.dispatch(next);
	}

	reveal(line: number, col: number): void {
		const view = this.#merge.b;
		const doc = view.state.doc;
		const l = doc.line(Math.min(doc.lines, Math.max(1, Math.trunc(line))));
		const pos = l.from + Math.min(l.length, Math.max(0, Math.trunc(col) - 1));
		view.dispatch({ selection: { anchor: pos }, effects: EditorView.scrollIntoView(pos, { y: 'center' }) });
	}

	setDiagnostics(list: readonly EditorDiagnostic[]): void {
		void list;
	}

	destroy(): void {
		this.#gone = true;
		this.#offChange();
		this.#root.removeEventListener('keydown', this.#onKey as EventListener);
		if (this.#buffer.onSave === this.#save) this.#buffer.onSave = this.#prevSave;
		// The buffer already holds every edit; the side's own state (selection, history) is not kept.
		this.#merge.destroy();
		this.#labels?.remove();
		if (this.#sheet !== null) this.#root.adoptedStyleSheets = this.#root.adoptedStyleSheets.filter((s) => s !== this.#sheet);
	}
}

export function mountCompare(host: HTMLElement, opts: CompareOptions): EditorHandle {
	return new CmCompare(host, opts);
}
