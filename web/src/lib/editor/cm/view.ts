// The CodeMirror 6 editor behind surface.ts's `EditorHandle` (plan m2i D2, D3). This
// directory is the only one that imports `@codemirror/*`; the app reaches this module by a
// dynamic `import()` (surface.ts `loadEditor`), so its chunk loads when a file first opens.
//
// CSP (D2): the editor is mounted in an open shadow root on its host and given that root, so
// style-mod adopts a constructed style sheet instead of writing a <style> element, which
// `default-src 'self'` would refuse. The theme values are inherited custom properties.
// Seen at level 4 in Chrome (m2i step 3): no violation; mounted on the document, refused.
//
// The state is not made here: a `CmBuffer` (buffer.ts) owns it for as long as the file's tab
// lives, and a view only shows it (plan m2i D7), so a tab switch that destroys the view keeps
// the document, selection, undo history and scroll.
import { EditorView } from '@codemirror/view';
import type { BufferOptions, CompareOptions, EditorBuffer, EditorDiagnostic, EditorHandle, EditorOptions } from '../surface';
import { CmBuffer } from './buffer';
import { offsetOf } from './diagnostics';
import { isSaveKey } from './savekey';

class CmEditor implements EditorHandle {
	#view: EditorView;
	#buffer: CmBuffer;
	#save: () => void;
	#prevSave: (() => void) | null;
	#root: ShadowRoot;
	#onKey: (e: KeyboardEvent) => void;

	constructor(host: HTMLElement, opts: EditorOptions) {
		if (!(opts.buffer instanceof CmBuffer)) throw new Error('the buffer was not made by this editor');
		const buffer = opts.buffer;
		// An open shadow root, reused if the host had one (a root cannot be detached).
		const root = host.shadowRoot ?? host.attachShadow({ mode: 'open' });
		root.replaceChildren();
		this.#buffer = buffer;
		this.#root = root;
		// The content's keymap saves with Mod-s; the search panel's inputs are not content, and
		// there the browser would open its Save dialog. The keymap prevents the default when it
		// saves, so a chord that reaches the root unhandled is the panel's.
		this.#onKey = (e) => {
			if (!isSaveKey(e)) return;
			e.preventDefault();
			opts.onSave();
		};
		root.addEventListener('keydown', this.#onKey as EventListener);
		this.#view = new EditorView({ state: buffer.state, parent: root, root });
		// Each mount installs its own handler and puts back the one it found, so a late mount
		// destroyed at once cannot leave the buffer with none (step 7 re-review N1).
		this.#prevSave = buffer.onSave;
		this.#save = () => opts.onSave();
		buffer.onSave = this.#save;
		buffer.attach(this.#view);
		if (buffer.scroll !== null) this.#view.dispatch({ effects: buffer.scroll });
	}

	reveal(line: number, col: number): void {
		const pos = offsetOf(this.#view.state.doc, line, col);
		this.#view.dispatch({
			selection: { anchor: pos },
			effects: EditorView.scrollIntoView(pos, { y: 'center' }),
		});
	}

	// D10: kept by the buffer, so they outlive this view.
	setDiagnostics(list: readonly EditorDiagnostic[]): void {
		this.#buffer.setDiagnostics(list);
	}

	destroy(): void {
		this.#root.removeEventListener('keydown', this.#onKey as EventListener);
		if (this.#buffer.onSave === this.#save) this.#buffer.onSave = this.#prevSave;
		this.#buffer.detach(this.#view);
		this.#view.destroy();
	}
}

export function mount(host: HTMLElement, opts: EditorOptions): EditorHandle {
	return new CmEditor(host, opts);
}

// Compare's code (the merge view) is its own chunk, fetched when Compare is first opened.
export async function mountCompare(host: HTMLElement, opts: CompareOptions): Promise<EditorHandle> {
	return (await import('./merge')).mountCompare(host, opts);
}

export function createBuffer(opts: BufferOptions): EditorBuffer {
	return new CmBuffer(opts);
}
