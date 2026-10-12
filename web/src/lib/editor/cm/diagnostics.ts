// The last build's errors in a file (plan m2i D10), as @codemirror/lint diagnostics. They are
// part of the buffer's state (the lint state field), so they follow the buffer: positions map
// through edits, survive a tab switch, and are replaced when a new build result arrives. There
// is no lint source (nothing runs while typing; live diagnostics are v1), and a message is
// text only: the default renderer sets textContent, `renderMessage` is never given.
import { lintGutter, setDiagnostics, type Diagnostic } from '@codemirror/lint';
import type { EditorState, Extension, Text, TransactionSpec } from '@codemirror/state';
import { EditorView } from '@codemirror/view';
import type { EditorDiagnostic } from '../surface';

function clamp(n: number, lo: number, hi: number): number {
	return Math.min(hi, Math.max(lo, n));
}

/** Document offset of a 1-based line and column, clamped to the text (a past-the-end line or column lands on the last line or the line's end). */
export function offsetOf(doc: Text, line: number, col: number): number {
	const l = doc.line(clamp(Math.trunc(line), 1, doc.lines));
	return l.from + clamp(Math.trunc(col) - 1, 0, l.length);
}

/**
 * The range to underline: up to the given end when there is one, else one character from the
 * column (the compiler names a position, not a span); at a line's end, or on an empty line, a
 * zero-width mark that lint still shows in the gutter and as a point.
 */
export function toLint(doc: Text, list: readonly EditorDiagnostic[]): Diagnostic[] {
	return list.map((d) => {
		const from = offsetOf(doc, d.line, d.col);
		let to = from + 1;
		if (d.endLine !== undefined) to = offsetOf(doc, d.endLine, d.endCol ?? Number.MAX_SAFE_INTEGER);
		else if (d.endCol !== undefined) to = offsetOf(doc, d.line, d.endCol);
		// Never backwards, and one-character marks stay on their line.
		to = d.endLine !== undefined ? clamp(to, from, doc.length) : clamp(to, from, doc.lineAt(from).to);
		return { from, to, severity: d.severity, message: d.message };
	});
}

/** The transaction that replaces the state's diagnostics (and installs the lint state when it is not there yet). */
export function diagnosticsSpec(state: EditorState, list: readonly EditorDiagnostic[]): TransactionSpec {
	return setDiagnostics(state, toLint(state.doc, list));
}

/** The gutter markers, in the editor's tokens; every colour is a custom property of ui/tokens.css. */
export const diagnosticsExtension: Extension = [
	lintGutter(),
	EditorView.theme({
		// lint's own styles draw with data: SVG images and colour literals; these replace them.
		'.cm-lintRange': { backgroundImage: 'none', paddingBottom: '0' },
		'.cm-lintRange-error': { textDecoration: 'underline wavy var(--danger)' },
		'.cm-lintRange-warning': { textDecoration: 'underline wavy var(--warning)' },
		'.cm-lintRange-info': { textDecoration: 'underline wavy var(--text-3)' },
		'.cm-lintRange-active': { backgroundColor: 'var(--bg-hover)' },
		'.cm-lintPoint:after': { borderBottomColor: 'var(--danger)' },
		'.cm-lintPoint-warning:after': { borderBottomColor: 'var(--warning)' },
		'.cm-lintPoint-info:after': { borderBottomColor: 'var(--text-3)' },
		'.cm-lint-marker': { content: 'none', borderRadius: '50%', width: '0.6em', height: '0.6em', margin: '0.3em 0.2em' },
		'.cm-lint-marker-error': { backgroundColor: 'var(--danger)' },
		'.cm-lint-marker-warning': { backgroundColor: 'var(--warning)' },
		'.cm-lint-marker-info': { backgroundColor: 'var(--text-3)' },
		'.cm-tooltip': {
			color: 'var(--text)',
			backgroundColor: 'var(--bg-raised)',
			border: '1px solid var(--border)',
			borderRadius: 'var(--radius-control)',
			fontSize: 'var(--fs-small)',
		},
		'.cm-diagnostic-error': { borderLeft: '5px solid var(--danger)' },
		'.cm-diagnostic-warning': { borderLeft: '5px solid var(--warning)' },
		'.cm-diagnostic-info': { borderLeft: '5px solid var(--text-3)' },
	}),
];
