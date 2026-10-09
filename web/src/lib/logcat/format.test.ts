import { describe, expect, it } from 'vitest';
import { clockTime, formatLine, priorityClass } from './format';
import { line } from './testing';

describe('formatLine', () => {
	it('puts the time, ids, priority, tag and message in one row', () => {
		const l = line(123456, 'hello', { pid: 7, tid: 8, priority: 'warn', tag: 'Net' });
		expect(formatLine(l)).toBe('12:00:00.123 7 8 W Net: hello');
	});

	// Decision (D5): device text is data. Markup in a field comes out as the same
	// characters, for the text node that renders it; this module builds no markup.
	it('keeps device markup as characters', () => {
		const l = line(1, '<img src=x onerror=alert(1)> <a href="javascript:0">', { tag: '<b>' });
		expect(formatLine(l)).toContain('<b>: <img src=x onerror=alert(1)> <a href="javascript:0">');
	});

	it('gives a class only for a known priority', () => {
		expect(priorityClass('error')).toBe('p-error');
		expect(priorityClass('evil" onclick="x' as never)).toBe('');
	});

	it('shows an unexpected time as it came', () => {
		expect(clockTime('later')).toBe('later');
	});
});
