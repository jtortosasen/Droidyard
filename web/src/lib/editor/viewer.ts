// The read-only viewer's model (plan m2d step 6; Q4, D26). One FileViewer per open file
// reads it with `file.read` in pages and holds the text whole; a ViewerSet owns the
// viewers and feeds them the root watch's events (files/watch.ts), so there is no second
// `file.watch`.
//
//   - D26's working default: at most CAP bytes are read, in PAGE-byte pages, and the view
//     then says so. A page that is not the next one in the file (the file changed under
//     the read) restarts the read from the top instead of joining text that never existed.
//   - A binary page (a NUL byte or invalid UTF-8) makes the file a placeholder: no text is
//     kept, only its size.
//   - `modified` (or `created`, a file that came back) for the path reads it again and
//     swaps the text in when it is complete; `deleted` marks the viewer and keeps the last
//     text. A re-sync (the watch restarted: events may have been missed) re-reads all.
//   - Text from a file is data: it reaches the page as text nodes only.
import type { Params, Result } from '../api/capabilities.gen';
import type { CallOptions } from '../api/client';
import { failure, failureText } from '../build/failure';
import type { Change, WatchListener } from '../files/watch';

/** D26: bytes of a file the viewer reads before it stops and says so. */
export const CAP = 2 * 1024 * 1024;
/** D26: the size of one `file.read` page (the capability's largest limit). */
export const PAGE = 256 * 1024;

export interface ViewerClient {
	call(name: 'file.read', params: Params<'file.read'>, opts?: CallOptions): Promise<Result<'file.read'>>;
}

export interface ViewerWorkspace {
	readonly root: string;
}

export type ViewerState = 'loading' | 'ready' | 'binary' | 'deleted' | 'error';

export interface ViewerView {
	state: ViewerState;
	path: string;
	/** The file's size in bytes, when known. */
	size: number | null;
	/** The text as read; empty for a binary file. */
	text: string;
	/** The text read stopped at CAP and the file is larger. */
	capped: boolean;
	/** Bytes of text held. */
	read: number;
	/** The failure as text (never markup), when state is 'error'. */
	error: string | null;
}

export class FileViewer {
	readonly path: string;
	#client: ViewerClient;
	#ws: ViewerWorkspace;
	#listeners = new Set<() => void>();
	#view: ViewerView;
	#run = 0;
	#disposed = false;

	constructor(client: ViewerClient, ws: ViewerWorkspace, path: string) {
		this.#client = client;
		this.#ws = ws;
		this.path = path;
		this.#view = { state: 'loading', path, size: null, text: '', capped: false, read: 0, error: null };
	}

	get view(): ViewerView {
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	dispose(): void {
		this.#disposed = true;
		this.#run++;
		this.#listeners.clear();
	}

	#set(patch: Partial<ViewerView>): void {
		this.#view = { ...this.#view, ...patch };
		for (const l of [...this.#listeners]) l();
	}

	/** The file was deleted: the last text stays, marked. */
	markDeleted(): void {
		this.#run++; // a read under way is for a file that is gone
		this.#set({ state: 'deleted' });
	}

	/** Reads the file; the text shown is replaced when the new read is complete. */
	async load(): Promise<void> {
		const run = ++this.#run;
		let text = '';
		let offset = 0;
		let size = 0;
		let restarts = 0;
		for (;;) {
			let page: Result<'file.read'>;
			try {
				page = await this.#client.call('file.read', { path: this.path, offset, limit: PAGE }, { workspace: this.#ws.root.trim() });
			} catch (e) {
				if (run === this.#run && !this.#disposed) this.#set({ state: 'error', error: failureText(failure(e)) });
				return;
			}
			if (run !== this.#run || this.#disposed) return;
			if (page.binary) {
				this.#set({ state: 'binary', size: page.size, text: '', capped: false, read: 0, error: null });
				return;
			}
			if (page.offset !== offset) {
				// Not the page asked for: the file changed under the read. Start over, a few times.
				if (++restarts > 3) {
					this.#set({ state: 'error', error: 'the file kept changing while it was read' });
					return;
				}
				text = '';
				offset = 0;
				continue;
			}
			text += page.text ?? '';
			size = page.size;
			// A page that moves nothing would loop for ever.
			const done = page.nextOffset <= offset || page.nextOffset >= size;
			offset = page.nextOffset;
			if (done || offset >= CAP) {
				this.#set({ state: 'ready', size, text, capped: offset < size, read: offset, error: null });
				return;
			}
		}
	}
}

/** The viewers of the open files, kept current by the root watch. */
export class ViewerSet implements WatchListener {
	#client: ViewerClient;
	#ws: ViewerWorkspace;
	#viewers = new Map<string, FileViewer>();

	constructor(client: ViewerClient, ws: ViewerWorkspace) {
		this.#client = client;
		this.#ws = ws;
	}

	/** The viewer of `path`, reading the file when it is first asked for. */
	get(path: string): FileViewer {
		let v = this.#viewers.get(path);
		if (v === undefined) {
			v = new FileViewer(this.#client, this.#ws, path);
			this.#viewers.set(path, v);
			void v.load();
		}
		return v;
	}

	/** Keeps only the viewers of these paths. */
	keep(paths: Iterable<string>): void {
		const keep = new Set(paths);
		for (const [p, v] of this.#viewers) {
			if (!keep.has(p)) {
				v.dispose();
				this.#viewers.delete(p);
			}
		}
	}

	changes(list: Change[]): void {
		for (const c of list) {
			const v = this.#viewers.get(c.path);
			if (v === undefined) continue;
			if (c.kind === 'deleted') v.markDeleted();
			else if (c.type === undefined || c.type === 'file') void v.load();
		}
	}

	resync(): void {
		for (const v of this.#viewers.values()) {
			// A viewer still reading its first answer has nothing out of date.
			if (v.view.state !== 'loading') void v.load();
		}
	}

	dispose(): void {
		this.keep([]);
	}
}

/** "1.2 MB": decimal units, as the placeholder and the header say them. */
export function formatSize(bytes: number): string {
	if (bytes < 1000) return `${bytes} B`;
	const units = ['kB', 'MB', 'GB'];
	let n = bytes / 1000;
	let i = 0;
	while (n >= 1000 && i < units.length - 1) {
		n /= 1000;
		i++;
	}
	return `${n < 10 ? n.toFixed(1) : Math.round(n)} ${units[i]}`;
}
