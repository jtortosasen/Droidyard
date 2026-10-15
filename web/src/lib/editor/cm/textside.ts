// A read-only side that no buffer stands behind (plan m2n step 9; ADR-0070 §4): a commit's file,
// or the parent's, shown in the Diff tab. It is a state made from a string; nothing mirrors it
// into a buffer and nothing follows one, because there is none.
//
//   - It cannot be edited by any route. `readOnly` and a non-editable DOM stop typing and the
//     commands, but a dispatched transaction still applies, so a transaction filter refuses every
//     one that changes the document. Selections, effects (the language, the gutter marks) pass.
//   - The language comes from the path, by its own dynamic import as the buffer's does; until it
//     has arrived (or for an unknown extension, or if the chunk fails) the text is plain.
import { MergeView } from '@codemirror/merge';
import { syntaxHighlighting } from '@codemirror/language';
import { Compartment, EditorState, type EditorStateConfig, type Extension, type Transaction } from '@codemirror/state';
import { EditorView, lineNumbers } from '@codemirror/view';
import type { EditorDiagnostic, EditorHandle, TextCompareOptions } from '../surface';
import { highlightStyle } from './highlight';
import { languageFor } from './languages';
import { COMPARE_CSS, mergeConfig, mergeTheme, sideCaptions } from './merge';
import { theme } from './theme';

const languageSlot = new Compartment();

/** Refuses any transaction that would change the document; the rest of it (selection, effects) is kept. */
export const refuseEdits = EditorState.transactionFilter.of((tr: Transaction) => (tr.docChanged ? [] : tr));

/** The state config of a read-only text: its document is `text` as it is (line breaks split as CodeMirror does). */
export function textSide(text: string, label: string, language: Extension = []): EditorStateConfig {
	return {
		doc: text,
		extensions: [
			EditorState.readOnly.of(true),
			EditorView.editable.of(false),
			refuseEdits,
			lineNumbers(),
			syntaxHighlighting(highlightStyle),
			languageSlot.of(language),
			EditorView.contentAttributes.of({ 'aria-label': label, 'aria-readonly': 'true' }),
			theme,
			mergeTheme,
		],
	};
}

/** The effect that gives a text side its language once it has loaded. */
export const useLanguage = (language: Extension) => languageSlot.reconfigure(language);

/** The language of a path, loaded; plain text (an empty extension) for an unknown one or a chunk that fails. */
export async function languageOf(path: string): Promise<Extension> {
	const entry = languageFor(path);
	if (entry === null) return [];
	try {
		return await entry.load();
	} catch {
		return [];
	}
}

/**
 * Compare of two read-only texts (ADR-0070 §4), in the same shadow-root host as the other views:
 * the first side is `opts.disk` (a commit's parent), the second `opts.text` (the commit). Neither
 * has a buffer, so nothing is mirrored back and nothing follows; `onSave` does not exist.
 */
class CmTextCompare implements EditorHandle {
	#merge: MergeView;
	#root: ShadowRoot;
	#sheet: CSSStyleSheet | null = null;
	#labels: HTMLElement;

	constructor(host: HTMLElement, opts: TextCompareOptions, language: Extension) {
		const root = host.shadowRoot ?? host.attachShadow({ mode: 'open' });
		root.replaceChildren();
		this.#root = root;
		if (typeof CSSStyleSheet !== 'undefined' && 'adoptedStyleSheets' in root) {
			const sheet = new CSSStyleSheet();
			sheet.replaceSync(COMPARE_CSS);
			root.adoptedStyleSheets = [...root.adoptedStyleSheets, sheet];
			this.#sheet = sheet;
		}
		this.#labels = document.createElement('div');
		this.#labels.className = 'droidyard-sides';
		for (const label of sideCaptions(opts)) {
			const span = document.createElement('span');
			span.textContent = label;
			this.#labels.append(span);
		}
		root.append(this.#labels);
		this.#merge = new MergeView(
			mergeConfig(
				textSide(opts.disk, opts.sideLabel ?? 'File contents', language),
				textSide(opts.text.text, opts.textLabel ?? 'File contents', language),
				root,
			),
		);
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
		this.#merge.destroy();
		this.#labels.remove();
		if (this.#sheet !== null) this.#root.adoptedStyleSheets = this.#root.adoptedStyleSheets.filter((s) => s !== this.#sheet);
	}
}

/** The two texts are laid out once the language of the path has loaded, so the first paint is already highlighted. */
export async function mountTextCompare(host: HTMLElement, opts: TextCompareOptions): Promise<EditorHandle> {
	return new CmTextCompare(host, opts, await languageOf(opts.text.path));
}
