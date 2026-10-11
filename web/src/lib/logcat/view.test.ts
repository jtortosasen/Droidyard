import { describe, expect, it } from 'vitest';
import type { Entry } from './entry';
import { compileQuery } from './query';
import { Ring } from './ring';
import { line } from './testing';
import { View } from './view';

const text = (e: Entry | undefined) => (e && 'message' in e ? e.message : undefined);

function make(capacity: number, query = '') {
	const ring = new Ring<Entry>(capacity);
	const view = new View(ring, compileQuery(query));
	return { ring, view };
}

describe('View', () => {
	it('lists the entries that pass the filter, in order', () => {
		const { ring, view } = make(10, 'message:keep');
		for (const m of ['keep 1', 'drop', 'keep 2']) ring.push(line(1, m));
		view.sync();
		expect(view.length).toBe(2);
		expect([text(view.entry(0)), text(view.entry(1))]).toEqual(['keep 1', 'keep 2']);
	});

	// Decision: a row keeps its seq while older rows are evicted, so the list can key and
	// anchor rows by it.
	it('drops evicted rows from the front without renumbering the rest', () => {
		const { ring, view } = make(3);
		for (const m of ['a', 'b', 'c']) ring.push(line(1, m));
		view.sync();
		expect(view.headSeq).toBe(0);
		ring.push(line(1, 'd'));
		ring.push(line(1, 'e'));
		view.sync();
		expect(view.length).toBe(3);
		expect(view.headSeq).toBe(2);
		expect(view.endSeq).toBe(5);
		expect(text(view.entry(2))).toBe('c');
		expect(text(view.entry(4))).toBe('e');
		expect(view.entry(1)).toBeUndefined(); // evicted
		expect(view.entry(5)).toBeUndefined();
	});

	it('does not index entries the ring forgot before it saw them', () => {
		const { ring, view } = make(2);
		for (const m of ['a', 'b', 'c', 'd', 'e']) ring.push(line(1, m));
		view.sync();
		expect(view.length).toBe(2);
		expect([text(view.entry(view.headSeq)), text(view.entry(view.headSeq + 1))]).toEqual(['d', 'e']);
	});

	// Decision: a new filter is applied to everything the ring holds, not only to what
	// arrives next.
	it('tests the held entries again when the filter changes', () => {
		const { ring, view } = make(10);
		for (const m of ['x one', 'y two', 'x three']) ring.push(line(1, m));
		view.sync();
		expect(view.length).toBe(3);
		view.setFilter(compileQuery('message:"x "'));
		expect(view.length).toBe(2);
		expect(text(view.entry(view.headSeq + 1))).toBe('x three');
		ring.push(line(1, 'y later'));
		ring.push(line(1, 'x later'));
		view.sync();
		expect(view.length).toBe(3);
	});

	it('empties on reset and goes on numbering', () => {
		const { ring, view } = make(10);
		ring.push(line(1, 'a'));
		view.sync();
		ring.clear();
		view.reset();
		expect(view.length).toBe(0);
		ring.push(line(1, 'b'));
		view.sync();
		expect(view.length).toBe(1);
		expect(view.headSeq).toBe(1);
		expect(text(view.entry(1))).toBe('b');
	});
});
