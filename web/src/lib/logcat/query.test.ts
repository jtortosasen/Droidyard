import { describe, expect, it } from 'vitest';
import type { Entry, Line } from './entry';
import { compileQuery, parseQuery, type QueryContext } from './query';
import { Ring, RING_CAPACITY } from './ring';
import { line } from './testing';
import { View } from './view';

const ok = (text: string, e: Entry, ctx: QueryContext = {}) => compileQuery(text, ctx)(e);
const err = (text: string) => {
	const r = parseQuery(text);
	if (r.ok) throw new Error(`"${text}" parsed`);
	return r;
};

describe('text keys', () => {
	// Decision (D38): a key matches a case-insensitive substring; `-` negates, `~` is a
	// regex, `=` is the whole value; all case-insensitive.
	it('has substring, negated, regex and exact forms for each key', () => {
		const l = line(1, 'Hello World', { tag: 'NetCache', package: 'dev.x.app' });
		for (const key of ['tag', 'package', 'message', 'line']) {
			const text = key === 'tag' ? 'netc' : key === 'package' ? 'DEV.X' : key === 'message' ? 'hello w' : 'hello';
			expect(ok(`${key}:${text}`, l), `${key}:`).toBe(true);
			expect(ok(`-${key}:${text}`, l), `-${key}:`).toBe(false);
			expect(ok(`${key}:zzz`, l)).toBe(false);
			expect(ok(`-${key}:zzz`, l)).toBe(true);
		}
		expect(ok('tag~:^net.*e$', l)).toBe(true);
		expect(ok('tag~:^cache', l)).toBe(false);
		expect(ok('tag=:netcache', l)).toBe(true);
		expect(ok('tag=:net', l)).toBe(false);
		expect(ok('message=:"hello world"', l)).toBe(true);
	});

	// Decision (D38): values are bare, quoted with either quote, and backslash escapes;
	// a regex keeps its own backslashes.
	it('reads bare, quoted and escaped values', () => {
		const l = line(1, 'say "hi" (now) a|b d1');
		expect(ok('message:"say \\"hi\\""', l)).toBe(true);
		expect(ok("message:'(now) a|b'", l)).toBe(true);
	});
	it('escapes a space in a bare value and keeps regex backslashes', () => {
		const l = line(1, 'two words d1');
		expect(ok('message:two\\ words', l)).toBe(true);
		expect(ok('message~:"d\\d"', l)).toBe(true);
		expect(ok('message~:"words\\sd"', l)).toBe(true);
	});

	// Decision (D38): a bare word or quoted string is a `line` substring, adjacent bare
	// words are one phrase, and `line` includes the package.
	it('treats bare words as one line phrase', () => {
		const l = line(1, 'connection reset by peer', { tag: 'Net', package: 'dev.x' });
		expect(ok('reset by', l)).toBe(true);
		expect(ok('reset peer', l)).toBe(false); // a phrase, not two words
		expect(ok('"reset by"', l)).toBe(true);
		expect(ok('dev.x net', l)).toBe(true);
		expect(ok('NET', l)).toBe(true);
	});
});

describe('operators', () => {
	const a = line(1, 'm', { tag: 'a', package: 'x' });
	const b = line(1, 'm', { tag: 'b', package: 'x' });
	const c = line(1, 'm', { tag: 'c', package: 'x' });
	const ay = line(1, 'm', { tag: 'a', package: 'y' });

	// Decision (D38): side by side, same-key terms are ORed and different keys ANDed.
	it('ORs same-key terms and ANDs different keys', () => {
		const f = compileQuery('tag:a tag:b package:x');
		expect([a, b, c, ay].map((e) => f(e))).toEqual([true, true, false, false]);
	});

	// Decision (D38): negated terms are ANDed, not ORed.
	it('ANDs negated terms', () => {
		const f = compileQuery('-tag:a -tag:b');
		expect([a, b, c].map((e) => f(e))).toEqual([false, false, true]);
	});

	// Decision (D38): an explicit `&` is an AND even for one key, and AND binds tighter
	// than OR.
	it('binds & before | and honours parentheses', () => {
		expect(ok('tag:a & tag:b', a)).toBe(false);
		const f = compileQuery('tag:a | tag:b & package:y');
		expect([a, b, ay].map((e) => f(e))).toEqual([true, false, true]);
		const g = compileQuery('(tag:a | tag:b) & package:y');
		expect([a, b, ay].map((e) => g(e))).toEqual([false, false, true]);
	});
});

describe('level, age, is, name', () => {
	// Decision (D38): level is that level and above, and assert is fatal.
	it('keeps the level and above, assert being fatal', () => {
		const at = (p: Line['priority']) => line(1, 'x', { priority: p });
		const warn = compileQuery('level:WARN');
		expect(['info', 'warn', 'error', 'fatal'].map((p) => warn(at(p as Line['priority'])))).toEqual([
			false,
			true,
			true,
			true,
		]);
		const assert = compileQuery('level:assert');
		expect(assert(at('error'))).toBe(false);
		expect(assert(at('fatal'))).toBe(true);
	});

	// Decision (D38): age compares the line's time with the clock read at each test.
	it('compares age against the clock', () => {
		const l = line(0, 'x'); // 2026-10-09T12:00:00.000000Z
		const t0 = Date.parse('2026-10-09T12:00:00Z');
		let now = t0 + 4 * 60_000;
		const f = compileQuery('age:5m', { now: () => now });
		expect(f(l)).toBe(true);
		now = t0 + 6 * 60_000;
		expect(f(l)).toBe(false); // the clock is read again
		expect(compileQuery('age:2h', { now: () => now })(l)).toBe(true);
		expect(compileQuery('age:90s', { now: () => now })(l)).toBe(false);
		expect(compileQuery('age:1d', { now: () => now })(l)).toBe(true);
	});

	// Decision (D41): is:crash is the mark, is:stacktrace the per-line shape.
	it('has is:crash and is:stacktrace', () => {
		expect(ok('is:crash', { ...line(1, 'x'), crash: true })).toBe(true);
		expect(ok('is:crash', line(1, 'x'))).toBe(false);
		expect(ok('is:stacktrace', line(1, '\tat com.x.A.b(A.kt:3)'))).toBe(true);
		expect(ok('is:stacktrace', line(1, 'at the end of the day'))).toBe(false);
		expect(ok('is:STACKTRACE', line(1, 'at a.B(C.kt:1)'))).toBe(true);
	});

	// Decision (D38): name: is accepted and matches everything; a `-` on a plain key is ignored.
	it('accepts name and ignores a minus on level', () => {
		expect(ok('name:whatever', line(1, 'x'))).toBe(true);
		expect(ok('-level:error', line(1, 'x', { priority: 'info' }))).toBe(false);
		expect(ok('-level:error', line(1, 'x', { priority: 'error' }))).toBe(true);
	});
});

describe('package:mine', () => {
	const mine = new Set(['dev.x.app']);

	// Decision (D39): mine matches a line of one of the ids, or an error-or-above line
	// whose message names one; a warn message naming it does not count.
	it('matches by package and by an error message naming an id', () => {
		const f = compileQuery('package:mine', { mine });
		expect(f(line(1, 'x', { package: 'dev.x.app' }))).toBe(true);
		expect(f(line(1, 'x', { package: 'dev.y' }))).toBe(false);
		const sys = { tag: 'ActivityManager', package: 'system' };
		expect(f(line(1, 'Process dev.x.app has died', { ...sys, priority: 'error' }))).toBe(true);
		expect(f(line(1, 'Process dev.x.app has died', { ...sys, priority: 'fatal' }))).toBe(true);
		expect(f(line(1, 'Process dev.x.app has died', { ...sys, priority: 'warn' }))).toBe(false);
	});
	it('matches nothing while no id is known, and follows the set', () => {
		const ids = new Set<string>();
		const f = compileQuery('package:mine', { mine: ids });
		expect(f(line(1, 'x', { package: 'dev.x.app' }))).toBe(false);
		ids.add('dev.x.app');
		expect(f(line(1, 'x', { package: 'dev.x.app' }))).toBe(true);
	});

	// Decision (D39): only the plain, non-negated form is "mine"; any other is the text.
	it('treats -package:mine and package~:mine as the text "mine"', () => {
		const own = line(1, 'x', { package: 'dev.x.app' });
		const named = line(1, 'x', { package: 'mine.thing' });
		expect(compileQuery('-package:mine', { mine })(own)).toBe(true);
		expect(compileQuery('-package:mine', { mine })(named)).toBe(false);
		expect(compileQuery('package~:mine', { mine })(own)).toBe(false);
		expect(compileQuery('package~:mine', { mine })(named)).toBe(true);
	});

	// Decision (D39): case-insensitive, and ORed with other package terms.
	it('is case-insensitive and ORs with another package term', () => {
		const f = compileQuery('package:MINE package:other', { mine });
		expect(f(line(1, 'x', { package: 'DEV.X.APP' }))).toBe(true);
		expect(f(line(1, 'x', { package: 'other.app' }))).toBe(true);
	});
});

describe('errors', () => {
	// Decision (D38): a bad query is an error with its position, never a guess.
	it.each([
		['process:x', 0, /process/],
		['tag:a bogus:1', 6, /unknown key/],
		['level:loud', 6, /level/],
		['age:5x', 4, /age/],
		['is:nothing', 3, /is:/],
		['level~:warn', 5, /takes no/],
		['tag~:(', 5, /needs a value/],
		['tag:', 4, /needs a value/],
		['tag~:[', 5, /regular expression/],
		['tag:a (tag:b', 6, /missing "\)"/],
		['tag:a)', 5, /unbalanced/],
		['tag:a |', 6, /needs a term/],
		['& tag:a', 0, /expected a term/],
		['tag:a & | tag:b', 6, /needs a term/],
		['()', 0, /expected a term/],
		['message:"open', 8, /unterminated/],
		['"open', 0, /unterminated/],
	])('rejects %j at %i', (text, at, message) => {
		const r = err(text);
		expect(r.at).toBe(at);
		expect(r.error).toMatch(message);
	});

	// Decision (D38): an empty query shows everything.
	it('accepts the empty query', () => {
		expect(ok('', line(1, 'x'))).toBe(true);
		expect(ok('   ', line(1, 'x'))).toBe(true);
	});
});

describe('markers', () => {
	// Decision (m2a, kept): no query hides a marker.
	it('always pass', () => {
		const m: Entry = { marker: 'dropped', text: '3 lines' };
		expect(ok('tag:nothing level:fatal', m)).toBe(true);
		expect(ok('-tag:x', m)).toBe(true);
	});
});

describe('speed', () => {
	// Decision (plan m2e step 3): applying a query to a full ring is fast enough to run as
	// the user types; 500 ms in Node is the generous bound, the measured time is reported.
	it('rebuilds a full ring under 500 ms', () => {
		const ring = new Ring<Entry>(RING_CAPACITY);
		for (let i = 0; i < RING_CAPACITY; i++) {
			ring.push(
				line(i % 1_000_000, `message number ${i} with some text to search`, {
					tag: i % 3 === 0 ? 'a' : 'Other',
					package: i % 2 === 0 ? 'dev.x.app' : 'dev.y',
					priority: i % 50 === 0 ? 'error' : 'info',
				}),
			);
		}
		const view = new View(ring, () => true);
		const f = compileQuery('package:mine tag:a | level:error', { mine: new Set(['dev.x.app']) });
		const t0 = performance.now();
		view.setFilter(f);
		const ms = performance.now() - t0;
		console.log(`setFilter over ${RING_CAPACITY} lines: ${ms.toFixed(1)} ms`);
		expect(view.length).toBeGreaterThan(0);
		expect(ms).toBeLessThan(500);
	});
});
