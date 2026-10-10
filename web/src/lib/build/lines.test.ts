import { describe, expect, it } from 'vitest';
import { LineStore, splitLines, utf8Length } from './lines';

describe('utf8Length', () => {
	// Decision: the daemon's offsets are bytes of the console, so a line's length is its
	// UTF-8 bytes, not its characters (an emoji is one surrogate pair and four bytes).
	it('counts UTF-8 bytes like the encoder does', () => {
		for (const s of ['', 'abc', 'año', 'ü€', '😀', 'a😀b', '\ud800', 'x\udc00y']) {
			expect(utf8Length(s)).toBe(new TextEncoder().encode(s).length);
		}
	});
});

describe('splitLines', () => {
	// Decision: a read's text ends in a newline, and the empty piece after it is not a line;
	// an empty line in the middle is one.
	it('does not make a line of what follows the last newline', () => {
		expect(splitLines('a\n\nb\n')).toEqual(['a', '', 'b']);
		expect(splitLines('')).toEqual([]);
		expect(splitLines('x')).toEqual(['x']);
	});
});

describe('LineStore', () => {
	// Decision (D18): the cap forgets the oldest lines, and the byte position of the first
	// line held moves by exactly their bytes, so "Earlier" reads from the right place.
	it('forgets from the front over the cap and keeps the byte position of the first line', () => {
		const s = new LineStore(3);
		s.append(['aa', 'bbb', 'c']);
		expect(s.startOffset).toBe(0);
		s.append(['dd', 'e']);
		expect(s.rows(0, 9).map((r) => r.text)).toEqual(['c', 'dd', 'e']);
		expect(s.startOffset).toBe(3 + 4); // 'aa\n' and 'bbb\n'
		expect(s.startSeq).toBe(2);
		expect(s.evicted).toBe(2);
		expect(s.count).toBe(3);
	});

	// Decision: a row keeps its seq while lines are forgotten before it or added before it
	// (the keyed list does not redraw it).
	it('keeps a line\'s seq when lines are added before it', () => {
		const s = new LineStore(10);
		s.append(['x', 'y']);
		expect(s.rows(1, 2)).toEqual([{ seq: 1, text: 'y' }]);
		s.prepend(['p', 'q', 'r'], 0);
		expect(s.rows(4, 5)).toEqual([{ seq: 1, text: 'y' }]);
		expect(s.startSeq).toBe(-3);
		expect(s.startOffset).toBe(0);
	});
});
