// The Logcat query language (plan m2e D38, D39): the subset of Android Studio's Logcat
// query that the panel takes. A tokenizer and a recursive-descent parser build one
// predicate over the entries; a text that does not parse is an error with a position,
// never a guess (the caller keeps the last valid query).
//
//   query   := or
//   or      := and ('|' and)*
//   and     := unit (('&')? unit)*     AND binds tighter than OR
//   unit    := term | phrase | string | '(' or ')'
//   term    := '-'? key ('~' | '=')? ':' value      '~' regex, '=' exact, else substring
//
// Terms written next to each other without `&` follow Studio's rule: non-negated terms
// of the same key are ORed, different keys ANDed, negated terms ANDed. An explicit `&`
// starts a new run, so `tag:a & tag:b` is an AND.
import { isMarker, PRIORITIES, type Entry, type Line, type Priority } from './entry';

export interface QueryContext {
	/** This workspace's application ids (D39); read at every test, so a live set works. */
	mine?: ReadonlySet<string>;
	/** The host clock in ms, read at every `age:` test. */
	now?: () => number;
}

export type ParseResult = { ok: true; predicate: (e: Entry) => boolean; /** The query has a `package:mine` term (D39): the panel says so when the set is empty. */ usesMine: boolean } | { ok: false; error: string; at: number };

export class QueryError extends Error {
	constructor(
		message: string,
		readonly at: number,
	) {
		super(message);
	}
}

type Pred = (l: Line) => boolean;
type Op = 'sub' | 're' | 'eq';

const TEXT_KEYS = new Set(['tag', 'package', 'message', 'line']);
// level, age, is and name take no `~` or `=` form, and a `-` before them is ignored.
const PLAIN_KEYS = new Set(['level', 'age', 'is', 'name']);

type Token =
	| { kind: 'lparen' | 'rparen' | 'and' | 'or'; at: number; end: number }
	| { kind: 'word' | 'string'; at: number; end: number; text: string }
	| { kind: 'term'; at: number; end: number; key: string; op: Op; neg: boolean; value: string; valueAt: number };

// Tokenizer.

const SPECIAL = /[\s()&|]/;
const KEY = /(-?)([A-Za-z]+)([~=]?):/y;

function scan(text: string): Token[] {
	const out: Token[] = [];
	let i = 0;
	while (i < text.length) {
		const c = text[i]!;
		if (/\s/.test(c)) {
			i++;
		} else if (c === '(' || c === ')' || c === '&' || c === '|') {
			const kind = c === '(' ? 'lparen' : c === ')' ? 'rparen' : c === '&' ? 'and' : 'or';
			out.push({ kind, at: i, end: i + 1 });
			i++;
		} else {
			KEY.lastIndex = i;
			const m = KEY.exec(text);
			if (m) {
				const [whole, minus, key, form] = m as unknown as [string, string, string, string];
				const k = key.toLowerCase();
				if (k === 'process') throw new QueryError('process: is not available', i);
				if (!TEXT_KEYS.has(k) && !PLAIN_KEYS.has(k)) throw new QueryError(`unknown key "${key}"`, i);
				const opAt = i + minus.length + key.length;
				if (PLAIN_KEYS.has(k) && form !== '') throw new QueryError(`${k}: takes no "${form}" form`, opAt);
				const op: Op = form === '~' ? 're' : form === '=' ? 'eq' : 'sub';
				const valueAt = i + whole.length;
				const v = readValue(text, valueAt, op === 're');
				if (v.text === '' && !v.quoted) throw new QueryError(`${k}: needs a value`, valueAt);
				out.push({ kind: 'term', at: i, end: v.end, key: k, op, neg: minus !== '', value: v.text, valueAt });
				i = v.end;
			} else {
				const v = readValue(text, i, false);
				out.push({ kind: v.quoted ? 'string' : 'word', at: i, end: v.end, text: v.text });
				i = v.end;
			}
		}
	}
	return out;
}

/**
 * A quoted string or a bare run. A backslash escapes the next character, except in a
 * regex, where only a quote or a space loses its backslash so `\d` and `\\` survive.
 */
function readValue(text: string, at: number, regex: boolean): { text: string; end: number; quoted: boolean } {
	const quote = text[at] === '"' || text[at] === "'" ? text[at]! : '';
	let i = quote === '' ? at : at + 1;
	let out = '';
	for (; i < text.length; i++) {
		const c = text[i]!;
		if (quote !== '' ? c === quote : SPECIAL.test(c)) break;
		if (c === '\\' && i + 1 < text.length) {
			const n = text[i + 1]!;
			if (!regex || n === '"' || n === "'" || /\s/.test(n)) {
				out += n;
			} else {
				out += c + n;
			}
			i++;
		} else {
			out += c;
		}
	}
	if (quote !== '') {
		if (i >= text.length) throw new QueryError('unterminated quote', at);
		return { text: out, end: i + 1, quoted: true };
	}
	return { text: out, end: i, quoted: false };
}

// Parser.

/** A parsed unit; `group` is the key whose non-negated terms are ORed when written side by side. */
interface Node {
	test: Pred;
	group: string | null;
}

class Parser {
	#toks: Token[];
	#src: string;
	#ctx: QueryContext;
	#p = 0;
	usesMine = false;

	constructor(src: string, toks: Token[], ctx: QueryContext) {
		this.#src = src;
		this.#toks = toks;
		this.#ctx = ctx;
	}

	parse(): Pred {
		if (this.#toks.length === 0) return () => true;
		const root = this.#or();
		const t = this.#toks[this.#p];
		if (t) throw new QueryError('unbalanced ")"', t.at);
		return root;
	}

	#or(): Pred {
		const parts = [this.#and()];
		while (this.#toks[this.#p]?.kind === 'or') {
			const op = this.#toks[this.#p++]!;
			if (!this.#startsUnit()) throw new QueryError('"|" needs a term after it', op.at);
			parts.push(this.#and());
		}
		return any(parts);
	}

	#and(): Pred {
		const runs: Node[][] = [[]];
		runs[0]!.push(this.#unit());
		for (;;) {
			const t = this.#toks[this.#p];
			if (t?.kind === 'and') {
				this.#p++;
				if (!this.#startsUnit()) throw new QueryError('"&" needs a term after it', t.at);
				runs.push([this.#unit()]);
			} else if (this.#startsUnit()) {
				runs[runs.length - 1]!.push(this.#unit());
			} else {
				break;
			}
		}
		return all(runs.flatMap(combineRun));
	}

	#startsUnit(): boolean {
		const k = this.#toks[this.#p]?.kind;
		return k === 'term' || k === 'word' || k === 'string' || k === 'lparen';
	}

	#unit(): Node {
		const t = this.#toks[this.#p++]!;
		switch (t.kind) {
			case 'term':
				return this.#term(t);
			case 'string':
				return { test: textTest('line', 'sub', t.text), group: null };
			case 'word': {
				// Adjacent bare words are one phrase, with the spacing the user typed.
				let phrase = t.text;
				let end = t.end;
				for (let n = this.#toks[this.#p]; n?.kind === 'word'; n = this.#toks[this.#p]) {
					phrase += this.#src.slice(end, n.at) + n.text;
					end = n.end;
					this.#p++;
				}
				return { test: textTest('line', 'sub', phrase), group: null };
			}
			case 'lparen': {
				if (!this.#startsUnit()) throw new QueryError('expected a term after "("', t.at);
				const inner = this.#or();
				if (this.#toks[this.#p]?.kind !== 'rparen') throw new QueryError('missing ")"', t.at);
				this.#p++;
				return { test: inner, group: null };
			}
			default:
				throw new QueryError('expected a term', t.at);
		}
	}

	#term(t: Extract<Token, { kind: 'term' }>): Node {
		const { key, op, value, valueAt } = t;
		if (TEXT_KEYS.has(key)) {
			if (key === 'package' && op === 'sub' && !t.neg && value.toLowerCase() === 'mine') {
				this.usesMine = true;
				return { test: mineTest(this.#ctx), group: 'package' };
			}
			const test = textTest(key, op, value, valueAt);
			return t.neg ? { test: (l) => !test(l), group: null } : { test, group: key };
		}
		// A plain key: a `-` before it is ignored.
		const v = value.toLowerCase();
		let test: Pred;
		if (key === 'level') {
			const min = levelRank(v);
			if (min < 0) throw new QueryError(`unknown level "${value}"`, valueAt);
			test = (l) => PRIORITIES.indexOf(l.priority) >= min;
		} else if (key === 'age') {
			test = ageTest(v, value, valueAt, this.#ctx);
		} else if (key === 'is') {
			if (v === 'crash') test = (l) => l.crash === true;
			else if (v === 'stacktrace') test = (l) => STACK_LINE.test(l.message);
			else throw new QueryError(`is: takes crash or stacktrace, not "${value}"`, valueAt);
		} else {
			test = () => true; // name: accepted and ignored
		}
		return { test, group: key };
	}
}

/** Non-negated terms of one key are ORed, everything else stands alone: the units to AND. */
function combineRun(run: Node[]): Pred[] {
	const groups = new Map<string, Pred[]>();
	const out: Pred[] = [];
	for (const n of run) {
		if (n.group === null) {
			out.push(n.test);
			continue;
		}
		const g = groups.get(n.group);
		if (g) g.push(n.test);
		else groups.set(n.group, [n.test]);
	}
	for (const g of groups.values()) out.push(any(g));
	return out;
}

const all = (ps: Pred[]): Pred => (ps.length === 1 ? ps[0]! : (l) => ps.every((p) => p(l)));
const any = (ps: Pred[]): Pred => (ps.length === 1 ? ps[0]! : (l) => ps.some((p) => p(l)));

// Predicates.

/** The line text `line:` and bare words search: package, tag and message. */
function lineText(l: Line): string {
	return `${l.package === undefined ? '' : l.package + ' '}${l.tag} ${l.message}`;
}

const GETTERS: Record<string, (l: Line) => string> = {
	tag: (l) => l.tag,
	package: (l) => l.package ?? '',
	message: (l) => l.message,
	line: lineText,
};

function textTest(key: string, op: Op, value: string, valueAt = 0): Pred {
	const get = GETTERS[key]!;
	const v = value.toLowerCase();
	if (op === 're') {
		let re: RegExp;
		try {
			re = new RegExp(value, 'i');
		} catch (err) {
			throw new QueryError(`bad regular expression: ${(err as Error).message}`, valueAt);
		}
		return (l) => re.test(get(l));
	}
	if (op === 'eq') return (l) => get(l).toLowerCase() === v;
	return (l) => get(l).toLowerCase().includes(v);
}

/** D39: the line's package is one of the ids, or an error-or-above message names one. */
function mineTest(ctx: QueryContext): Pred {
	return (l) => {
		const mine = ctx.mine;
		if (mine === undefined || mine.size === 0) return false;
		const pkg = (l.package ?? '').toLowerCase();
		const loud = l.priority === 'error' || l.priority === 'fatal';
		const msg = loud ? l.message.toLowerCase() : '';
		for (const id of mine) {
			const low = id.toLowerCase();
			if (pkg === low || (loud && msg.includes(low))) return true;
		}
		return false;
	};
}

function levelRank(name: string): number {
	return PRIORITIES.indexOf((name === 'assert' ? 'fatal' : name) as Priority);
}

const UNIT_MS: Record<string, number> = { s: 1000, m: 60_000, h: 3_600_000, d: 86_400_000 };

function ageTest(v: string, raw: string, at: number, ctx: QueryContext): Pred {
	const m = /^(\d+)([smhd])$/.exec(v);
	if (!m) throw new QueryError(`age: takes a number and s, m, h or d, not "${raw}"`, at);
	const span = Number(m[1]) * UNIT_MS[m[2]!]!;
	const now = ctx.now ?? Date.now;
	return (l) => Date.parse(l.time) >= now() - span; // an unreadable time (NaN) is not recent
}

/** A stack frame line: `at com.x.A.b(A.kt:3)`, tested per line (D41). */
const STACK_LINE = /^\s*at \S.*\(.*\)$/;

// Entry points.

/** Parses `text`; the predicate lets markers through (m2a: a filter never hides a loss). */
export function parseQuery(text: string, ctx: QueryContext = {}): ParseResult {
	try {
		const parser = new Parser(text, scan(text), ctx);
		const root = parser.parse();
		return { ok: true, predicate: (e) => isMarker(e) || root(e), usesMine: parser.usesMine };
	} catch (err) {
		if (err instanceof QueryError) return { ok: false, error: err.message, at: err.at };
		throw err;
	}
}

/** The predicate of a query known to be valid; throws a QueryError otherwise. */
export function compileQuery(text: string, ctx: QueryContext = {}): (e: Entry) => boolean {
	const r = parseQuery(text, ctx);
	if (!r.ok) throw new QueryError(r.error, r.at);
	return r.predicate;
}
