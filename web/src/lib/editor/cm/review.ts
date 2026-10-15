// Review mode (ADR-0067 §4; plan m2l step 7): the buffer, editable, on the left; HEAD's text as
// unified rows on the right, read-only; the panes held level by an elastic scroll. A commit's file
// (ADR-0070 §4; plan m2n step 9) puts that commit's text, read-only, in the buffer's place and its
// first parent's on the right: no buffer is touched, and nothing is mirrored or followed.
//
//   - The maps (rows, the elastic map, the anchor, the driver) are `git/review.ts`, pure. This file
//     is the glue: it measures both panes with `lineBlockAt`, sets their scroll and padding, and
//     builds the right pane's document, classes and highlighting.
//   - The left is the buffer as Compare's editable side is: `bufferSide`, `mirror`, and `follow`
//     over `buffer.onChange`. Nothing is inserted into its document: no spacer, no widget, so it
//     holds exactly the file's lines. Gutter marks show added and modified lines and deletions.
//   - The right document is the rows' text, one line per row. Unchanged rows are dimmed, added ones
//     green, deleted ones red. Each side is highlighted from its own parse (`highlightTree`): the
//     buffer's tree for same and added rows, HEAD's for red ones. Its gutter is the disk line,
//     blank on red rows. Clicking a row reveals its line on the left.
//   - The chunks come from `Chunk.build` at mount and `Chunk.updateB` on the edits made since the
//     last frame, composed into one, with `DIFF_CONFIG`, the object the merge view takes too.
//   - Padding that makes any row reachable is the content's padding, set through CSSOM (never a
//     style attribute); the colours are tokens (ui/tokens.css), in the theme below.
import { Chunk } from '@codemirror/merge';
import { highlightTree } from '@lezer/highlight';
import { ensureSyntaxTree, syntaxHighlighting, syntaxTree, language as languageFacet } from '@codemirror/language';
import {
	ChangeSet,
	EditorState,
	RangeSet,
	StateEffect,
	StateField,
	Text,
	type ChangeDesc,
	type EditorStateConfig,
	type Extension,
	type Range,
} from '@codemirror/state';
import { Decoration, EditorView, GutterMarker, gutter as cmGutter, type DecorationSet } from '@codemirror/view';
import {
	Driver,
	ElasticMap,
	alignLeft,
	alignRight,
	anchorOf,
	gutter as gutterOf,
	lineOfRow,
	rowOfLine,
	rows as rowsOf,
	type Anchor,
	type Gutter,
	type LineHunk,
	type Row,
	type Side,
	type Span,
} from '../../git/review';
import type { EditorDiagnostic, EditorHandle, ReviewOptions } from '../surface';
import { CmBuffer } from './buffer';
import { minimalChange } from './change';
import { highlightStyle } from './highlight';
import { DIFF_CONFIG, SIDES_CSS, bufferSide, follow, followed, mergeTheme, mirror, mix } from './merge';
import { languageFor } from './languages';
import { textSide, useLanguage } from './textside';
import { isSaveKey } from './savekey';
import { theme } from './theme';

// ---- the pure part: documents in, rows, text, classes and marks out ----------------------------

/** The chunks of two documents, with the one diff configuration. */
export function buildChunks(a: Text, b: Text): readonly Chunk[] {
	return Chunk.build(a, b, DIFF_CONFIG);
}

/** The chunks after an edit of the buffer's document (`b` is the new one), with the one diff configuration. */
export function updateChunks(chunks: readonly Chunk[], a: Text, b: Text, changes: ChangeDesc): readonly Chunk[] {
	return Chunk.updateB(chunks, a, b, changes, DIFF_CONFIG);
}

/**
 * A chunk's range as lines. A chunk covers whole lines and its `to` is one past the last line's
 * end (outside the document for a last line with no newline); an empty range sits before the line
 * it starts at, or after the last (the document's line count) when it starts past the end.
 */
function lineRange(doc: Text, from: number, to: number): [number, number] {
	const f = from > doc.length ? doc.lines : doc.lineAt(from).number - 1;
	const t = to <= from ? f : doc.lineAt(Math.min(to - 1, doc.length)).number;
	return [f, t];
}

/** `git/review.ts`'s hunks for these chunks: `a` is the base's document, `b` the buffer's. The chunks are the merge view's, so a last line that gains or loses its newline is a changed line, as in Classic. */
export function hunksOf(chunks: readonly Chunk[], a: Text, b: Text): LineHunk[] {
	return chunks.map((c) => {
		// An empty base holds no line, though a document is never without one: nothing is replaced in it.
		const [fromA, toA] = a.length === 0 ? [0, 0] : lineRange(a, c.fromA, c.toA);
		const [fromB, toB] = lineRange(b, c.fromB, c.toB);
		return { fromA, toA, fromB, toB };
	});
}

export function rowClass(row: Row): string {
	return row.kind === 'same' ? 'cm-review-same' : row.kind === 'add' ? 'cm-review-add' : 'cm-review-del';
}

/** The text of each row: a red row's is the base's line, the others' the buffer's. */
export function rowText(row: Row, a: Text, b: Text | null): string {
	return row.kind === 'del' ? a.line(row.a + 1).text : b!.line(row.b + 1).text;
}

/** The right pane's document: one line per row. */
export function rowsText(rs: readonly Row[], a: Text, b: Text | null): string {
	return rs.map((r) => rowText(r, a, b)).join('\n');
}

const tokenMarks = new Map<string, Decoration>();
const tokenMark = (cls: string): Decoration => {
	let d = tokenMarks.get(cls);
	if (d === undefined) tokenMarks.set(cls, (d = Decoration.mark({ class: cls })));
	return d;
};

const treeOf = (s: EditorState) => ensureSyntaxTree(s, s.doc.length, 50) ?? syntaxTree(s);
const lineDeco = new Map<string, Decoration>();
const lineClass = (cls: string): Decoration => {
	let d = lineDeco.get(cls);
	if (d === undefined) lineDeco.set(cls, (d = Decoration.line({ class: cls })));
	return d;
};

/** What the right pane shows, and what the maps need to know about it. */
export interface Render {
	rows: readonly Row[];
	hunks: readonly LineHunk[];
	/** The right pane's document. */
	text: string;
	/** Line classes and syntax highlighting, over `text`'s positions. */
	deco: DecorationSet;
	/** The buffer's line count (0 with no buffer). */
	lines: number;
}

/**
 * The right pane for these hunks: `a` is the base's state, `b` the buffer's (null: no buffer, every
 * row is red). Highlighting is per side: the buffer's tree for same and added rows, the base's for red ones.
 */
export function renderOf(a: EditorState, b: EditorState | null, hunks: readonly LineHunk[], nA: number = a.doc.lines): Render {
	const nB = b === null ? 0 : b.doc.lines;
	const rs = rowsOf(hunks, nA, nB);
	const ranges: Range<Decoration>[] = [];
	const treeA = treeOf(a);
	const treeB = b === null ? null : treeOf(b);
	let at = 0;
	for (const r of rs) {
		const fromBase = r.kind === 'del';
		const doc = fromBase ? a.doc : b!.doc;
		const line = doc.line((fromBase ? r.a : (r as { b: number }).b) + 1);
		ranges.push(lineClass(rowClass(r)).range(at));
		highlightTree(fromBase ? treeA : treeB!, highlightStyle, (from, to, cls) => {
			const f = Math.max(from, line.from);
			const t = Math.min(to, line.to);
			if (f < t) ranges.push(tokenMark(cls).range(at + f - line.from, at + t - line.from));
		}, line.from, line.to);
		at += line.length + 1;
	}
	return { rows: rs, hunks, text: rowsText(rs, a.doc, b === null ? null : b.doc), deco: Decoration.set(ranges, true), lines: nB };
}

/** A mark in the left gutter: its class colours the gutter's element. */
class Mark extends GutterMarker {
	constructor(readonly cls: string) {
		super();
		this.elementClass = cls;
	}
	eq(o: GutterMarker): boolean {
		return o instanceof Mark && o.cls === this.cls;
	}
	toDOM(): Node {
		const span = document.createElement('span');
		span.className = 'cm-review-mark';
		return span;
	}
}
const MARKS = {
	added: new Mark('cm-review-added'),
	modified: new Mark('cm-review-modified'),
	before: new Mark('cm-review-del-before'),
	after: new Mark('cm-review-del-after'),
};

/**
 * The left gutter's marks over the buffer's document: a line a hunk added or changed, and, where
 * lines were deleted, the line they were before (its top edge), or the last line (its bottom edge)
 * for a deletion at the end.
 */
export function leftMarks(g: Gutter, doc: Text): RangeSet<GutterMarker> {
	if (doc.lines === 0) return RangeSet.empty;
	const ranges: Range<GutterMarker>[] = [];
	for (const l of g.lines) if (l.line < doc.lines) ranges.push(MARKS[l.kind].range(doc.line(l.line + 1).from));
	for (const d of g.deleted) {
		const end = d.at >= doc.lines;
		ranges.push(MARKS[end ? 'after' : 'before'].range(doc.line((end ? doc.lines - 1 : d.at) + 1).from));
	}
	return RangeSet.of(ranges, true);
}

const setMarks = StateEffect.define<RangeSet<GutterMarker>>();
const marksField = StateField.define<RangeSet<GutterMarker>>({
	create: () => RangeSet.empty,
	update(v, tr) {
		for (const e of tr.effects) if (e.is(setMarks)) return e.value;
		return tr.docChanged ? v.map(tr.changes) : v;
	},
});

const setRender = StateEffect.define<Render>();
const EMPTY: Render = { rows: [], hunks: [], text: '', deco: Decoration.none, lines: 0 };
const renderField = StateField.define<Render>({
	create: () => EMPTY,
	update(v, tr) {
		for (const e of tr.effects) if (e.is(setRender)) return e.value;
		return v;
	},
	provide: (f) => EditorView.decorations.from(f, (r) => r.deco),
});

/** A disk line number, or blank for a red row. */
class Num extends GutterMarker {
	constructor(readonly n: string) {
		super();
	}
	eq(o: GutterMarker): boolean {
		return o instanceof Num && o.n === this.n;
	}
	toDOM(): Node {
		return document.createTextNode(this.n);
	}
}

/** The disk line of each row of the right pane, as text; '' for a red row. */
export function diskLabels(rs: readonly Row[]): string[] {
	return rs.map((r) => (r.kind === 'del' ? '' : String(r.b + 1)));
}

const widest = (rs: readonly Row[]): Num => new Num('0'.repeat(String(Math.max(1, rs.filter((r) => r.kind !== 'del').length)).length));
const numbers = cmGutter({
	class: 'cm-lineNumbers',
	lineMarker(view, line) {
		const i = view.state.doc.lineAt(line.from).number - 1;
		return new Num(diskLabels(view.state.field(renderField).rows)[i] ?? '');
	},
	lineMarkerChange: (u) => u.startState.field(renderField) !== u.state.field(renderField),
	initialSpacer: (view) => widest(view.state.field(renderField).rows),
	updateSpacer: (_s, u) => widest(u.state.field(renderField).rows),
});

/** Review's own rules, in the tokens; no colour literal. */
export const reviewTheme = EditorView.theme({
	'.cm-review-same': { opacity: '0.55' },
	'.cm-review-add': { backgroundColor: mix('--success', 12) },
	'.cm-review-del': { backgroundColor: mix('--danger', 12) },
	'.cm-review-gutter .cm-gutterElement': { padding: '0' },
	'.cm-review-mark': { display: 'inline-block', width: '4px' },
	'.cm-review-added': { background: 'var(--success)' },
	'.cm-review-modified': { background: 'var(--accent)' },
	'.cm-review-del-before': { boxShadow: 'inset 0 2px 0 var(--danger)' },
	'.cm-review-del-after': { boxShadow: 'inset 0 -2px 0 var(--danger)' },
});

// ---- the view ------------------------------------------------------------------------------

const SHEET = `
.droidyard-review { flex: 1 1 0; min-height: 0; display: flex; flex-direction: column; }
.droidyard-review.single .droidyard-sides { grid-template-columns: 1fr; }
.droidyard-panes { flex: 1 1 0; min-height: 0; display: flex; }
.droidyard-pane { flex: 1 1 0; min-width: 0; min-height: 0; display: flex; flex-direction: column; }
.droidyard-pane + .droidyard-pane { border-left: 1px solid var(--border); }
${SIDES_CSS}`;

const EASE_MS = 120;

interface Pad {
	top: number;
	bottom: number;
}

class CmReview implements EditorHandle {
	#buffer: CmBuffer | null;
	/** A commit's text in the buffer's place (read-only), else null. */
	#text: ReviewOptions['text'];
	#opts: ReviewOptions;
	#root: ShadowRoot;
	#sheet: CSSStyleSheet | null = null;
	#wrap: HTMLElement;
	#leftEl: HTMLElement | null = null;
	#rightEl: HTMLElement;
	#left: EditorView | null = null;
	#right: EditorView;
	#baseDoc: Text;
	#baseState: EditorState;
	#lang: Extension | null = null;
	#chunks: readonly Chunk[] = [];
	#changes: ChangeSet | null = null;
	#render: Render = EMPTY;
	#map: ElasticMap | null = null;
	#driver = new Driver();
	#anchor: Anchor = { y: 0, offset: 0 };
	#kind: 'cursor' | 'centre' | null = null;
	#ease: { t0: number; from: number } | null = null;
	#pad: Record<Side, Pad> = { left: { top: 0, bottom: 0 }, right: { top: 0, bottom: 0 } };
	#raf = 0;
	#own: Extension = [];
	#observer: ResizeObserver | null = null;
	#save: () => void;
	#prevSave: (() => void) | null;
	#onKey: (e: KeyboardEvent) => void;
	#offChange: () => void = () => {};
	#unscroll: (() => void)[] = [];
	#gone = false;

	constructor(host: HTMLElement, opts: ReviewOptions) {
		if (opts.text !== undefined && opts.buffer !== null) throw new Error('a read-only text takes the buffer’s place; give one of them');
		if (opts.buffer !== null && !(opts.buffer instanceof CmBuffer)) throw new Error('the buffer was not made by this editor');
		this.#buffer = opts.buffer;
		this.#text = opts.text;
		this.#opts = opts;
		const root = host.shadowRoot ?? host.attachShadow({ mode: 'open' });
		root.replaceChildren();
		this.#root = root;
		this.#onKey = (e) => {
			if (!isSaveKey(e)) return;
			e.preventDefault();
			opts.onSave();
		};
		root.addEventListener('keydown', this.#onKey as EventListener);
		if (typeof CSSStyleSheet !== 'undefined' && 'adoptedStyleSheets' in root) {
			const sheet = new CSSStyleSheet();
			sheet.replaceSync(SHEET);
			root.adoptedStyleSheets = [...root.adoptedStyleSheets, sheet];
			this.#sheet = sheet;
		}
		this.#baseDoc = EditorState.create({ doc: opts.base }).doc;
		this.#baseState = this.#makeBase();

		// The DOM: captions, then the panes (the left only with a buffer).
		const single = this.#buffer === null && this.#text === undefined;
		const [leftLabel, rightLabel] = opts.labels ? [opts.labels.left, opts.labels.right] : ['Working tree', 'Against HEAD'];
		this.#wrap = document.createElement('div');
		this.#wrap.className = single ? 'droidyard-review single' : 'droidyard-review';
		this.#wrap.dataset.review = 'root';
		const sides = document.createElement('div');
		sides.className = 'droidyard-sides';
		for (const label of single ? [opts.labels?.right ?? 'HEAD (deleted from the working tree)'] : [leftLabel, rightLabel]) {
			const span = document.createElement('span');
			span.textContent = label;
			sides.append(span);
		}
		const panes = document.createElement('div');
		panes.className = 'droidyard-panes';
		this.#rightEl = document.createElement('div');
		this.#rightEl.className = 'droidyard-pane';
		this.#rightEl.dataset.review = 'right';
		if (!single) {
			this.#leftEl = document.createElement('div');
			this.#leftEl.className = 'droidyard-pane';
			this.#leftEl.dataset.review = 'left';
			panes.append(this.#leftEl);
		}
		panes.append(this.#rightEl);
		this.#wrap.append(sides, panes);
		root.append(this.#wrap);

		this.#right = this.#makeRight();
		this.#bindScroll('right', this.#right);
		if (this.#buffer !== null) {
			this.#mountLeft();
			this.#offChange = this.#buffer.onChange(() => this.#follow());
		} else if (this.#text !== undefined) {
			this.#mountLeft();
		} else {
			this.#rebuild();
		}
		this.#prevSave = this.#buffer?.onSave ?? null;
		this.#save = () => opts.onSave();
		if (this.#buffer !== null) this.#buffer.onSave = this.#save;
		if (this.#buffer === null) this.#loadLanguage();
		if (typeof ResizeObserver !== 'undefined') {
			this.#observer = new ResizeObserver(() => {
				this.#map = null;
				this.#schedule();
			});
			this.#observer.observe(panes);
		}
		this.#schedule();
	}

	// The language of the side shown: the buffer's, or (no buffer) the one the path picks.
	get #language(): Extension {
		return this.#buffer !== null ? this.#buffer.language : this.#own;
	}

	#loadLanguage(): void {
		const path = this.#text?.path ?? this.#opts.path;
		const entry = path === undefined ? null : languageFor(path);
		if (entry === null) return;
		entry.load().then(
			(ext) => {
				if (this.#gone) return;
				this.#own = ext;
				// A read-only text's left pane takes the language too (an effect, not an edit).
				if (this.#text !== undefined) this.#left?.dispatch({ effects: useLanguage(ext) });
				this.#schedule();
			},
			() => {},
		);
	}

	#makeBase(): EditorState {
		this.#lang = this.#language;
		return EditorState.create({ doc: this.#baseDoc, extensions: [this.#lang] });
	}

	#makeRight(): EditorView {
		const state = EditorState.create({
			doc: '',
			extensions: [
				EditorState.lineSeparator.of('\n'),
				EditorState.readOnly.of(true),
				EditorView.editable.of(false),
				renderField,
				numbers,
				syntaxHighlighting(highlightStyle),
				EditorView.contentAttributes.of({ 'aria-label': this.#opts.labels ? `Changes: ${this.#opts.labels.right}` : 'Changes against HEAD', 'aria-readonly': 'true' }),
				theme,
				mergeTheme,
				reviewTheme,
				EditorView.domEventHandlers({ click: (e, view) => this.#clicked(e, view) }),
				EditorView.updateListener.of((u) => {
					if (u.docChanged || u.heightChanged) this.#map = null;
					if (u.heightChanged) this.#schedule();
				}),
			],
		});
		return new EditorView({ state, parent: this.#rightEl, root: this.#root });
	}

	/** The left pane's document and extensions before the review's own: the buffer's, or the read-only text's. */
	#leftSide(): { config: EditorStateConfig; mirrored: Extension } {
		const buffer = this.#buffer;
		if (buffer === null) {
			const text = this.#text!;
			return { config: textSide(text.text, 'File contents', this.#own), mirrored: [] };
		}
		const mirrored = EditorView.updateListener.of((u) => {
			if (u.docChanged && !u.transactions.some((t) => t.annotation(followed) === true)) mirror(buffer, u.startState, u.state, u.changes);
		});
		return { config: bufferSide(buffer), mirrored };
	}

	#mountLeft(): void {
		const { config: side, mirrored } = this.#leftSide();
		const watch = EditorView.updateListener.of((u) => {
			if (u.docChanged) {
				this.#changes = this.#changes === null ? u.changes : this.#changes.compose(u.changes);
				this.#map = null;
				this.#schedule();
			} else if (u.heightChanged || u.geometryChanged) {
				this.#map = null;
				this.#schedule();
			}
			if (u.selectionSet) this.#schedule(true);
			// The language's code arrived: HEAD's rows are highlighted again.
			if (u.startState.facet(languageFacet) !== u.state.facet(languageFacet)) this.#schedule();
		});
		const state = EditorState.create({
			...side,
			extensions: [
				side.extensions ?? [],
				marksField,
				cmGutter({ class: 'cm-review-gutter', markers: (v) => v.state.field(marksField), initialSpacer: () => MARKS.added }),
				reviewTheme,
				mirrored,
				watch,
			],
		});
		const view = new EditorView({ state, parent: this.#leftEl!, root: this.#root });
		this.#left = view;
		this.#bindScroll('left', view);
		this.#pad.left = { top: 0, bottom: 0 };
		this.#chunks = buildChunks(this.#baseDoc, view.state.doc);
		this.#changes = null;
		this.#rebuild();
	}

	#bindScroll(side: Side, view: EditorView): void {
		const el = view.scrollDOM;
		const on = () => {
			const switched = this.#driver.side !== side;
			if (!this.#driver.scrolled(side, el.scrollTop)) return;
			if (switched && side === 'left') this.#kind = null; // the anchor is the left's again: ease into it
			if (this.#changes === null) this.#apply();
		};
		el.addEventListener('scroll', on, { passive: true });
		this.#unscroll.push(() => el.removeEventListener('scroll', on));
	}

	#follow(): void {
		if (this.#gone || this.#left === null) return;
		const next = follow(this.#left.state, this.#buffer!);
		if (next === null) return;
		if (next === 'remount') {
			// The separator or read-only changed: the left is made again from the buffer.
			this.#left.destroy();
			this.#left = null;
			this.#leftEl!.replaceChildren();
			this.#unscroll.splice(0).forEach((f) => f());
			this.#bindScroll('right', this.#right);
			this.#mountLeft();
			return;
		}
		this.#left.dispatch(next);
	}

	// ---- the rows -------------------------------------------------------------------------

	/** Takes the pending edits into the chunks and puts the rows, classes, marks and numbers on the panes. */
	#rebuild(): void {
		const left = this.#left;
		if (left !== null && this.#changes !== null) {
			this.#chunks = updateChunks(this.#chunks, this.#baseDoc, left.state.doc, this.#changes.desc);
		}
		this.#changes = null;
		if (this.#language !== this.#lang) this.#baseState = this.#makeBase();
		let render: Render;
		if (left === null) {
			// No buffer: all of HEAD's lines, red (a final newline does not make a last empty line).
			const n = this.#baseDoc.lines - (this.#baseDoc.lines > 1 && this.#baseDoc.line(this.#baseDoc.lines).length === 0 ? 1 : 0);
			render = renderOf(this.#baseState, null, [{ fromA: 0, toA: n, fromB: 0, toB: 0 }], n);
		} else {
			render = renderOf(this.#baseState, left.state, hunksOf(this.#chunks, this.#baseDoc, left.state.doc));
		}
		const right = this.#right;
		const change = minimalChange(right.state.doc.toString(), render.text);
		right.dispatch({
			changes: change === null ? undefined : { from: change.from, to: change.to, insert: change.insert },
			effects: setRender.of(render),
		});
		this.#render = render;
		if (left !== null) left.dispatch({ effects: setMarks.of(leftMarks(gutterOf(render.hunks), left.state.doc)) });
		this.#map = null;
	}

	/** One frame's work: the rows if edits are pending, then the scroll. */
	#schedule(ease = false): void {
		if (ease) this.#easeNext = true;
		if (this.#raf !== 0 || this.#gone) return;
		this.#raf = requestAnimationFrame(() => {
			this.#raf = 0;
			if (this.#gone) return;
			if (this.#changes !== null || this.#language !== this.#lang) this.#rebuild();
			this.#apply();
			if (this.#ease !== null) this.#schedule();
		});
	}
	#easeNext = false;

	// ---- the scroll -----------------------------------------------------------------------

	#pos(view: EditorView, line: number): Span {
		const b = view.lineBlockAt(view.state.doc.line(line + 1).from);
		return { top: b.top, height: b.height };
	}

	#measure(): ElasticMap | null {
		if (this.#map !== null) return this.#map;
		const left = this.#left;
		const r = this.#render;
		if (left === null || left.state.doc.lines !== r.lines || this.#right.state.doc.lines !== r.rows.length || r.lines === 0) return null;
		const l: Span[] = [];
		for (let i = 0; i < r.lines; i++) l.push(this.#pos(left, i));
		const rr: Span[] = [];
		for (let i = 0; i < r.rows.length; i++) rr.push(this.#pos(this.#right, i));
		this.#map = new ElasticMap({ left: l, right: rr, rowOfLine: rowOfLine(r.rows, r.lines) });
		return this.#map;
	}

	/** The top of the viewport in a pane's line-block coordinates (its scroll less its padding). */
	#v(side: Side): number {
		const view = side === 'left' ? this.#left! : this.#right;
		return view.scrollDOM.scrollTop - this.#pad[side].top;
	}

	/** Holds the follower level with the driver's anchor. */
	#apply(): void {
		const left = this.#left;
		const m = this.#measure();
		if (left === null || m === null) return;
		const driver = this.#driver;
		const lh = left.scrollDOM.clientHeight;
		const rh = this.#right.scrollDOM.clientHeight;
		let follower: Side;
		let target: number;
		let changed = this.#easeNext;
		this.#easeNext = false;
		if (driver.side === 'left') {
			const head = left.lineBlockAt(left.state.selection.main.head);
			const cursor = { top: head.top, height: head.height };
			const a = anchorOf(cursor, { scrollTop: this.#v('left'), height: lh });
			const kind = a.y === cursor.top ? 'cursor' : 'centre';
			if (kind !== this.#kind) changed = true;
			this.#kind = kind;
			this.#anchor = a;
			const al = alignRight(m, a.y, a.offset, rh);
			follower = 'right';
			target = al.scrollTop - al.padTop;
		} else {
			const off = this.#anchor.offset;
			const al = alignLeft(m, this.#v('right') + off, off, lh);
			follower = 'left';
			target = al.scrollTop - al.padTop;
		}
		if (changed) this.#ease = { t0: performance.now(), from: this.#v(follower) };
		let v = target;
		if (this.#ease !== null) {
			const t = (performance.now() - this.#ease.t0) / EASE_MS;
			if (t >= 1) this.#ease = null;
			else v = this.#ease.from + (target - this.#ease.from) * (t * (2 - t));
		}
		this.#place(follower, m, v);
	}

	/** Puts a pane's viewport top at `v`, padding its content so that is reachable. */
	#place(side: Side, m: ElasticMap, v: number): void {
		const view = side === 'left' ? this.#left! : this.#right;
		const el = view.scrollDOM;
		const content = side === 'left' ? m.leftEnd : m.rightEnd;
		const top = Math.max(0, -v);
		const bottom = Math.max(0, v + el.clientHeight - (content + top));
		const pad = this.#pad[side];
		if (pad.top !== top || pad.bottom !== bottom) {
			pad.top = top;
			pad.bottom = bottom;
			view.contentDOM.style.paddingTop = top > 0 ? `${top}px` : '';
			view.contentDOM.style.paddingBottom = bottom > 0 ? `${bottom}px` : '';
			view.requestMeasure();
		}
		const before = el.scrollTop;
		el.scrollTop = v + top;
		const now = el.scrollTop;
		if (Math.abs(now - before) > 0.01) {
			this.#driver.expect(side, now);
			// No event follows a position the pane was already at; a pending echo must not outlive the frame it was set in.
			requestAnimationFrame(() => requestAnimationFrame(() => this.#driver.release(side, now)));
		}
	}

	#clicked(e: MouseEvent, view: EditorView): boolean {
		const left = this.#left;
		if (left === null) return false;
		const sel = view.dom.ownerDocument.getSelection();
		if (sel !== null && !sel.isCollapsed) return false; // selecting text to copy, not asking for a line
		const pos = view.posAtCoords({ x: e.clientX, y: e.clientY });
		if (pos === null) return false;
		const r = this.#render;
		const row = view.state.doc.lineAt(pos).number - 1;
		const line = lineOfRow(r.rows, r.hunks, r.lines)[row];
		if (line === undefined || line >= left.state.doc.lines) return false;
		const at = left.state.doc.line(line + 1).from;
		left.dispatch({ selection: { anchor: at }, effects: EditorView.scrollIntoView(at, { y: 'nearest' }), userEvent: 'select' });
		left.focus();
		return false;
	}

	reveal(line: number, col: number): void {
		const view = this.#left;
		if (view === null) return;
		const doc = view.state.doc;
		const l = doc.line(Math.min(doc.lines, Math.max(1, Math.trunc(line))));
		const pos = l.from + Math.min(l.length, Math.max(0, Math.trunc(col) - 1));
		view.dispatch({ selection: { anchor: pos }, effects: EditorView.scrollIntoView(pos, { y: 'center' }) });
	}

	setDiagnostics(list: readonly EditorDiagnostic[]): void {
		void list;
	}

	destroy(): void {
		this.#gone = true;
		if (this.#raf !== 0) cancelAnimationFrame(this.#raf);
		this.#observer?.disconnect();
		this.#offChange();
		this.#unscroll.splice(0).forEach((f) => f());
		this.#root.removeEventListener('keydown', this.#onKey as EventListener);
		if (this.#buffer !== null && this.#buffer.onSave === this.#save) this.#buffer.onSave = this.#prevSave;
		this.#left?.destroy();
		this.#right.destroy();
		this.#wrap.remove();
		if (this.#sheet !== null) this.#root.adoptedStyleSheets = this.#root.adoptedStyleSheets.filter((s) => s !== this.#sheet);
	}
}

export function mountReview(host: HTMLElement, opts: ReviewOptions): EditorHandle {
	return new CmReview(host, opts);
}
