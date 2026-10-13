import { describe, expect, it } from 'vitest';
import { countsText, fullDate, listRows, treeRows, type FileLeafRow, type ShowFile } from './files';

const f = (path: string, over: Partial<ShowFile> = {}): ShowFile => ({ path, status: 'modified', added: 1, removed: 0, binary: false, ...over });
const leaves = (rows: ReturnType<typeof treeRows>) => rows.filter((r): r is FileLeafRow => r.kind === 'file');

describe('files', () => {
	// Decision (item 10): the tree compacts a chain of folders holding one folder and no file, as changes.ts does, folders before files.
	it('builds a tree with compacted folders, folders before files', () => {
		const rows = treeRows([f('README.md'), f('web/src/lib/a.ts'), f('web/src/lib/b.ts'), f('web/x.ts')]);
		expect(rows.map((r) => `${r.kind}:${r.depth}:${r.kind === 'dir' ? r.label : r.label}`)).toEqual([
			'dir:1:web',
			'dir:2:src/lib',
			'file:3:a.ts',
			'file:3:b.ts',
			'file:2:x.ts',
			'file:1:README.md',
		]);
	});

	// Decision: a collapsed folder hides everything under it and nothing after it; the folder row stays and says it is collapsed.
	it('hides what is under a collapsed folder only', () => {
		const files = [f('web/src/lib/a.ts'), f('web/src/lib/b.ts'), f('web/x.ts'), f('z.ts')];
		const rows = treeRows(files, new Set(['web/src/lib']));
		expect(rows.map((r) => r.key)).toEqual(['d:web', 'd:web/src/lib', 'f:web/x.ts', 'f:z.ts']);
		const lib = rows[1];
		expect(lib.kind === 'dir' && lib.collapsed).toBe(true);
		expect(treeRows(files, new Set(['web'])).map((r) => r.key)).toEqual(['d:web', 'f:z.ts']);
	});

	// Decision: the list is flat, the name first with its folder apart, ordered by name in numeric order.
	it('lists the files flat, by name in numeric order', () => {
		const rows = listRows([f('b/file10.txt'), f('a/file2.txt'), f('top.txt')]);
		expect(rows.map((r) => [r.label, r.dir, r.depth])).toEqual([
			['file2.txt', 'a', 1],
			['file10.txt', 'b', 1],
			['top.txt', '', 1],
		]);
	});

	// Decision: counts read "(+a | -r)", a binary file says "binary", and a file left uncounted by a cap shows no count at all (not 0).
	it('writes the counts, binary, and nothing when uncounted', () => {
		expect(countsText({ binary: false, added: 26, removed: 2 })).toBe('(+26 | -2)');
		expect(countsText({ binary: false, added: 0, removed: 0 })).toBe('(+0 | -0)');
		expect(countsText({ binary: true })).toBe('binary');
		expect(countsText({ binary: false })).toBe('');
		const [bin, cut] = listRows([f('i.png', { binary: true, added: undefined, removed: undefined }), f('late.txt', { added: undefined, removed: undefined })]);
		expect(bin.counts).toBe('binary');
		expect(cut.counts).toBe('');
		expect(cut.added).toBeUndefined();
		expect(cut.removed).toBeUndefined();
	});

	// Decision: a renamed file is listed under its new path and keeps where it came from, in the tree and in the list.
	it('keeps the origin of a rename', () => {
		const files = [f('new/name.kt', { status: 'renamed', origPath: 'old/name.kt', added: 0, removed: 0 }), f('plain.kt')];
		expect(leaves(treeRows(files)).find((r) => r.path === 'new/name.kt')).toMatchObject({ status: 'renamed', origPath: 'old/name.kt' });
		expect(listRows(files).find((r) => r.path === 'new/name.kt')?.origPath).toBe('old/name.kt');
		expect(listRows(files).find((r) => r.path === 'plain.kt')?.origPath).toBeUndefined();
	});

	// Decision: a commit with no file under the workspace gives no rows (the view then says so).
	it('gives no rows for no files', () => {
		expect(treeRows([])).toEqual([]);
		expect(listRows([])).toEqual([]);
	});

	// Decision (item 10): the date keeps the commit's own offset; text that is not RFC 3339 is shown as it came.
	it('writes the full date with the commit offset', () => {
		expect(fullDate('2026-10-13T14:05:09+02:00')).toBe('2026-10-13 14:05:09 +02:00');
		expect(fullDate('2026-10-13T14:05:09Z')).toBe('2026-10-13 14:05:09 UTC');
		expect(fullDate('2026-10-13T14:05:09.123-05:30')).toBe('2026-10-13 14:05:09 -05:30');
		expect(fullDate('yesterday')).toBe('yesterday');
	});
});
