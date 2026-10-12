// The Git tree's Changes rows (plan m2l step 5; ADR-0067 §5), pure over the status `git.watch`
// carries: the paths that differ from HEAD, sorted by path, at most 5000.
//
//   - Grouped: folders, with a chain of folders that holds one folder and no file shown as one
//     row ("src/main/java", as files/tree.ts compacts); folders before files; names in numeric
//     order ("file2" before "file10").
//   - Flat: one row per file, the name first, then its directory as greyed text; by name
//     (numeric), then by directory.
//   - Letters M A U D R C; a deleted file is struck through (`deleted`).
import type { Result } from '../api/capabilities.gen';

export type Entry = Result<'git.watch'>['entries'][number];
export type Mark = Entry['mark'];
export type Letter = 'M' | 'A' | 'U' | 'D' | 'R' | 'C';

export const LETTERS: Readonly<Record<Mark, Letter>> = {
	modified: 'M',
	added: 'A',
	untracked: 'U',
	deleted: 'D',
	renamed: 'R',
	conflicted: 'C',
};

/** The mark in words, for a tooltip and a screen reader. */
export const MARK_WORDS: Readonly<Record<Mark, string>> = {
	modified: 'modified',
	added: 'added',
	untracked: 'untracked',
	deleted: 'deleted',
	renamed: 'renamed',
	conflicted: 'conflicted',
};

export interface DirRow {
	kind: 'dir';
	key: string;
	/** The folder (the end of a compacted chain), relative to the root. */
	path: string;
	label: string;
	/** 1 for the root's children. */
	depth: number;
}

export interface FileRow {
	kind: 'file';
	key: string;
	path: string;
	/** The file's name. */
	label: string;
	/** The file's directory as greyed text; empty in the root, and always empty in the grouped rows. */
	dir: string;
	depth: number;
	mark: Mark;
	letter: Letter;
	deleted: boolean;
	/** For a renamed path: where it was in HEAD. */
	origPath?: string;
}

export type ChangeRow = DirRow | FileRow;

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });
const order = (a: string, b: string): number => collator.compare(a, b) || (a < b ? -1 : a > b ? 1 : 0);

const nameOf = (path: string): string => path.slice(path.lastIndexOf('/') + 1);
const dirOf = (path: string): string => (path.includes('/') ? path.slice(0, path.lastIndexOf('/')) : '');

function fileRow(e: Entry, depth: number, dir: string): FileRow {
	return {
		kind: 'file',
		key: `f:${e.path}`,
		path: e.path,
		label: nameOf(e.path),
		dir,
		depth,
		mark: e.mark,
		letter: LETTERS[e.mark],
		deleted: e.mark === 'deleted',
		...(e.origPath !== undefined ? { origPath: e.origPath } : {}),
	};
}

interface Node {
	dirs: Map<string, Node>;
	files: Entry[];
}

/** The rows of the changes grouped by folder, every folder open. */
export function groupedRows(entries: readonly Entry[]): ChangeRow[] {
	const root: Node = { dirs: new Map(), files: [] };
	for (const e of entries) {
		let at = root;
		const parts = e.path.split('/');
		for (const part of parts.slice(0, -1)) {
			let next = at.dirs.get(part);
			if (next === undefined) {
				next = { dirs: new Map(), files: [] };
				at.dirs.set(part, next);
			}
			at = next;
		}
		at.files.push(e);
	}
	const out: ChangeRow[] = [];
	const visit = (node: Node, base: string, depth: number) => {
		for (const name of [...node.dirs.keys()].sort(order)) {
			let end = node.dirs.get(name)!;
			let label = name;
			let path = base === '' ? name : `${base}/${name}`;
			// A chain of folders holding one folder and no file is one row.
			while (end.files.length === 0 && end.dirs.size === 1) {
				const [[next, child]] = [...end.dirs];
				label += `/${next}`;
				path += `/${next}`;
				end = child!;
			}
			out.push({ kind: 'dir', key: `d:${path}`, path, label, depth });
			visit(end, path, depth + 1);
		}
		for (const e of [...node.files].sort((a, b) => order(nameOf(a.path), nameOf(b.path)))) out.push(fileRow(e, depth, ''));
	};
	visit(root, '', 1);
	return out;
}

/** The rows of the changes as one list: the name, then the directory as greyed text. */
export function flatRows(entries: readonly Entry[]): FileRow[] {
	return entries
		.map((e) => fileRow(e, 1, dirOf(e.path)))
		.sort((a, b) => order(a.label, b.label) || order(a.dir, b.dir));
}

/** The note under a truncated list, or null: the daemon left paths out. */
export function truncatedNote(truncated: boolean, listed: number): string | null {
	return truncated ? `More than ${listed} paths differ; only the first ${listed} are listed.` : null;
}
