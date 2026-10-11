// The CodeMirror 6 editor behind surface.ts's `EditorHandle` (plan m2i D2, D3). This
// directory is the only one that imports `@codemirror/*`; the app reaches this module by a
// dynamic `import()` (surface.ts `loadEditor`), so its chunk loads when a file first opens.
//
// CSP (D2): the editor is mounted in an open shadow root on its host and given that root, so
// style-mod adopts a constructed style sheet instead of writing a <style> element, which
// `default-src 'self'` would refuse. The theme values are inherited custom properties.
// Seen at level 4 in Chrome (m2i step 3): no violation; mounted on the document, refused.
//
// Read-only for now (editing is step 6, which adds per-tab state to the interface).
import { foldGutter } from '@codemirror/language';
import { Compartment, EditorState, Transaction, type Extension } from '@codemirror/state';
import {
	EditorView,
	drawSelection,
	highlightActiveLine,
	highlightActiveLineGutter,
	keymap,
	lineNumbers,
} from '@codemirror/view';
import type { EditorDiagnostic, EditorHandle, EditorOptions } from '../surface';
import { minimalChange } from './change';
import { theme } from './theme';

const readOnlySlot = new Compartment();

/** Ctrl/Cmd+End and +Home jump to the ends of the text (the default keymap arrives with editing). */
function jump(to: 'start' | 'end') {
	return (view: EditorView): boolean => {
		view.dispatch({ selection: { anchor: to === 'start' ? 0 : view.state.doc.length }, scrollIntoView: true });
		return true;
	};
}

function extensions(readOnly: boolean): Extension[] {
	return [
		readOnlySlot.of(EditorState.readOnly.of(readOnly)),
		EditorState.tabSize.of(4),
		lineNumbers(),
		foldGutter(),
		highlightActiveLine(),
		highlightActiveLineGutter(),
		drawSelection(),
		keymap.of([
			{ key: 'Mod-End', run: jump('end') },
			{ key: 'Ctrl-End', run: jump('end') },
			{ key: 'Mod-Home', run: jump('start') },
			{ key: 'Ctrl-Home', run: jump('start') },
		]),
		EditorView.contentAttributes.of({ 'aria-label': 'File contents', 'aria-multiline': 'true' }),
		theme,
	];
}

function clamp(n: number, lo: number, hi: number): number {
	return Math.min(hi, Math.max(lo, n));
}

class CmEditor implements EditorHandle {
	#view: EditorView;

	constructor(host: HTMLElement, opts: EditorOptions) {
		// An open shadow root, reused if the host had one (a root cannot be detached).
		const root = host.shadowRoot ?? host.attachShadow({ mode: 'open' });
		root.replaceChildren();
		this.#view = new EditorView({
			state: EditorState.create({ doc: opts.doc, extensions: extensions(opts.readOnly) }),
			parent: root,
			root,
		});
	}

	// Replaces only the span that differs, so a re-read (an agent's write, a re-sync) keeps
	// the scroll position and the cursor outside the change: CM maps them through the
	// change, and a whole-text replacement would map both to the start (step 3 verifier).
	setDocument(text: string): void {
		const change = minimalChange(this.#view.state.doc.toString(), text);
		if (change === null) return;
		this.#view.dispatch({ changes: change, annotations: Transaction.addToHistory.of(false) });
	}

	setReadOnly(readOnly: boolean): void {
		this.#view.dispatch({ effects: readOnlySlot.reconfigure(EditorState.readOnly.of(readOnly)) });
	}

	reveal(line: number, col: number): void {
		const doc = this.#view.state.doc;
		const l = doc.line(clamp(Math.trunc(line), 1, doc.lines));
		const pos = l.from + clamp(Math.trunc(col) - 1, 0, l.length);
		this.#view.dispatch({
			selection: { anchor: pos },
			effects: EditorView.scrollIntoView(pos, { y: 'center' }),
		});
	}

	// Diagnostics arrive with step 8 (@codemirror/lint); until then they are accepted, not drawn.
	setDiagnostics(list: readonly EditorDiagnostic[]): void {
		void list;
	}

	destroy(): void {
		this.#view.destroy();
	}
}

export function mount(host: HTMLElement, opts: EditorOptions): EditorHandle {
	return new CmEditor(host, opts);
}
