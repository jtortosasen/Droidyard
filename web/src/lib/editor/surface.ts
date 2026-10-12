// The editor's seam (plan m2i D3). The app talks to the code editor only through the
// types and the one lazy function below; the library itself lives in `cm/`, the only
// directory that imports `@codemirror/*` or `@lezer/*` (boundary.test.ts checks it), and is
// reached by a dynamic `import()` so its chunk is requested when a file opens, not at page
// load. A replacement editor implements `EditorHandle` and changes `loadEditor` and nothing else.

/** What a buffer is made from. */
export interface BufferOptions {
	/** The file's text, byte for byte as read (its line separators included). */
	text: string;
	readOnly: boolean;
	/** The file's path or name: its extension picks the language to highlight; none or unknown is plain text. */
	path?: string;
}

/** The document as it was at one moment, and the means to call it the saved one. */
export interface BufferSnapshot {
	/** The whole text, with the file's line separator: the bytes a save writes. */
	readonly text: string;
	/** Makes this document the saved one; a no-op once `load` has replaced the document. */
	commit(): void;
}

/**
 * One file's editing state (plan m2i D7): its document, selection and undo history. It lives
 * as long as the file's tab, outside any mounted editor, so switching tabs keeps it; the
 * editor module makes it and shows it, and the app holds it as a value with this interface.
 */
export interface EditorBuffer {
	/** The document differs from the saved text. */
	readonly dirty: boolean;
	/** The whole text, with the file's line separator. */
	text(): string;
	snapshot(): BufferSnapshot;
	/**
	 * Replaces the document and the saved text with `text` (the disk's, taken by a clean buffer or
	 * by Reload). The replacement is one undoable history event of its own: Undo brings back the
	 * text as it was as a whole. When the line separator changes the state is rebuilt instead
	 * and the history starts again.
	 */
	load(text: string): void;
	/** The undo history holds something Redo would bring back: such a buffer is not clean enough to be replaced silently. */
	readonly hasRedo: boolean;
	/**
	 * Declares what the disk holds (`null`: nothing, the file is gone or not text) without touching
	 * the document, so the buffer counts as unsaved against it (D8 Keep mine).
	 */
	setSaved(text: string | null): void;
	setReadOnly(readOnly: boolean): void;
	/** Sets the file's path or name, and so the language; the highlighting follows once its code has loaded. */
	setPath(path: string): void;
	/** Calls `listener` whenever the document changes (the dirty flag may have flipped). */
	onChange(listener: () => void): () => void;
}

/** What a mounted editor is given at the start. */
export interface EditorOptions {
	/** The buffer to show, made by the same module's `createBuffer`. */
	buffer: EditorBuffer;
	/** Mod-s was pressed in the editor. */
	onSave(): void;
}

/** What a Compare view is given (plan m2i D8): the buffer, editable, beside the disk's text, read-only. */
export interface CompareOptions extends EditorOptions {
	/** The disk's text, as the file read gave it. */
	disk: string;
	/** The two captions above the sides; default `On disk` and `Yours`. The Diff tab says `HEAD` and `Working tree`. */
	labels?: { a: string; b: string };
	/** The read-only side's accessible name; default `File contents on disk`, so HEAD's text is not announced as the disk's. */
	sideLabel?: string;
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

/** A mounted editor, showing one buffer. */
export interface EditorHandle {
	/** Moves the cursor to a 1-based line and column (clamped to the text) and scrolls it into view. */
	reveal(line: number, col: number): void;
	/**
	 * Replaces the build errors marked in the buffer (D10); the marks belong to the buffer and
	 * follow its edits. The same array again changes nothing, so a caller passes one array per
	 * build result and not a copy per call.
	 */
	setDiagnostics(list: readonly EditorDiagnostic[]): void;
	/** Removes the editor from its host. */
	destroy(): void;
}

/** The module `cm/view.ts` exports; the only thing `loadEditor` must return. */
export interface EditorModule {
	mount(host: HTMLElement, opts: EditorOptions): EditorHandle;
	/**
	 * D8: the buffer beside the disk's text, in the same host. Edits made on the buffer's side
	 * are the buffer's (it is the same document, so Mod-s saves it and leaving keeps them).
	 */
	mountCompare(host: HTMLElement, opts: CompareOptions): Promise<EditorHandle>;
	createBuffer(opts: BufferOptions): EditorBuffer;
}

export type LoadEditor = () => Promise<EditorModule>;

/** The one place the app names `cm/`: by a dynamic import. */
export const loadEditor: LoadEditor = () => import('./cm/view');

/**
 * An editor that is usable at once and mounted when its code has loaded: calls made before
 * that are remembered (the last reveal and diagnostics) and applied at mount; a `destroy`
 * before it cancels the mount. A failure to load or to mount goes to `onError`.
 */
export class LazyEditor implements EditorHandle {
	#handle: EditorHandle | null = null;
	#gone = false;
	#reveal: [number, number] | null = null;
	#diagnostics: readonly EditorDiagnostic[] | null = null;

	constructor(
		host: HTMLElement,
		opts: EditorOptions,
		onError: (e: unknown) => void,
		load: LoadEditor = loadEditor,
		mount: (m: EditorModule, host: HTMLElement, opts: EditorOptions) => EditorHandle | Promise<EditorHandle> = (m, h, o) => m.mount(h, o),
	) {
		load().then(
			async (m) => {
				if (this.#gone) return;
				try {
					const h = await mount(m, host, opts);
					if (this.#gone) {
						h.destroy(); // closed while a further chunk (Compare's) was arriving
						return;
					}
					this.#handle = h;
					if (this.#diagnostics !== null) h.setDiagnostics(this.#diagnostics);
					if (this.#reveal !== null) h.reveal(...this.#reveal);
				} catch (e) {
					if (!this.#gone) onError(e);
				}
			},
			(e: unknown) => {
				if (!this.#gone) onError(e);
			},
		);
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
