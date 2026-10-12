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
	// The search panel and the matches (D9): CM's own panel, in the UI's tokens (its default is a light grey).
	'.cm-panels': {
		color: 'var(--text)',
		backgroundColor: 'var(--bg-raised)',
		borderColor: 'var(--border)',
	},
	'.cm-panels-top': {
		borderBottom: '1px solid var(--border)',
	},
	'.cm-panel.cm-search': {
		fontSize: 'var(--fs-small)',
	},
	'.cm-panel.cm-search input, .cm-panel.cm-search button, .cm-panel.cm-search label': {
		fontSize: 'inherit',
		color: 'var(--text)',
	},
	'.cm-panel.cm-search input.cm-textfield': {
		backgroundColor: 'var(--bg-panel)',
		border: '1px solid var(--border)',
		borderRadius: 'var(--radius-control)',
	},
	'.cm-panel.cm-search button.cm-button': {
		backgroundColor: 'var(--bg-hover)',
		backgroundImage: 'none',
		border: '1px solid var(--border)',
		borderRadius: 'var(--radius-control)',
	},
	'.cm-panel.cm-search [name=close]': {
		color: 'var(--text-2)',
		backgroundColor: 'transparent',
	},
	'.cm-searchMatch': {
		backgroundColor: 'var(--accent-soft)',
		outline: '1px solid var(--accent)',
	},
	'.cm-searchMatch.cm-searchMatch-selected': {
		backgroundColor: 'var(--accent-soft)',
		outline: '2px solid var(--accent)',
	},
	'.cm-selectionMatch': {
		backgroundColor: 'var(--bg-active)',
	},
	'.cm-foldPlaceholder': {
		color: 'var(--text-2)',
		backgroundColor: 'var(--bg-raised)',
		border: '1px solid var(--border)',
	},
});
