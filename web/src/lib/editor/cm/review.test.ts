import { Chunk } from '@codemirror/merge';
import { highlightTree } from '@lezer/highlight';
import { syntaxTree } from '@codemirror/language';
import { EditorState } from '@codemirror/state';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { highlightStyle } from './highlight';
import { languageFor } from './languages';
import { DIFF_CONFIG, mergeConfig } from './merge';
import { buildChunks, diskLabels, hunksOf, leftMarks, mountReview, renderOf, rowClass, rowsText, updateChunks } from './review';
import { textSide } from './textside';
import { gutter } from '../../git/review';

const doc = (s: string) => EditorState.create({ doc: s }).doc;
const hunks = (a: string, b: string) => hunksOf(buildChunks(doc(a), doc(b)), doc(a), doc(b));

afterEach(() => vi.restoreAllMocks());

describe('chunks to hunks', () => {
	// Decision (ADR-0067 §4): the maps take whole-line, 0-based, half-open hunks; a chunk's `to` is one
	// past the last line's end, and past the document for a last line with no newline.
	it('converts a change in the middle, an addition and a deletion', () => {
		expect(hunks('a\nb\nc\n', 'a\nB\nc\n')).toEqual([{ fromA: 1, toA: 2, fromB: 1, toB: 2 }]);
		expect(hunks('a\nc\n', 'a\nb\nc\n')).toEqual([{ fromA: 1, toA: 1, fromB: 1, toB: 2 }]);
		expect(hunks('a\nb\nc\n', 'a\nc\n')).toEqual([{ fromA: 1, toA: 2, fromB: 1, toB: 1 }]);
	});

	// Decision: a last line with no newline is a line like another (its chunk ends outside the document).
	it('converts a change of a last line that has no newline', () => {
		expect(hunks('x\ny', 'x\nz')).toEqual([{ fromA: 1, toA: 2, fromB: 1, toB: 2 }]);
		// The chunks are the merge view's: a last line that gains a newline is changed, as in Classic.
		expect(hunks('x', 'x\nz')).toEqual([{ fromA: 0, toA: 1, fromB: 0, toB: 2 }]);
		expect(hunks('x\ny', 'x')).toEqual([{ fromA: 0, toA: 2, fromB: 0, toB: 1 }]);
		expect(hunks('x\ny\n', 'x\n')).toEqual([{ fromA: 1, toA: 2, fromB: 1, toB: 1 }]);
		expect(hunks('x\n', 'x\ny')).toEqual([{ fromA: 1, toA: 2, fromB: 1, toB: 2 }]);
	});

	it('has no hunk for equal documents and one over a whole new file', () => {
		expect(hunks('a\n', 'a\n')).toEqual([]);
		// An empty base holds no line: the new file is added whole, nothing of HEAD is replaced.
		expect(hunks('', 'x\ny')).toEqual([{ fromA: 0, toA: 0, fromB: 0, toB: 2 }]);
	});
});

describe('the right pane', () => {
	const A = 'one\ntwo\nthree\nfour\nfive\n';
	const B = 'one\nTWO\nthree\nfive\nsix\n';

	// Decision: the right document is the rows' text, one line per row (red rows from the base, the rest from the buffer).
	it('has a document equal to its rows, a line per row', () => {
		const a = EditorState.create({ doc: A });
		const b = EditorState.create({ doc: B });
		const r = renderOf(a, b, hunksOf(buildChunks(a.doc, b.doc), a.doc, b.doc));
		expect(r.text).toBe(rowsText(r.rows, a.doc, b.doc));
		expect(r.text.split('\n')).toHaveLength(r.rows.length);
		expect(r.text.split('\n')).toEqual(['one', 'two', 'TWO', 'three', 'four', 'five', 'six', '']);
		expect(r.rows.map(rowClass)).toEqual([
			'cm-review-same',
			'cm-review-del',
			'cm-review-add',
			'cm-review-same',
			'cm-review-del',
			'cm-review-same',
			'cm-review-add',
			'cm-review-same',
		]);
	});

	// Decision: unchanged rows are dim, added green, deleted red; the class is by the row's kind.
	it('classes each kind of row differently', () => {
		const classes = new Set([rowClass({ kind: 'same', a: 0, b: 0 }), rowClass({ kind: 'add', b: 0 }), rowClass({ kind: 'del', a: 0, hunk: 0 })]);
		expect(classes.size).toBe(3);
	});

	// Decision: the disk's line is shown beside each row and blank on a red one.
	it('numbers the rows by disk line, blank on red rows', () => {
		const a = EditorState.create({ doc: A });
		const b = EditorState.create({ doc: B });
		const r = renderOf(a, b, hunksOf(buildChunks(a.doc, b.doc), a.doc, b.doc));
		expect(diskLabels(r.rows)).toEqual(['1', '', '2', '3', '', '4', '5', '6']);
	});

	// Decision: each side is highlighted from its own parse, so a red row (a line the buffer no longer has)
	// takes its colours from the base's tree.
	it('highlights a red row from the base’s tree', async () => {
		const lang = await languageFor('x.json')!.load();
		const a = EditorState.create({ doc: '{\n"keep": 1,\n"gone": "text"\n}', extensions: [lang] });
		const b = EditorState.create({ doc: '{\n"keep": 1\n}', extensions: [lang] });
		const r = renderOf(a, b, hunksOf(buildChunks(a.doc, b.doc), a.doc, b.doc));
		const redRow = r.rows.findIndex((x) => x.kind === 'del');
		expect(redRow).toBeGreaterThanOrEqual(0);
		const lineStart = r.text.split('\n').slice(0, redRow).join('\n').length + (redRow > 0 ? 1 : 0);
		const lineEnd = lineStart + r.text.split('\n')[redRow]!.length;
		const got: string[] = [];
		r.deco.between(lineStart, lineEnd, (from, to, v) => {
			const c = (v.spec as { class?: string }).class;
			if (c !== undefined && from >= lineStart && to <= lineEnd && to > from && !c.startsWith('cm-review')) got.push(`${from - lineStart}-${to - lineStart}:${c}`);
		});
		// What the base's own parse says about that line.
		const line = a.doc.line(r.rows[redRow]!.kind === 'del' ? (r.rows[redRow] as { a: number }).a + 1 : 1);
		const want: string[] = [];
		highlightTree(syntaxTree(a), highlightStyle, (f, t, c) => {
			const ff = Math.max(f, line.from);
			const tt = Math.min(t, line.to);
			if (ff < tt) want.push(`${ff - line.from}-${tt - line.from}:${c}`);
		}, line.from, line.to);
		expect(want.length).toBeGreaterThan(0);
		expect(got).toEqual(want);
	});

	// Decision: an edit of the buffer changes the rows, and the updated chunks give what a fresh build gives.
	it('changes its rows when the buffer is edited', () => {
		const a = EditorState.create({ doc: A });
		let b = EditorState.create({ doc: A });
		let chunks = buildChunks(a.doc, b.doc);
		const before = renderOf(a, b, hunksOf(chunks, a.doc, b.doc));
		expect(before.rows.every((r) => r.kind === 'same')).toBe(true);
		const tr = b.update({ changes: { from: 4, to: 7, insert: 'TWO' } });
		b = tr.state;
		chunks = updateChunks(chunks, a.doc, b.doc, tr.changes.desc);
		const after = renderOf(a, b, hunksOf(chunks, a.doc, b.doc));
		expect(after.text).not.toBe(before.text);
		expect(after.rows.map((r) => r.kind)).toEqual(['same', 'del', 'add', 'same', 'same', 'same', 'same']);
		const fresh = renderOf(a, b, hunksOf(buildChunks(a.doc, b.doc), a.doc, b.doc));
		expect(after.text).toBe(fresh.text);
	});

	// Decision: a deleted file has no buffer: every line of HEAD is a red row, and a final newline is no extra line.
	it('renders a file with no buffer as red rows only', () => {
		const a = EditorState.create({ doc: 'x\ny\n' });
		const r = renderOf(a, null, [{ fromA: 0, toA: 2, fromB: 0, toB: 0 }], 2);
		expect(r.rows.map((x) => x.kind)).toEqual(['del', 'del']);
		expect(r.text).toBe('x\ny');
	});
});

describe('the left pane’s gutter marks', () => {
	// Decision: added and modified lines are marked on their own line; lines deleted are marked on the
	// edge of the line they were before, or the last line's bottom for a deletion at the end.
	it('marks added, modified and deleted places', () => {
		const b = doc('a\nB\nc\nd');
		const marks = leftMarks(
			gutter([
				{ fromA: 1, toA: 2, fromB: 1, toB: 2 },
				{ fromA: 3, toA: 4, fromB: 2, toB: 2 },
				{ fromA: 5, toA: 7, fromB: 4, toB: 4 },
			]),
			b,
		);
		const got: string[] = [];
		marks.between(0, b.length, (from, _to, m) => {
			got.push(`${b.lineAt(from).number}:${(m as unknown as { cls: string }).cls}`);
		});
		expect(got).toEqual(['2:cm-review-modified', '3:cm-review-del-before', '4:cm-review-del-after']);
	});
});

describe('one diff configuration', () => {
	// Decision (ADR-0067 §1): the merge view and Review's chunks take the same DIFF_CONFIG object.
	it('is the object both modes are given', () => {
		const build = vi.spyOn(Chunk, 'build');
		const updateB = vi.spyOn(Chunk, 'updateB');
		const a = doc('a\n');
		const chunks = buildChunks(a, doc('b\n'));
		updateChunks(chunks, a, doc('c\n'), EditorState.create({ doc: 'b\n' }).update({ changes: { from: 0, to: 1, insert: 'c' } }).changes.desc);
		expect(build.mock.calls[0]![2]).toBe(DIFF_CONFIG);
		expect(updateB.mock.calls[0]![4]).toBe(DIFF_CONFIG);
		expect(mergeConfig({}, {}, {} as ShadowRoot).diffConfig).toBe(DIFF_CONFIG);
	});
});

describe('a commit’s file in Review (plan m2n step 9; ADR-0070 §4)', () => {
	const PARENT = 'a\nb\nc\n';
	const COMMIT = 'a\nB\nc\nd\n';
	const left = () => EditorState.create(textSide(COMMIT, 'File contents'));

	// Decision: the left pane's document is the commit's text and nothing else, and the rows are the diff against the parent, as with a buffer.
	it('shows the commit’s text on the left and the change against the first parent in the rows', () => {
		const a = EditorState.create({ doc: PARENT });
		const b = left();
		expect(b.doc.toString()).toBe(COMMIT);
		const r = renderOf(a, b, hunksOf(buildChunks(a.doc, b.doc), a.doc, b.doc));
		expect(r.rows.map((x) => x.kind)).toEqual(['same', 'del', 'add', 'same', 'add', 'same']);
		expect(r.text).toBe('a\nb\nB\nc\nd\n');
		expect(r.lines).toBe(b.doc.lines);
	});

	// Decision: an added file has an empty base: every line of the commit is a green row.
	it('takes an added file against an empty base as green rows only', () => {
		const a = EditorState.create({ doc: '' });
		const b = EditorState.create(textSide('x\ny\n', 'File contents'));
		const r = renderOf(a, b, hunksOf(buildChunks(a.doc, b.doc), a.doc, b.doc));
		expect(r.rows.filter((x) => x.kind === 'del')).toHaveLength(0);
		expect(r.rows.filter((x) => x.kind === 'add').length).toBeGreaterThan(0);
	});

	// Decision: a commit's text in the buffer's place never edits: an attempt on the left leaves the document, so the rows and marks stay the commit's.
	it('refuses an edit on the left, so the rows do not move', () => {
		const a = EditorState.create({ doc: PARENT });
		const b = left();
		const edited = b.update({ changes: { from: 0, to: b.doc.length, insert: PARENT } }).state;
		expect(edited.doc.toString()).toBe(COMMIT);
		expect(renderOf(a, edited, hunksOf(buildChunks(a.doc, edited.doc), a.doc, edited.doc)).text).toBe('a\nb\nB\nc\nd\n');
	});

	// Decision: a buffer and a text are two answers to one question; giving both is a bug in the caller, caught at the mount before anything is touched.
	it('is not given a buffer and a text at once', () => {
		const buffer = {} as never;
		expect(() => mountReview({} as HTMLElement, { buffer, text: { text: 'x', path: 'a.kt' }, base: 'y', onSave: () => {} })).toThrow(/takes the buffer’s place/);
	});
});
