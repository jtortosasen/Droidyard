// Compare (plan m2i D8): the disk's text, read-only, beside the editable buffer, in a
// `@codemirror/merge` MergeView mounted in the same shadow-root host as the editor (so the CSP
// answer is the editor's: constructed style sheets, nothing written into a <style> element).
//
//   - The buffer's side is built from the buffer's own extensions, so it has the tab's line
//     separator, the separator and paste filters, the history and the keymap with Mod-s. Its
//     edits are mirrored into the buffer as they are made: the buffer is the one document, a
//     save from here writes it, and leaving Compare keeps every edit with nothing to copy back.
//   - Above them, "On disk" and "Yours" say which side is which (text; colours from tokens).
//   - The disk's side is read-only and takes the disk's text as read. The diff is by line text
//     (a Text joins its lines with "\n"), so a CRLF buffer against an LF disk is not "all changed".
//   - Colours come from the UI's tokens (ui/tokens.css), in the theme below and nowhere else.
import { MergeView } from '@codemirror/merge';
import { syntaxHighlighting } from '@codemirror/language';
import { EditorState, type ChangeSet, type EditorStateConfig } from '@codemirror/state';
import { EditorView, lineNumbers } from '@codemirror/view';
import type { CompareOptions, EditorDiagnostic, EditorHandle } from '../surface';
import { CmBuffer } from './buffer';
import { minimalChange } from './change';
import { highlightStyle } from './highlight';
import { isSaveKey } from './savekey';
import { theme } from './theme';

// A mix of a token with transparency keeps the diff colours in the two schemes with no literal.
const mix = (token: string, pct: number): string => `color-mix(in srgb, var(${token}) ${pct}%, transparent)`;
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

/** The two documents of a Compare view: the disk's text read-only, the buffer's text editable with its own separator. */
export function compareSides(buffer: CmBuffer, disk: string): { a: EditorStateConfig; b: EditorStateConfig } {
	return {
		a: {
			doc: disk,
			extensions: [
				EditorState.readOnly.of(true),
				EditorView.editable.of(false),
				lineNumbers(),
				syntaxHighlighting(highlightStyle),
				buffer.language,
				EditorView.contentAttributes.of({ 'aria-label': 'File contents on disk', 'aria-readonly': 'true' }),
				theme,
				mergeTheme,
			],
		},
		b: {
			doc: buffer.text(),
			selection: buffer.state.selection,
			extensions: [buffer.extensions(), EditorView.contentAttributes.of({ 'aria-label': 'Your version' }), mergeTheme],
		},
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
	const change = minimalChange(buffer.text(), after.sliceDoc());
	if (change !== null) buffer.dispatch({ changes: change, userEvent: 'input' });
	return false;
}

/** `.cm-mergeView` is the scroller (the merge view does not scroll by itself); a flex child of the host. */
const SHEET = `
.cm-mergeView { flex: 1 1 0; min-height: 0; overflow-y: auto; }
.droidyard-sides { flex: none; display: grid; grid-template-columns: 1fr 1fr; background: var(--bg-raised); border-bottom: 1px solid var(--border); color: var(--text-2); font-size: var(--fs-small); }
.droidyard-sides > span { padding: var(--sp-2) var(--sp-5); }
.droidyard-sides > span + span { border-left: 1px solid var(--border); }
`;

class CmCompare implements EditorHandle {
	#merge: MergeView;
	#buffer: CmBuffer;
	#save: () => void;
	#prevSave: (() => void) | null;
	#root: ShadowRoot;
	#sheet: CSSStyleSheet | null = null;
	#labels: HTMLElement | null = null;
	#onKey: (e: KeyboardEvent) => void;

	constructor(host: HTMLElement, opts: CompareOptions) {
		if (!(opts.buffer instanceof CmBuffer)) throw new Error('the buffer was not made by this editor');
		const buffer = opts.buffer;
		const root = host.shadowRoot ?? host.attachShadow({ mode: 'open' });
		root.replaceChildren();
		this.#buffer = buffer;
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
		for (const label of ['On disk', 'Yours']) {
			const span = document.createElement('span');
			span.textContent = label;
			this.#labels.append(span);
		}
		root.append(this.#labels);
		const { a, b } = compareSides(buffer, opts.disk);
		const mirrored = EditorView.updateListener.of((u) => {
			if (u.docChanged) mirror(buffer, u.startState, u.state, u.changes);
		});
		this.#merge = new MergeView({ a, b: { ...b, extensions: [b.extensions ?? [], mirrored] }, parent: root, root });
		// Each mount installs its own handler and puts back the one it found, so a late mount
		// destroyed at once cannot leave the buffer with none (step 7 re-review N1).
		this.#prevSave = buffer.onSave;
		this.#save = () => opts.onSave();
		buffer.onSave = this.#save;
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
