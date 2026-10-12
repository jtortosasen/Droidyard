// The editor's models (plan m2d step 6, m2i step 6; D5-D8). One EditorModel per open file
// reads it with `file.read` in pages, holds its editing buffer, and saves it with
// `file.write`; an EditorSet (here `ViewerSet`) owns the models and feeds them the root
// watch's events (files/watch.ts), so there is no second `file.watch`.
//
//   - A read is consistent (D6): every page of one read must carry the same `revision`, and
//     a complete read with a revision must hash to it (WebCrypto). A page at another offset
//     or with another revision starts the read again, at most three times. A whole-file
//     mismatch with equal page revisions (a final line cut mid-rune) opens the file read-only.
//   - D26's working default: at most CAP bytes are read, in PAGE-byte pages.
//   - Read-only, with the reason as text (D5): a binary file is a placeholder with no text;
//     a file over 512 KiB, a capped read, a read without a revision, mixed line endings or a
//     lone CR, and a hash mismatch are shown and never saved.
//   - Save (D7): `file.write {path, content, ifRevision: base}`, one at a time; a save asked
//     for during one waits and goes out with the new base. The content is hashed before the
//     call, so the revision the daemon will report is known while it is in flight, and a
//     watch event carrying it is our own echo. Nothing is sent over 512 KiB or ~1,000,000
//     bytes of JSON.
//   - External change (D8, step 6 part): `modified`/`created` and a re-sync re-read the file;
//     a revision equal to the base or in flight is the echo of our own write and changes
//     nothing; a clean buffer takes the disk's text; a dirty one is never reloaded (the
//     save's `ifRevision` catches the difference; step 7 adds the bar).
//   - Text from a file is data: it reaches the page as text nodes only.
import type { Params, Result } from '../api/capabilities.gen';
import type { CallOptions } from '../api/client';
import { failure, failureText } from '../build/failure';
import type { Change, WatchListener } from '../files/watch';
import { loadEditor, type EditorBuffer, type EditorModule } from './surface';
import { detectSeparator, MAX_WRITE, saveRefusal, sha256Hex } from './text';

/** D26: bytes of a file the viewer reads before it stops and says so. */
export const CAP = 2 * 1024 * 1024;
/** D26: the size of one `file.read` page (the capability's largest limit). */
export const PAGE = 256 * 1024;
/** D6: restarts of one read before it fails. */
export const RESTARTS = 3;

export interface ViewerClient {
	call(name: 'file.read', params: Params<'file.read'>, opts?: CallOptions): Promise<Result<'file.read'>>;
	call(name: 'file.write', params: Params<'file.write'>, opts?: CallOptions): Promise<Result<'file.write'>>;
}

export interface ViewerWorkspace {
	readonly root: string;
}

/** Where buffers come from: the editor module, loaded when the first file opens. */
export type LoadBuffers = () => Promise<Pick<EditorModule, 'createBuffer'>>;

export type ViewerState = 'loading' | 'ready' | 'binary' | 'deleted' | 'error';

/** Why a text is shown and not edited (D5). */
export type ReadOnlyReason = 'large' | 'capped' | 'norevision' | 'mixed' | 'unverified';

export function readOnlyText(reason: ReadOnlyReason): string {
	switch (reason) {
		case 'large':
			return 'Read-only: the file is over 512 KiB, which the editor does not save.';
		case 'capped':
			return 'Read-only: the file is longer than the editor reads, so a save would cut it.';
		case 'norevision':
			return 'Read-only: the daemon gave this file no revision (it is over 8 MiB), so a save could not be checked.';
		case 'mixed':
			return 'Read-only: the file mixes line endings (or has a lone CR), which a save could not write back as read.';
		case 'unverified':
			return 'Read-only: the text read does not match the file\u2019s revision (a line was cut or the file changed while it was read).';
	}
}

export interface ViewerView {
	state: ViewerState;
	path: string;
	/** The file's size in bytes, when known. */
	size: number | null;
	/** The text read stopped at CAP and the file is larger. */
	capped: boolean;
	/** Bytes of text held. */
	read: number;
	/** The failure as text (never markup), when state is 'error'. */
	error: string | null;
	/** The editing buffer of a text file (also while it is marked deleted); null for the rest. */
	buffer: EditorBuffer | null;
	/** Why the buffer cannot be edited, or null when it can. */
	readOnly: ReadOnlyReason | null;
	/** The buffer holds changes the disk does not. */
	dirty: boolean;
	saving: boolean;
	/** The last save's failure as text, or null. */
	saveError: string | null;
	/** The last save was answered FILE_REVISION_CONFLICT: the file changed on disk after it was read. */
	conflict: boolean;
}

/** What one consistent read found. */
type Read =
	| { kind: 'error'; message: string }
	| { kind: 'binary'; size: number }
	| { kind: 'text'; text: string; size: number; read: number; capped: boolean; revision: string | undefined; matches: boolean };

/** D5: the first reason a complete or capped read of text cannot be edited. */
function reasonFor(r: Extract<Read, { kind: 'text' }>): ReadOnlyReason | null {
	if (r.capped) return 'capped';
	if (r.size > MAX_WRITE) return 'large';
	if (r.revision === undefined) return 'norevision';
	if (!r.matches) return 'unverified';
	if (detectSeparator(r.text) === null) return 'mixed';
	return null;
}

export class EditorModel {
	path: string;
	#client: ViewerClient;
	#ws: ViewerWorkspace;
	#loadBuffers: LoadBuffers;
	#listeners = new Set<() => void>();
	#view: ViewerView;
	#run = 0;
	#disposed = false;
	#buffer: EditorBuffer | null = null;
	#offBuffer: (() => void) | null = null;
	/** The revision of the disk text the buffer is based on; undefined for a read without one. */
	#base: string | undefined;
	/** Bumped when a save starts and when it ends: a read begun before is out of date. */
	#gen = 0;
	#saving = false;
	#inflight: string | null = null;
	#queued = false;
	#stale = false;
	#saveDone: Promise<void> = Promise.resolve();

	constructor(client: ViewerClient, ws: ViewerWorkspace, path: string, loadBuffers: LoadBuffers = loadEditor) {
		this.#client = client;
		this.#ws = ws;
		this.path = path;
		this.#loadBuffers = loadBuffers;
		this.#view = {
			state: 'loading',
			path,
			size: null,
			capped: false,
			read: 0,
			error: null,
			buffer: null,
			readOnly: null,
			dirty: false,
			saving: false,
			saveError: null,
			conflict: false,
		};
	}

	get view(): ViewerView {
		return this.#view;
	}

	/** The revision the buffer is based on (the disk's text when it was read or last saved). */
	get base(): string | undefined {
		return this.#base;
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
		this.#offBuffer?.();
		this.#offBuffer = null;
		this.#listeners.clear();
	}

	#set(patch: Partial<ViewerView>): void {
		this.#view = { ...this.#view, ...patch };
		for (const l of [...this.#listeners]) l();
	}

	/** The file was deleted: the buffer stays, marked. */
	markDeleted(): void {
		this.#run++; // a read under way is for a file that is gone
		this.#set({ state: 'deleted' });
	}

	/** The file was renamed or moved in the tree (D7): the same buffer, base and history, under the new path. */
	rename(to: string): void {
		this.path = to;
		this.#run++; // a read under way was for the old path
		this.#buffer?.setPath(to);
		// The move's own events may have reached this model before its reply did, marking it
		// deleted at the old path: read the new one to see whether the mark still holds.
		const reread = this.#view.state === 'loading' || this.#view.state === 'deleted';
		this.#set({ path: to });
		if (reread) void this.load();
	}

	/** Reads the file; what happens to the result depends on the buffer (see the header). */
	async load(): Promise<void> {
		const run = ++this.#run;
		const gen = this.#gen;
		const read = await this.#read(run);
		if (read === null || run !== this.#run || this.#disposed) return;
		if (gen !== this.#gen || this.#saving) {
			// A save began or ended while this read was out: it may show the file from before it.
			// Only the revision of the write in flight is our own echo. The base's bytes seen now
			// are somebody writing the old text back during our save: read again after it.
			if (this.#saving && read.kind === 'text' && read.revision !== undefined && read.revision === this.#inflight) return;
			if (this.#saving) this.#stale = true;
			else void this.load();
			return;
		}
		await this.#apply(read, run);
	}

	/** One consistent read (D6); null when it was overtaken. */
	async #read(run: number): Promise<Read | null> {
		let text = '';
		let offset = 0;
		let size = 0;
		let restarts = 0;
		let revision: string | undefined;
		for (;;) {
			let page: Result<'file.read'>;
			try {
				page = await this.#client.call('file.read', { path: this.path, offset, limit: PAGE }, { workspace: this.#ws.root.trim() });
			} catch (e) {
				return run === this.#run && !this.#disposed ? { kind: 'error', message: failureText(failure(e)) } : null;
			}
			if (run !== this.#run || this.#disposed) return null;
			if (page.binary) return { kind: 'binary', size: page.size };
			// A page that is not the one asked for, or whose revision differs from the first
			// page's: the file changed under the read. Start over, a few times.
			if (page.offset !== offset || (offset > 0 && page.revision !== revision)) {
				if (++restarts > RESTARTS) return { kind: 'error', message: 'the file kept changing while it was read' };
				text = '';
				offset = 0;
				revision = undefined;
				continue;
			}
			if (offset === 0) revision = page.revision;
			text += page.text ?? '';
			size = page.size;
			// A page that moves nothing would loop for ever.
			const done = page.nextOffset <= offset || page.nextOffset >= size;
			offset = page.nextOffset;
			if (done || offset >= CAP) {
				const capped = offset < size;
				let matches = true;
				if (!capped && revision !== undefined) {
					try {
						matches = (await sha256Hex(text)) === revision;
					} catch {
						matches = false; // no WebCrypto: the read cannot be verified
					}
					if (run !== this.#run || this.#disposed) return null;
				}
				return { kind: 'text', text, size, read: offset, capped, revision, matches };
			}
		}
	}

	async #apply(read: Read, run: number): Promise<void> {
		if (read.kind === 'error') {
			// A buffer on show is not replaced by a failed re-read; the next event tries again.
			if (this.#buffer === null) this.#set({ state: 'error', error: read.message });
			return;
		}
		if (read.kind === 'binary') {
			if (this.#buffer?.dirty) return; // the user's edits are not dropped for a file that turned binary (step 7 asks)
			this.#dropBuffer();
			this.#base = undefined;
			this.#set({ state: 'binary', size: read.size, capped: false, read: 0, error: null, buffer: null, readOnly: null, dirty: false });
			return;
		}
		const reason = reasonFor(read);
		const buffer = this.#buffer;
		if (buffer === null) {
			let made: EditorBuffer;
			try {
				made = (await this.#loadBuffers()).createBuffer({ text: read.text, readOnly: reason !== null, path: this.path });
			} catch (e) {
				if (run === this.#run && !this.#disposed) this.#set({ state: 'error', error: failureText(failure(e)) });
				return;
			}
			if (run !== this.#run || this.#disposed) return;
			this.#buffer = made;
			this.#offBuffer = made.onChange(() => {
				if (made.dirty !== this.#view.dirty) this.#set({ dirty: made.dirty });
			});
			this.#base = read.revision;
			this.#set({ state: 'ready', size: read.size, capped: read.capped, read: read.read, error: null, buffer: made, readOnly: reason, dirty: false });
			return;
		}
		// The echo of our own write, or of a move: the same bytes as the buffer is based on.
		if (read.revision !== undefined && (read.revision === this.#base || read.revision === this.#inflight)) {
			if (this.#view.state === 'deleted') this.#set({ state: 'ready' });
			return;
		}
		if (buffer.dirty) {
			// D8: never reloaded (step 7 shows the bar). The file exists again, though: it is not "deleted".
			if (this.#view.state === 'deleted') this.#set({ state: 'ready' });
			return;
		}
		buffer.load(read.text);
		buffer.setReadOnly(reason !== null);
		this.#base = read.revision;
		this.#set({
			state: 'ready',
			size: read.size,
			capped: read.capped,
			read: read.read,
			error: null,
			readOnly: reason,
			dirty: buffer.dirty,
			saveError: null, // a failed save of the text this replaces is moot
			conflict: false,
		});
	}

	#dropBuffer(): void {
		this.#offBuffer?.();
		this.#offBuffer = null;
		this.#buffer = null;
	}

	/**
	 * Saves the buffer (D7). Resolves when this save, and any asked for during it, is done. A
	 * save asked for during one is not lost and not concurrent: it goes out after, with the
	 * new base, if the buffer is still dirty then.
	 */
	save(): Promise<void> {
		if (this.#saving) {
			this.#queued = true;
			return this.#saveDone;
		}
		this.#saveDone = this.#saveLoop();
		return this.#saveDone;
	}

	async #saveLoop(): Promise<void> {
		this.#saving = true;
		this.#gen++;
		this.#set({ saving: true });
		try {
			do {
				this.#queued = false;
				if (!(await this.#saveOnce())) this.#queued = false; // a failed save is not repeated blindly
			} while (this.#queued);
		} finally {
			this.#saving = false;
			this.#inflight = null;
			this.#gen++;
			this.#set({ saving: false });
			if (this.#stale && !this.#disposed) {
				this.#stale = false;
				void this.load();
			}
		}
	}

	/** One write; false when it failed (the reason is in the view). */
	async #saveOnce(): Promise<boolean> {
		const buffer = this.#buffer;
		if (buffer === null || this.#view.readOnly !== null || !buffer.dirty) return true;
		const base = this.#base;
		if (base === undefined) {
			this.#set({ saveError: 'The file has no revision to save against; read it again.', conflict: false });
			return false;
		}
		const snap = buffer.snapshot();
		const path = this.path;
		const refusal = saveRefusal(path, snap.text, base);
		if (refusal !== null) {
			this.#set({ saveError: refusal, conflict: false });
			return false;
		}
		let revision: string;
		try {
			revision = await sha256Hex(snap.text);
		} catch (e) {
			this.#set({ saveError: failureText(failure(e)), conflict: false });
			return false;
		}
		this.#inflight = revision;
		this.#set({ saveError: null, conflict: false });
		try {
			const r = await this.#client.call('file.write', { path, content: snap.text, ifRevision: base }, { workspace: this.#ws.root.trim() });
			this.#base = r.revision;
			snap.commit();
			this.#set({ size: r.size, saveError: null, conflict: false, dirty: buffer.dirty, ...(this.#view.state === 'deleted' ? { state: 'ready' as const } : {}) });
			return true;
		} catch (e) {
			const f = failure(e);
			this.#set({ saveError: failureText(f), conflict: f.code === 'FILE_REVISION_CONFLICT' });
			return false;
		}
	}
}

/** What the set tells its owner. */
export interface SetHooks {
	/** A model's dirty flag changed (the tab's mark, and the first edit keeps a preview tab). */
	dirty?(path: string, dirty: boolean): void;
}

const under = (path: string, dir: string): boolean => path === dir || path.startsWith(`${dir}/`);

/** The models of the open files, kept current by the root watch. */
export class ViewerSet implements WatchListener {
	#client: ViewerClient;
	#ws: ViewerWorkspace;
	#hooks: SetHooks;
	#load: LoadBuffers;
	#models = new Map<string, EditorModel>();

	constructor(client: ViewerClient, ws: ViewerWorkspace, hooks: SetHooks = {}, load: LoadBuffers = loadEditor) {
		this.#client = client;
		this.#ws = ws;
		this.#hooks = hooks;
		this.#load = load;
	}

	/** The model of `path`, reading the file when it is first asked for. */
	get(path: string): EditorModel {
		let m = this.#models.get(path);
		if (m === undefined) {
			m = new EditorModel(this.#client, this.#ws, path, this.#load);
			const model = m;
			let dirty = false;
			model.subscribe(() => {
				if (model.view.dirty !== dirty) {
					dirty = model.view.dirty;
					this.#hooks.dirty?.(model.path, dirty);
				}
			});
			this.#models.set(path, m);
			void m.load();
		}
		return m;
	}

	/** Keeps only the models of these paths, and any model with unsaved changes (those go by `discard`). */
	keep(paths: Iterable<string>): void {
		const keep = new Set(paths);
		for (const [p, m] of this.#models) {
			if (!keep.has(p) && !m.view.dirty) {
				m.dispose();
				this.#models.delete(p);
			}
		}
	}

	/** Drops the models of these paths with their unsaved changes (the user's "Don't save"). */
	discard(paths: Iterable<string>): void {
		for (const p of paths) {
			const m = this.#models.get(p);
			if (m === undefined) continue;
			m.dispose();
			this.#models.delete(p);
			this.#hooks.dirty?.(p, false);
		}
	}

	/** The paths of the models with unsaved changes. */
	dirtyPaths(): string[] {
		return [...this.#models].filter(([, m]) => m.view.dirty).map(([p]) => p);
	}

	/** Whether `path`, or a file under it, is open with unsaved changes (D7: a move or paste may not land there). */
	holdsDirty(path: string): boolean {
		for (const [p, m] of this.#models) if (m.view.dirty && under(p, path)) return true;
		return false;
	}

	/** Whether an open file directly in `dir` ('' is the root) is marked deleted and has unsaved changes. */
	goneDirtyIn(dir: string): boolean {
		for (const [p, m] of this.#models) {
			if (m.view.dirty && m.view.state === 'deleted' && p.slice(0, Math.max(0, p.lastIndexOf('/'))) === dir) return true;
		}
		return false;
	}

	/** Saves these paths in turn; the first failure's text, or null when all are saved. */
	async saveAll(paths: readonly string[]): Promise<string | null> {
		for (const p of paths) {
			const m = this.#models.get(p);
			if (m === undefined) continue;
			await m.save();
			if (m.view.dirty) return `${p}: ${m.view.saveError ?? 'not saved'}`;
		}
		return null;
	}

	/**
	 * A file or folder was moved or renamed (D7), before the tabs follow: the models under it
	 * are re-keyed (by path prefix, never a sibling sharing the prefix), keeping buffer, base and
	 * history. A model at a target path is replaced by the moved one unless it has unsaved
	 * changes (FileOps refuses a move onto one, so that is a safety net: both are kept, the
	 * moved one under its old path).
	 */
	moved(from: string, to: string): void {
		const re = (p: string): string => (p === from ? to : p.startsWith(`${from}/`) ? to + p.slice(from.length) : p);
		const moving = [...this.#models].filter(([p]) => re(p) !== p);
		if (moving.length === 0) return;
		for (const [p] of moving) this.#models.delete(p);
		for (const [p, m] of moving) {
			const np = re(p);
			const there = this.#models.get(np);
			if (there?.view.dirty) {
				// The model at the target has unsaved text and stays. A clean moved one gives way; one
				// with unsaved text too (FileOps refuses that move, so this is the safety net) is kept
				// under its old path rather than lose its edits.
				if (m.view.dirty) this.#models.set(p, m);
				else m.dispose();
				continue;
			}
			there?.dispose();
			this.#models.set(np, m);
			m.rename(np);
		}
	}

	changes(list: Change[]): void {
		for (const c of list) {
			const m = this.#models.get(c.path);
			if (m === undefined) continue;
			if (c.kind === 'deleted') m.markDeleted();
			else if (c.type === undefined || c.type === 'file') void m.load();
		}
	}

	resync(): void {
		for (const m of this.#models.values()) {
			// A model still reading its first answer has nothing out of date.
			if (m.view.state !== 'loading') void m.load();
		}
	}

	dispose(): void {
		for (const m of this.#models.values()) m.dispose();
		this.#models.clear();
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
