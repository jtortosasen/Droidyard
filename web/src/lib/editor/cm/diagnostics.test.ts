import { describe, expect, it } from 'vitest';
import { forEachDiagnostic } from '@codemirror/lint';
import { EditorState } from '@codemirror/state';
import { CmBuffer } from './buffer';
import { offsetOf, toLint } from './diagnostics';

const doc = (t: string) => EditorState.create({ doc: t }).doc;
const d = (line: number, col: number, extra = {}) => ({ line, col, severity: 'error' as const, message: 'm', ...extra });

/** The diagnostics a state holds, at their current positions. */
function held(b: CmBuffer): { from: number; to: number; message: string }[] {
	const out: { from: number; to: number; message: string }[] = [];
	forEachDiagnostic(b.state, (x, from, to) => out.push({ from, to, message: x.message }));
	return out;
}

describe('offsetOf (D10)', () => {
	const t = doc('abc\nde\n\nfghij');
	// Decision: 1-based line and column; no column is the line's start (the caller passes 1); past the end is clamped, never thrown.
	it('maps 1-based positions and clamps past the end', () => {
		expect(offsetOf(t, 1, 1)).toBe(0);
		expect(offsetOf(t, 2, 2)).toBe(5);
		expect(offsetOf(t, 2, 99)).toBe(6);
		expect(offsetOf(t, 3, 5)).toBe(7);
		expect(offsetOf(t, 99, 99)).toBe(t.length);
		expect(offsetOf(t, 0, 0)).toBe(0);
		expect(offsetOf(t, -3, -3)).toBe(0);
	});
});

describe('toLint', () => {
	const t = doc('abc\nde');
	// Decision: a position without an end marks one character, kept on its line.
	it('marks one character, on the line', () => {
		expect(toLint(t, [d(1, 2)])).toEqual([{ from: 1, to: 2, severity: 'error', message: 'm' }]);
		expect(toLint(t, [d(1, 4)])[0]).toMatchObject({ from: 3, to: 3 });
		expect(toLint(t, [d(2, 99)])[0]).toMatchObject({ from: 6, to: 6 });
	});
	it('honours an end, clamped', () => {
		expect(toLint(t, [d(1, 1, { endLine: 2, endCol: 2 })])[0]).toMatchObject({ from: 0, to: 5 });
		expect(toLint(t, [d(1, 2, { endCol: 99 })])[0]).toMatchObject({ from: 1, to: 3 });
	});
});

describe('CmBuffer diagnostics (D10)', () => {
	const make = () => new CmBuffer({ text: 'one\ntwo\nthree\n', readOnly: false });

	// Decision: the marks live in the buffer's state, so edits move them (they follow the text, not the line number).
	it('moves with edits made after they were set', () => {
		const b = make();
		b.setDiagnostics([d(2, 1, { message: 'at two' })]);
		expect(held(b)).toEqual([{ from: 4, to: 5, message: 'at two' }]);
		b.dispatch({ changes: { from: 0, insert: 'zero\n' } });
		expect(held(b)).toEqual([{ from: 9, to: 10, message: 'at two' }]);
	});

	// Decision: a new build result replaces the old marks as a whole; an empty one clears them.
	it('replaces and clears', () => {
		const b = make();
		b.setDiagnostics([d(1, 1, { message: 'a' })]);
		b.setDiagnostics([d(3, 1, { message: 'b' })]);
		expect(held(b).map((x) => x.message)).toEqual(['b']);
		b.setDiagnostics([]);
		expect(held(b)).toEqual([]);
	});

	// Decision: the same list again (a tab reopened later) must not redraw at the build's positions over edited text.
	it('does not reapply the same list', () => {
		const b = make();
		const list = [d(2, 1)];
		b.setDiagnostics(list);
		b.dispatch({ changes: { from: 0, insert: 'zero\n' } });
		b.setDiagnostics(list);
		expect(held(b)).toEqual([{ from: 9, to: 10, message: 'm' }]);
	});

	// Decision: a message is stored as the text it is (the tooltip sets textContent), never as markup to render.
	it('keeps a message verbatim', () => {
		const b = make();
		b.setDiagnostics([d(1, 1, { message: '<img src=x onerror=alert(1)>' })]);
		expect(held(b)[0]!.message).toBe('<img src=x onerror=alert(1)>');
	});
});
