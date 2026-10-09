import { describe, expect, it } from 'vitest';
import type { Entry } from './entry';
import { compileFilter, isOpen, NO_FILTER, parseTags } from './filter';
import { line } from './testing';

const keep = (f: Parameters<typeof compileFilter>[0], e: Entry) => compileFilter(f)(e);

describe('filters', () => {
	// Decision: the priority filter is a minimum, in logcat's order.
	it('shows lines at or above the minimum priority', () => {
		const f = { ...NO_FILTER, minPriority: 'warn' as const };
		expect(keep(f, line(1, 'x', { priority: 'info' }))).toBe(false);
		expect(keep(f, line(1, 'x', { priority: 'warn' }))).toBe(true);
		expect(keep(f, line(1, 'x', { priority: 'fatal' }))).toBe(true);
	});

	// Decision: tags match exactly, as device.logs' tags do, so a tag that merely contains
	// the filter is not shown.
	it('matches tags exactly', () => {
		const f = { ...NO_FILTER, tags: ['ActivityManager', 'Net'] };
		expect(keep(f, line(1, 'x', { tag: 'Net' }))).toBe(true);
		expect(keep(f, line(1, 'x', { tag: 'NetCache' }))).toBe(false);
		expect(keep(f, line(1, 'x', { tag: 'net' }))).toBe(false);
	});

	// Decision: the text filter is a case-insensitive substring of the message.
	it('matches the message by a case-insensitive substring', () => {
		const f = { ...NO_FILTER, text: 'Crash' };
		expect(keep(f, line(1, 'a CRASHED here'))).toBe(true);
		expect(keep(f, line(1, 'all fine'))).toBe(false);
		// The tag is not searched: it has its own filter.
		expect(keep(f, line(1, 'all fine', { tag: 'Crash' }))).toBe(false);
	});

	it('requires every filter to match', () => {
		const f = { minPriority: 'error' as const, tags: ['T'], text: 'boom' };
		expect(keep(f, line(1, 'boom', { priority: 'error', tag: 'T' }))).toBe(true);
		expect(keep(f, line(1, 'boom', { priority: 'warn', tag: 'T' }))).toBe(false);
		expect(keep(f, line(1, 'boom', { priority: 'error', tag: 'U' }))).toBe(false);
		expect(keep(f, line(1, 'fine', { priority: 'error', tag: 'T' }))).toBe(false);
	});

	// Decision: a filter never hides that lines were lost or the stream ended.
	it('always shows markers', () => {
		const f = { minPriority: 'fatal' as const, tags: ['x'], text: 'nothing matches this' };
		for (const marker of ['dropped', 'gap', 'stopped', 'hidden'] as const) {
			expect(keep(f, { marker, text: 'lost' })).toBe(true);
		}
	});

	it('reads the tags field as a set of words', () => {
		expect(parseTags(' Net, Net  Cache,,')).toEqual(['Net', 'Cache']);
		expect(parseTags('')).toEqual([]);
	});

	it('knows when it shows everything', () => {
		expect(isOpen(NO_FILTER)).toBe(true);
		expect(isOpen({ ...NO_FILTER, text: 'a' })).toBe(false);
	});
});
