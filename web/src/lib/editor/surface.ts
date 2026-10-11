// The editor's seam (plan m2i D3). The app talks to the code editor only through the
// types and the one lazy function below; the library itself lives in `cm/`, the only
// directory that imports `@codemirror/*` or `@lezer/*` (boundary.test.ts checks it), and is
// reached by a dynamic `import()` so its chunk is requested when a file opens, not at page
// load. A replacement editor implements `EditorHandle` and changes `loadEditor` and nothing else.

/** What a mounted editor is given at the start. */
export interface EditorOptions {
	/** The text to show. */
	doc: string;
	readOnly: boolean;
}

/** A problem to underline: lines and columns are 1-based, `end*` are optional. */
export interface EditorDiagnostic {
	line: number;
	col: number;
	endLine?: number;
	endCol?: number;
	severity: 'error' | 'warning' | 'info';
	message: string;
}

/** A mounted editor. Steps 4 (languages), 6 (editing, one state per tab) and 8 (diagnostics) extend it. */
export interface EditorHandle {
	/** Replaces the whole text; nothing happens when it is the text already shown. */
	setDocument(text: string): void;
	setReadOnly(readOnly: boolean): void;
	/** Moves the cursor to a 1-based line and column (clamped to the text) and scrolls it into view. */
	reveal(line: number, col: number): void;
	setDiagnostics(list: readonly EditorDiagnostic[]): void;
	/** Removes the editor from its host. */
	destroy(): void;
}

/** The module `cm/view.ts` exports; the only thing `loadEditor` must return. */
export interface EditorModule {
	mount(host: HTMLElement, opts: EditorOptions): EditorHandle;
}

export type LoadEditor = () => Promise<EditorModule>;

/** The one place the app names `cm/`: by a dynamic import. */
export const loadEditor: LoadEditor = () => import('./cm/view');

/**
 * An editor that is usable at once and mounted when its code has loaded: calls made before
 * that are remembered (the last text, the last read-only flag, the last reveal and diagnostics)
 * and applied at mount; a `destroy` before it cancels the mount. A failure to load or to mount
 * goes to `onError`.
 */
export class LazyEditor implements EditorHandle {
	#handle: EditorHandle | null = null;
	#gone = false;
	#doc: string;
	#readOnly: boolean;
	#reveal: [number, number] | null = null;
	#diagnostics: readonly EditorDiagnostic[] = [];

	constructor(host: HTMLElement, opts: EditorOptions, onError: (e: unknown) => void, load: LoadEditor = loadEditor) {
		this.#doc = opts.doc;
		this.#readOnly = opts.readOnly;
		load().then(
			(m) => {
				if (this.#gone) return;
				try {
					const h = m.mount(host, { doc: this.#doc, readOnly: this.#readOnly });
					this.#handle = h;
					if (this.#diagnostics.length > 0) h.setDiagnostics(this.#diagnostics);
					if (this.#reveal !== null) h.reveal(...this.#reveal);
				} catch (e) {
					onError(e);
				}
			},
			(e: unknown) => {
				if (!this.#gone) onError(e);
			},
		);
	}

	setDocument(text: string): void {
		this.#doc = text;
		this.#handle?.setDocument(text);
	}

	setReadOnly(readOnly: boolean): void {
		this.#readOnly = readOnly;
		this.#handle?.setReadOnly(readOnly);
	}

	reveal(line: number, col: number): void {
		if (this.#handle === null) this.#reveal = [line, col];
		else this.#handle.reveal(line, col);
	}

	setDiagnostics(list: readonly EditorDiagnostic[]): void {
		this.#diagnostics = list;
		this.#handle?.setDiagnostics(list);
	}

	destroy(): void {
		this.#gone = true;
		this.#handle?.destroy();
		this.#handle = null;
	}
}
