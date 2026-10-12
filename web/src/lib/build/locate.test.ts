import { describe, expect, it } from 'vitest';
import type { Problems } from './build';
import { diagnosticsByFile, followProblems, locate, openProblem, type Position } from './locate';

type Rec = Problems['errors'][number];
const rec = (r: Partial<Rec>): Rec => ({ severity: 'error', message: 'm', ...r });

describe('locate (D10)', () => {
	// Decision: a root-relative path is the project's own file; its line and column come along.
	it('locates a root-relative record', () => {
		expect(locate(rec({ file: 'app/src/Main.kt', line: 3, column: 24 }))).toEqual({ path: 'app/src/Main.kt', line: 3, column: 24 });
	});

	// Decision: no line or column is kept as null (the file opens at its top, the line at its start), not invented.
	it('keeps a missing line and column as null', () => {
		expect(locate(rec({ file: 'a.kt' }))).toEqual({ path: 'a.kt', line: null, column: null });
		expect(locate(rec({ file: 'a.kt', line: 7 }))).toEqual({ path: 'a.kt', line: 7, column: null });
	});

	// Decision: an `external` record is an outside path and never a link, whatever its spelling.
	it('does not locate an external record', () => {
		expect(locate(rec({ file: '/home/x/.gradle/foo.kt', line: 1, external: true }))).toBeNull();
		expect(locate(rec({ file: 'looks/relative.kt', line: 1, external: true }))).toBeNull();
	});

	// Decision: any absolute path is not locatable (no prefix matching against the root's spellings).
	it.each(['/abs/project/A.kt', 'C:\\proj\\A.kt', 'C:/proj/A.kt', '\\\\host\\share\\A.kt', '../outside/A.kt', 'a/../../b.kt', ''])('does not locate %j', (file) => {
		expect(locate(rec({ file, line: 2 }))).toBeNull();
	});

	// Decision: a merged record is a build-intermediate path; only a relative `resolved` source is a link.
	it('locates a merged record by its relative resolved source', () => {
		const merged = { file: 'app/build/intermediates/merged_res/layout.xml', line: 4, merged: true };
		expect(locate(rec(merged))).toBeNull();
		expect(locate(rec({ ...merged, resolved: { file: 'app/src/main/res/layout/a.xml', line: 9, column: 3 } }))).toEqual({
			path: 'app/src/main/res/layout/a.xml',
			line: 9,
			column: 3,
		});
		expect(locate(rec({ ...merged, resolved: { file: 'app/src/main/res/layout/a.xml' } }))).toEqual({
			path: 'app/src/main/res/layout/a.xml',
			line: null,
			column: null,
		});
		expect(locate(rec({ ...merged, resolved: { file: '/abs/res/a.xml', line: 9 } }))).toBeNull();
	});

	// Decision: a record with no file has no position.
	it('does not locate a record without a file', () => {
		expect(locate(rec({}))).toBeNull();
	});
});

describe('openProblem', () => {
	// Decision: a click hands the position to open; a row with none opens nothing.
	it('calls open with the position of a locatable row only', () => {
		const opened: Position[] = [];
		expect(openProblem(rec({ file: 'a.kt', line: 3, column: 5 }), (p) => opened.push(p))).toBe(true);
		expect(openProblem(rec({ file: '/x/a.kt', line: 3, external: true }), (p) => opened.push(p))).toBe(false);
		expect(opened).toEqual([{ path: 'a.kt', line: 3, column: 5 }]);
	});
});

describe('diagnosticsByFile', () => {
	const problems = (errors: Rec[]): Problems => ({ errors }) as Problems;

	// Decision: the marks of a file are its locatable records with a line; an external or line-less one marks nothing.
	it('groups the locatable records with a line by file', () => {
		const map = diagnosticsByFile(
			problems([
				rec({ file: 'a.kt', line: 3, column: 2, message: 'x' }),
				rec({ file: 'a.kt', line: 5, severity: 'warning', message: 'y' }),
				rec({ file: 'b.kt', line: 1 }),
				rec({ file: 'c.kt' }),
				rec({ file: '/o/d.kt', line: 1, external: true }),
			]),
		);
		expect([...map.keys()]).toEqual(['a.kt', 'b.kt']);
		expect(map.get('a.kt')).toEqual([
			{ line: 3, col: 2, severity: 'error', message: 'x' },
			{ line: 5, col: 1, severity: 'warning', message: 'y' },
		]);
	});

	it('is empty for no result', () => {
		expect(diagnosticsByFile(null).size).toBe(0);
	});
});

describe('followProblems', () => {
	function model(first: Problems | null) {
		const m = {
			view: { problems: first },
			listeners: new Set<() => void>(),
			subscribe(l: () => void) {
				m.listeners.add(l);
				return () => m.listeners.delete(l);
			},
			emit(p: Problems | null) {
				m.view = { problems: p };
				for (const l of m.listeners) l();
			},
		};
		return m;
	}
	const one = (file: string) => ({ errors: [rec({ file, line: 1 })] }) as Problems;

	// Decision: marks are replaced by each new build result as a whole, kept while a build runs
	// (problems null), and the same result again is not a new one (the editor ignores equal arrays by identity).
	it('replaces the marks at each new result and keeps them between', () => {
		const m = model(one('a.kt'));
		const seen: string[][] = [];
		const off = followProblems(m, (marks) => seen.push([...marks.keys()]));
		expect(seen).toEqual([['a.kt']]);
		m.emit(null);
		expect(seen).toHaveLength(1);
		m.emit(one('b.kt'));
		expect(seen).toEqual([['a.kt'], ['b.kt']]);
		m.emit(m.view.problems);
		expect(seen).toHaveLength(2);
		off();
		m.emit(one('c.kt'));
		expect(seen).toHaveLength(2);
	});
});
