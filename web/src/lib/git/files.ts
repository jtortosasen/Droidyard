// The changed files of an expanded commit (plan m2n step 8; item 10), pure over the files
// `git.show` returns: a folder tree (folders compacted and ordered as `changes.ts` does, so the
// two never differ) or a flat list, each file with its counts.
//   - "(+a | -r)" when git counted lines; "binary" for a binary file; nothing when the daemon
//     left the counts out (the files listed after git's output was cut), so they are shown
//     without counts rather than as 0.
//   - A renamed file keeps `origPath`, shown beside it.
import type { Result } from '../api/capabilities.gen';
import { flatRows, groupedRows, type Entry, type Mark } from './changes';

export type Show = Result<'git.show'>;
export type ShowFile = Show['files'][number];

export interface FileDirRow {
	kind: 'dir';
	key: string;
	/** The folder (the end of a compacted chain). */
	path: string;
	label: string;
	depth: number;
	collapsed: boolean;
}

export interface FileLeafRow {
	kind: 'file';
	key: string;
	path: string;
	label: string;
	/** The file's directory as greyed text (the list only). */
	dir: string;
	depth: number;
	status: ShowFile['status'];
	origPath?: string;
	binary: boolean;
	added?: number;
	removed?: number;
	/** "(+a | -r)", "binary", or "" when uncounted. */
	counts: string;
}

export type FileRow = FileDirRow | FileLeafRow;

const MARKS: Readonly<Record<ShowFile['status'], Mark>> = {
	added: 'added',
	modified: 'modified',
	deleted: 'deleted',
	renamed: 'renamed',
};

export function countsText(f: Pick<ShowFile, 'binary' | 'added' | 'removed'>): string {
	if (f.binary) return 'binary';
	if (f.added === undefined || f.removed === undefined) return '';
	return `(+${f.added} | -${f.removed})`;
}

function leaf(f: ShowFile, base: { label: string; dir: string; depth: number; key: string }): FileLeafRow {
	return {
		kind: 'file',
		key: base.key,
		path: f.path,
		label: base.label,
		dir: base.dir,
		depth: base.depth,
		status: f.status,
		...(f.origPath !== undefined ? { origPath: f.origPath } : {}),
		binary: f.binary,
		...(f.added !== undefined ? { added: f.added } : {}),
		...(f.removed !== undefined ? { removed: f.removed } : {}),
		counts: countsText(f),
	};
}

const asEntries = (files: readonly ShowFile[]): Entry[] => files.map((f) => ({ path: f.path, mark: MARKS[f.status] }));

/** The files as a tree; the folders in `collapsed` (by their `path`) hide what is under them. */
export function treeRows(files: readonly ShowFile[], collapsed: ReadonlySet<string> = new Set()): FileRow[] {
	const byPath = new Map(files.map((f) => [f.path, f]));
	const out: FileRow[] = [];
	let hideBelow = -1; // depth of the collapsed folder whose children are skipped
	for (const r of groupedRows(asEntries(files))) {
		if (hideBelow >= 0) {
			if (r.depth > hideBelow) continue;
			hideBelow = -1;
		}
		if (r.kind === 'dir') {
			const isCollapsed = collapsed.has(r.path);
			out.push({ kind: 'dir', key: r.key, path: r.path, label: r.label, depth: r.depth, collapsed: isCollapsed });
			if (isCollapsed) hideBelow = r.depth;
		} else {
			out.push(leaf(byPath.get(r.path)!, { label: r.label, dir: '', depth: r.depth, key: r.key }));
		}
	}
	return out;
}

/** The files as one list: the name, then the directory. */
export function listRows(files: readonly ShowFile[]): FileLeafRow[] {
	const byPath = new Map(files.map((f) => [f.path, f]));
	return flatRows(asEntries(files)).map((r) => leaf(byPath.get(r.path)!, { label: r.label, dir: r.dir, depth: 1, key: r.key }));
}

const RFC3339 = /^(\d{4}-\d\d-\d\d)T(\d\d:\d\d:\d\d)(?:\.\d+)?(Z|[+-]\d\d:\d\d)$/;

/** An RFC 3339 time as "2026-10-13 14:05:09 +02:00": the commit's own offset, kept. Else the text as it is. */
export function fullDate(time: string): string {
	const m = RFC3339.exec(time);
	return m ? `${m[1]} ${m[2]} ${m[3] === 'Z' ? 'UTC' : m[3]}` : time;
}
