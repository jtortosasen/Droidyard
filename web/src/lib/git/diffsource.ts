// What the Diff tab shows (plan m2n step 9; ADR-0070 §4, after ADR-0067 §2): two sources.
//   - the working tree: the Editor's active file against HEAD (`HeadBase`, `base.ts`), the
//     buffer side editable and following the buffer. This is the default and what the tab returns
//     to whenever a file is opened from the Editor or the Git tree;
//   - a commit's file: the file at that commit against the same file at the commit's FIRST parent
//     (ADR-0069 §5), both read-only text with no buffer behind either. A rename is read at its
//     `origPath` at the parent; a merge's first parent is the one `git.show` diffed against; an
//     added file has an empty base; a deleted file has no text at the commit (the right pane alone).
//
// `DiffSource` says which source is shown; `CommitText` reads the two texts of a commit's file
// with `git.read` (its `base` parameter is the commit's full object id) and says what to show:
// the texts, or a message (a binary or too-large file, a refusal). A read that a newer file
// overtook is ignored.
import type { Params, Result } from '../api/capabilities.gen';
import type { CallOptions } from '../api/client';
import { failure, failureText } from '../build/failure';
import type { Show, ShowFile } from './files';

/** A changed file of a commit, with the two commits it is read at. */
export interface CommitFile {
	/** The root the commit belongs to: `.` for the workspace, else a submodule path (plan m2n P7); the paths below are workspace-relative either way. */
	root: string;
	/** The commit, in full. */
	oid: string;
	/** The commit read as the before side: its first parent in full (null for a root commit), or the base of a comparison. */
	parent: string | null;
	path: string;
	/** For a renamed file: the path the parent holds it at. */
	origPath?: string;
	status: ShowFile['status'];
	binary: boolean;
}

export type SourceView = { kind: 'worktree' } | { kind: 'commit'; file: CommitFile };

const WORKTREE: SourceView = { kind: 'worktree' };

const fileKey = (f: CommitFile): string => JSON.stringify([f.root, f.oid, f.parent, f.path, f.origPath ?? null, f.status, f.binary]);

/** Which source the Diff tab shows. */
export class DiffSource {
	#view: SourceView = WORKTREE;
	#listeners = new Set<() => void>();

	get view(): SourceView {
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** Shows a commit's file. The same file again changes nothing. */
	showCommit(file: CommitFile): void {
		const v = this.#view;
		if (v.kind === 'commit' && fileKey(v.file) === fileKey(file)) return;
		this.#set({ kind: 'commit', file });
	}

	/** Returns to the working tree (a file opened from the Editor or the Git tree). Already there changes nothing. */
	showWorktree(): void {
		if (this.#view.kind === 'worktree') return;
		this.#set(WORKTREE);
	}

	dispose(): void {
		this.#listeners.clear();
	}

	#set(v: SourceView): void {
		this.#view = v;
		for (const l of [...this.#listeners]) l();
	}
}

/** A changed file of the commit `show` describes: read at its first parent. */
export function commitFileOf(show: Pick<Show, 'oid' | 'parents'>, f: Pick<ShowFile, 'path' | 'origPath' | 'status' | 'binary'>, root = '.'): CommitFile {
	return {
		root,
		oid: show.oid,
		parent: show.parents[0] ?? null,
		path: f.path,
		...(f.origPath !== undefined ? { origPath: f.origPath } : {}),
		status: f.status,
		binary: f.binary,
	};
}

/**
 * A file between two tips (Compare with current, ADR-0072 §3): read at `base` (HEAD, the before side) and at `tip`
 * (the after side). A renamed file is read at its `origPath` at the base.
 */
export function compareFileOf(tip: string, base: string, f: Pick<ShowFile, 'path' | 'origPath' | 'status' | 'binary'>): CommitFile {
	return commitFileOf({ oid: tip, parents: [base] }, f);
}

export const shortOid = (oid: string): string => oid.slice(0, 7);

/** What stands for the parent of a root commit, which has none: its files are all added against nothing. */
export const NO_PARENT = 'no parent';

/** The Diff tab's header text: `<path> · <short> vs <parent short>`. */
export function commitHeader(f: CommitFile): string {
	return `${f.path} · ${shortOid(f.oid)} vs ${f.parent === null ? NO_PARENT : shortOid(f.parent)}`;
}

export interface CommitTextClient {
	call(name: 'git.read', params: Params<'git.read'>, opts?: CallOptions): Promise<Result<'git.read'>>;
}

export interface CommitTextWorkspace {
	readonly root: string;
}

/** The two texts of a commit's file, or why there are none. */
export type CommitTextView =
	| { kind: 'idle' }
	| { kind: 'loading' }
	/** `before` is the first parent's text ('' for an added file or a root commit); `after` the commit's, null for a deleted file. */
	| { kind: 'text'; before: string; after: string | null }
	| { kind: 'message'; text: string };

export const BINARY_TEXT = 'This file is binary; there is no text to compare.';
export const TOO_LARGE_TEXT = 'This file is over 512 KiB, which the editor does not compare.';

type Side = { text: string } | { message: string };

function side(r: Result<'git.read'>): Side {
	if (r.binary) return { message: BINARY_TEXT };
	if (r.tooLarge) return { message: TOO_LARGE_TEXT };
	return { text: r.exists ? (r.text ?? '') : '' };
}

export class CommitText {
	#client: CommitTextClient;
	#ws: CommitTextWorkspace;
	#listeners = new Set<() => void>();
	#view: CommitTextView = { kind: 'idle' };
	#key: string | null = null;
	#ticket = 0;

	constructor(client: CommitTextClient, ws: CommitTextWorkspace) {
		this.#client = client;
		this.#ws = ws;
	}

	get view(): CommitTextView {
		return this.#view;
	}

	subscribe(listener: () => void): () => void {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	/** Points the model at a commit's file, or at none (`null`). The same file again reads nothing. */
	follow(file: CommitFile | null): void {
		if (file === null) {
			this.#key = null;
			this.#ticket++;
			this.#set({ kind: 'idle' });
			return;
		}
		const key = fileKey(file);
		if (key === this.#key) return;
		this.#key = key;
		const ticket = ++this.#ticket;
		if (file.binary) {
			this.#set({ kind: 'message', text: BINARY_TEXT });
			return;
		}
		this.#set({ kind: 'loading' });
		const opts = { workspace: this.#ws.root.trim() };
		const at = file.root === '.' ? {} : { root: file.root };
		// Added (or a root commit): nothing to read at the parent. Deleted: nothing at the commit.
		const before: Promise<Side> =
			file.status === 'added' || file.parent === null
				? Promise.resolve({ text: '' })
				: this.#client.call('git.read', { path: file.origPath ?? file.path, base: file.parent, ...at }, opts).then(side);
		const after: Promise<Side | null> =
			file.status === 'deleted' ? Promise.resolve(null) : this.#client.call('git.read', { path: file.path, base: file.oid, ...at }, opts).then(side);
		Promise.all([before, after]).then(
			([b, a]) => {
				if (ticket !== this.#ticket) return;
				const message = 'message' in b ? b.message : a !== null && 'message' in a ? a.message : null;
				if (message !== null) this.#set({ kind: 'message', text: message });
				else this.#set({ kind: 'text', before: (b as { text: string }).text, after: a === null ? null : (a as { text: string }).text });
			},
			(e: unknown) => {
				if (ticket !== this.#ticket) return;
				this.#set({ kind: 'message', text: failureText(failure(e)) });
			},
		);
	}

	dispose(): void {
		this.#ticket++;
		this.#listeners.clear();
	}

	#set(v: CommitTextView): void {
		this.#view = v;
		for (const l of [...this.#listeners]) l();
	}
}
