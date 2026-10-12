import { describe, expect, it } from 'vitest';
import { flatRows, groupedRows, LETTERS, truncatedNote, type Entry, type FileRow } from './changes';

const e = (path: string, mark: Entry['mark'] = 'modified', origPath?: string): Entry => ({ path, mark, ...(origPath !== undefined ? { origPath } : {}) });
const shape = (rows: ReturnType<typeof groupedRows>) => rows.map((r) => `${r.depth}${r.kind === 'dir' ? ' dir ' : ' file '}${r.label}`);

describe('groupedRows', () => {
	// Decision (ADR-0067 §5; as files/tree.ts): a chain of folders holding one folder and no file is one row.
	it('compacts a chain of single-folder folders into one row', () => {
		const rows = groupedRows([e('app/src/main/A.kt'), e('app/src/main/B.kt')]);
		expect(shape(rows)).toEqual(['1 dir app/src/main', '2 file A.kt', '2 file B.kt']);
		expect(rows[0]).toMatchObject({ kind: 'dir', path: 'app/src/main' });
	});

	// Decision: a folder that also holds a file, or two folders, ends the chain; the rows nest by depth.
	it('does not compact through a folder that holds a file or two folders', () => {
		const rows = groupedRows([e('app/x.kt'), e('app/src/y.kt'), e('lib/a/b/z.kt'), e('lib/a/c/w.kt')]);
		expect(shape(rows)).toEqual(['1 dir app', '2 dir src', '3 file y.kt', '2 file x.kt', '1 dir lib/a', '2 dir b', '3 file z.kt', '2 dir c', '3 file w.kt']);
	});

	// Decision: folders before files, names in numeric order ("file2" before "file10"), as the file tree.
	it('lists folders before files and names in numeric order', () => {
		const rows = groupedRows([e('file10.kt'), e('file2.kt'), e('z/q.kt'), e('a/q.kt'), e('b10/q.kt'), e('b2/q.kt')]);
		expect(shape(rows)).toEqual(['1 dir a', '2 file q.kt', '1 dir b2', '2 file q.kt', '1 dir b10', '2 file q.kt', '1 dir z', '2 file q.kt', '1 file file2.kt', '1 file file10.kt']);
	});

	// Decision: the letters M A U D R C, and a deleted file is the one struck through.
	it('gives each mark its letter and flags only a deleted file', () => {
		expect(LETTERS).toEqual({ modified: 'M', added: 'A', untracked: 'U', deleted: 'D', renamed: 'R', conflicted: 'C' });
		const rows = groupedRows([e('a', 'modified'), e('b', 'added'), e('c', 'untracked'), e('d', 'deleted'), e('e', 'renamed', 'old/e'), e('f', 'conflicted')]) as FileRow[];
		expect(rows.map((r) => r.letter).join('')).toBe('MAUDRC');
		expect(rows.filter((r) => r.deleted).map((r) => r.path)).toEqual(['d']);
		expect(rows.find((r) => r.path === 'e')?.origPath).toBe('old/e');
		expect(rows.find((r) => r.path === 'a')?.origPath).toBeUndefined();
	});

	// Decision: a deleted file has a row (the file watch does not list it), with its own path.
	it('lists a deleted path under its folders', () => {
		const rows = groupedRows([e('src/gone.kt', 'deleted')]);
		expect(shape(rows)).toEqual(['1 dir src', '2 file gone.kt']);
	});

	// Decision: row keys are unique, so a keyed list never merges a folder and a file of one name.
	it('keys a folder and a file of the same path apart', () => {
		const keys = groupedRows([e('a/b.kt'), e('a/b/c.kt')]).map((r) => r.key);
		expect(new Set(keys).size).toBe(keys.length);
	});
});

describe('flatRows', () => {
	// Decision (ADR-0067 §5): flat, each row the name and then its directory as greyed text; by name, then directory.
	it('puts the name first with the directory as text, ordered by name then directory', () => {
		const rows = flatRows([e('b/Util.kt'), e('a/Util.kt'), e('file10.kt'), e('file2.kt'), e('z/y/Main.kt')]);
		expect(rows.map((r) => [r.label, r.dir])).toEqual([
			['file2.kt', ''],
			['file10.kt', ''],
			['Main.kt', 'z/y'],
			['Util.kt', 'a'],
			['Util.kt', 'b'],
		]);
		expect(rows.every((r) => r.depth === 1)).toBe(true);
	});

	it('marks a deleted file and a rename as the grouped rows do', () => {
		const rows = flatRows([e('d/gone.kt', 'deleted'), e('n.kt', 'renamed', 'o.kt')]);
		expect(rows.find((r) => r.label === 'gone.kt')).toMatchObject({ letter: 'D', deleted: true, dir: 'd' });
		expect(rows.find((r) => r.label === 'n.kt')).toMatchObject({ letter: 'R', origPath: 'o.kt' });
	});
});

describe('truncatedNote', () => {
	// Decision: the daemon leaves paths out past 5000; the list says so, and says nothing otherwise.
	it('says how many are listed only when the status is truncated', () => {
		expect(truncatedNote(false, 12)).toBeNull();
		expect(truncatedNote(true, 5000)).toBe('More than 5000 paths differ; only the first 5000 are listed.');
	});
});
