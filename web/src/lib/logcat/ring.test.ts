import { describe, expect, it } from 'vitest';
import { Ring } from './ring';

describe('Ring', () => {
	// Decision: the ring keeps the newest `capacity` items and counts every item it
	// forgot, so the footer's "dropped" is the truth (plan m2a D8).
	it('keeps the newest items at its cap and counts what it forgot', () => {
		const r = new Ring<number>(3);
		const evictions = [1, 2, 3, 4, 5].map((n) => r.push(n));
		expect(evictions).toEqual([false, false, false, true, true]);
		expect(r.length).toBe(3);
		expect(r.evicted).toBe(2);
		expect([r.at(r.first), r.at(r.first + 1), r.at(r.first + 2)]).toEqual([3, 4, 5]);
	});

	// Decision: an item's absolute index survives eviction, so a view can name it.
	it('numbers items by an index that only grows', () => {
		const r = new Ring<string>(2);
		r.push('a');
		r.push('b');
		r.push('c');
		expect(r.first).toBe(1);
		expect(r.end).toBe(3);
		expect(r.at(0)).toBeUndefined(); // forgotten
		expect(r.at(1)).toBe('b');
		expect(r.at(2)).toBe('c');
		expect(r.at(3)).toBeUndefined(); // not yet pushed
	});

	// Decision: clear empties the ring and its count, but the next item does not reuse an
	// index a view may still hold.
	it('clears what it holds and its count, not its numbering', () => {
		const r = new Ring<number>(2);
		for (const n of [1, 2, 3]) r.push(n);
		r.clear();
		expect(r.length).toBe(0);
		expect(r.evicted).toBe(0);
		expect(r.first).toBe(r.end);
		expect(r.end).toBe(3);
		r.push(9);
		expect(r.at(3)).toBe(9);
		expect(r.at(1)).toBeUndefined();
	});

	it('refuses a capacity that is not a positive integer', () => {
		expect(() => new Ring(0)).toThrow(RangeError);
		expect(() => new Ring(1.5)).toThrow(RangeError);
	});
});
