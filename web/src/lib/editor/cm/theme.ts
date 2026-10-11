// The editor's chrome (plan m2i D2, D12). Every value is a CSS custom property of
// ui/tokens.css, which inherits into the shadow tree the editor is mounted in, so the
// dark and light schemes switch with no code here. The theme writes rules for classes
// only (D2; CM's own inline styles go through CSSOM, which the CSP allows, so the CSP
// would not catch a slip here: review does), and this file holds no colour literal.
import { EditorView } from '@codemirror/view';

export const theme = EditorView.theme({
	'&': {
		flex: '1 1 0',
		minHeight: '0',
		minWidth: '0',
		color: 'var(--text)',
		backgroundColor: 'var(--bg-panel)',
		fontSize: 'var(--fs-mono)',
	},
	'&.cm-focused': {
		outline: '1px solid var(--accent)',
		outlineOffset: '-1px',
	},
	'.cm-scroller': {
		fontFamily: 'var(--font-mono)',
		lineHeight: '1.5',
	},
	'.cm-content': {
		caretColor: 'var(--text)',
		padding: '0',
	},
	'.cm-line': {
		padding: '0 var(--sp-5) 0 var(--sp-4)',
	},
	'.cm-cursor, .cm-dropCursor': {
		borderLeftColor: 'var(--text)',
	},
	'.cm-selectionBackground': {
		background: 'var(--accent-soft)',
	},
	'&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground': {
		background: 'var(--accent-soft)',
	},
	'.cm-activeLine': {
		backgroundColor: 'var(--bg-hover)',
	},
	'.cm-gutters': {
		color: 'var(--text-3)',
		backgroundColor: 'var(--bg-panel)',
		border: 'none',
		borderRight: '1px solid var(--border)',
	},
	'.cm-lineNumbers .cm-gutterElement': {
		padding: '0 var(--sp-4) 0 var(--sp-5)',
	},
	'.cm-activeLineGutter': {
		color: 'var(--text-2)',
		backgroundColor: 'var(--bg-hover)',
	},
	'.cm-foldGutter .cm-gutterElement': {
		color: 'var(--text-3)',
		cursor: 'pointer',
	},
	'.cm-foldPlaceholder': {
		color: 'var(--text-2)',
		backgroundColor: 'var(--bg-raised)',
		border: '1px solid var(--border)',
	},
});
