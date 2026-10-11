import { describe, expect, it } from 'vitest';
import { dragStarts, dropMode, dropTarget, edgeScroll, hoverStep } from './drag';

describe('drag', () => {
	// Decision (D53): a drag starts after 4 px with the primary button, not before, not with another.
	it('starts at 4 px and not at 3', () => {
		expect(dragStarts({ x: 0, y: 0 }, { x: 3, y: 0 }, 1)).toBe(false);
		expect(dragStarts({ x: 0, y: 0 }, { x: 4, y: 0 }, 1)).toBe(true);
		expect(dragStarts({ x: 0, y: 0 }, { x: 40, y: 0 }, 2)).toBe(false);
	});

	// Decision (D53): the target is the folder under the pointer, or a file row's parent.
	it('targets a folder, or a file\'s parent', () => {
		expect(dropTarget({ path: 'res', dir: true }, ['a.kt'])).toBe('res');
		expect(dropTarget({ path: 'res/x.xml', dir: false }, ['a.kt'])).toBe('res');
		expect(dropTarget(null, ['res/x.xml'])).toBe('');
	});

	// Decision (D53): a folder into itself or a descendant, and anything into its current parent, is no drop;
	// a sibling sharing the prefix is a drop.
	it('refuses a folder onto its descendant, itself and its own parent', () => {
		expect(dropTarget({ path: 'a/b', dir: true }, ['a'])).toBeNull();
		expect(dropTarget({ path: 'a', dir: true }, ['a'])).toBeNull();
		expect(dropTarget({ path: 'a', dir: true }, ['a/b'])).toBeNull();
		expect(dropTarget({ path: 'ab', dir: true }, ['a'])).toBe('ab');
	});

	// Decision (D53): ⌥ held at the drop copies.
	it('copies with alt', () => {
		expect(dropMode(true)).toBe('copy');
		expect(dropMode(false)).toBe('move');
	});

	// Decision (D53): a collapsed folder expands after 600 ms under the pointer, once; leaving resets the timer.
	it('expands at 600 ms and not at 599, once', () => {
		let r = hoverStep(null, 'a', 1000);
		expect(r.expand).toBe(false);
		r = hoverStep(r.state, 'a', 1599);
		expect(r.expand).toBe(false);
		r = hoverStep(r.state, 'a', 1600);
		expect(r.expand).toBe(true);
		expect(hoverStep(r.state, 'a', 2600).expand).toBe(false);
		const left = hoverStep(r.state, null, 1700);
		expect(left.state).toBeNull();
		expect(hoverStep(hoverStep(null, 'b', 0).state, 'a', 700).expand).toBe(false);
	});

	// Decision (D53): within 24 px of the top or bottom the list scrolls that way.
	it('scrolls near the edges only', () => {
		expect(edgeScroll(110, 100, 500)).toBeLessThan(0);
		expect(edgeScroll(490, 100, 500)).toBeGreaterThan(0);
		expect(edgeScroll(300, 100, 500)).toBe(0);
		expect(edgeScroll(124, 100, 500)).toBe(0);
	});
});
