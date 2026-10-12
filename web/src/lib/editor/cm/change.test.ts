import { describe, expect, it } from 'vitest';
import { minimalChange } from './change';

function apply(a: string, c: { from: number; to: number; insert: string } | null): string {
	return c === null ? a : a.slice(0, c.from) + c.insert + a.slice(c.to);
}

describe('minimalChange', () => {
	// Decision (step 3 verifier): an edit near the end of a file replaces only that span, so a
	// view scrolled above it keeps its place; a whole-text replacement maps it to 0.
	it('replaces only the differing middle', () => {
		const a = 'line 1\nline 2\nline 3\n';
		const b = 'line 1\nline two\nline 3\n';
		const c = minimalChange(a, b);
		expect(c).toEqual({ from: 12, to: 13, insert: 'two' });
		expect(apply(a, c)).toBe(b);
	});

	it('is null for equal texts and handles growth, shrink and overlap', () => {
		expect(minimalChange('same', 'same')).toBeNull();
		for (const [a, b] of [
			['', 'abc'],
			['abc', ''],
			['aaa', 'aaaa'],
			['abab', 'ab'],
			['x\r\ny', 'x\ny'],
			['prefix tail', 'prefix new tail'],
		] as const) {
			const c = minimalChange(a, b);
			expect(apply(a, c)).toBe(b);
			expect(c!.from).toBeLessThanOrEqual(c!.to);
		}
	});
});
