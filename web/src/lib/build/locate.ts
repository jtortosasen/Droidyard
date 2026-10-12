// Where a build error is in the project (plan m2i D10). A record is locatable when its file
// is a path relative to the root that is neither `external` nor `merged`, or when it is
// `merged` and the daemon recovered a relative `resolved` source. Any absolute path is not
// locatable: no prefix matching against the root's spellings is tried, so a row either
// opens the project's own file or has no link. The last build's records are the one source
// (BuildModel.view.problems); the diagnostics of a file are derived from them here.
import type { EditorDiagnostic } from '../editor/surface';
import type { Problems } from './build';

type ErrorRecord = Problems['errors'][number];

export interface Position {
	/** Root-relative path, as the tree and the tabs name a file. */
	path: string;
	/** 1-based; null when the record carries none (the file opens at its top). */
	line: number | null;
	/** 1-based; null when the record carries none (the line's start). */
	column: number | null;
}

/** An absolute path in any spelling the daemon passes through: POSIX, a Windows drive, a UNC path. */
function absolute(p: string): boolean {
	return p.startsWith('/') || p.startsWith('\\') || /^[A-Za-z]:/.test(p);
}

/** A relative path that stays inside the root: not empty, no `..` segment. */
function inside(p: string): boolean {
	return p !== '' && !absolute(p) && !p.split(/[\\/]/).includes('..');
}

const num = (n: number | undefined): number | null => (typeof n === 'number' && Number.isInteger(n) && n > 0 ? n : null);

export function locate(e: ErrorRecord): Position | null {
	if (e.merged) {
		const r = e.resolved;
		if (r?.file === undefined || !inside(r.file)) return null;
		return { path: r.file, line: num(r.line), column: num(r.column) };
	}
	if (e.external || e.file === undefined || !inside(e.file)) return null;
	return { path: e.file, line: num(e.line), column: num(e.column) };
}

/** A click on a row: hands its position to `open`; false (and nothing opened) for a row the project does not hold. */
export function openProblem(e: ErrorRecord, open: (at: Position) => void): boolean {
	const at = locate(e);
	if (at === null) return false;
	open(at);
	return true;
}

/**
 * The diagnostics of every file the records locate, by path. A record without a line is not
 * marked (a mark on line 1 would say something the compiler did not). Each array is made
 * once per call, so its identity says "the same build result" to the editor.
 */
export function diagnosticsByFile(problems: Problems | null): Map<string, EditorDiagnostic[]> {
	const out = new Map<string, EditorDiagnostic[]>();
	for (const e of problems?.errors ?? []) {
		const at = locate(e);
		if (at === null || at.line === null) continue;
		const list = out.get(at.path) ?? [];
		list.push({
			line: at.line,
			col: at.column ?? 1,
			severity: e.severity === 'warning' ? 'warning' : 'error',
			message: e.message ?? '',
		});
		out.set(at.path, list);
	}
	return out;
}

/**
 * The marks follow the build model's result: a new result replaces them as a whole, and the
 * moment a build starts (the model drops its problems) they stay, so the file does not flicker
 * between two results. Calls `set` with the current marks at once and at every new result;
 * returns the unsubscribe.
 */
export function followProblems(
	model: { readonly view: { readonly problems: Problems | null }; subscribe(l: () => void): () => void },
	set: (marks: ReadonlyMap<string, EditorDiagnostic[]>) => void,
): () => void {
	let last = model.view.problems;
	set(diagnosticsByFile(last));
	return model.subscribe(() => {
		const p = model.view.problems;
		if (p === null || p === last) return;
		last = p;
		set(diagnosticsByFile(p));
	});
}
