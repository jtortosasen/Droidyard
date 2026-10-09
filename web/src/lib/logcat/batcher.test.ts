import { describe, expect, it } from 'vitest';
import { Batcher } from './batcher';
import { FakeFrames } from './testing';

function make(opts: { hidden?: boolean; hiddenCap?: number } = {}) {
	const frames = new FakeFrames();
	const flushes: Array<{ items: number[]; dropped: number }> = [];
	const b = new Batcher<number>({
		frames,
		flush: (items, dropped) => flushes.push({ items, dropped }),
		...opts,
	});
	return { frames, flushes, b };
}

describe('Batcher', () => {
	// Decision: one flush per animation frame, however many events arrive in between.
	it('flushes once per frame whatever the number of pushes', () => {
		const { frames, flushes, b } = make();
		for (let i = 0; i < 100; i++) b.push([i, i + 1000]);
		expect(flushes).toHaveLength(0); // nothing moves before the frame
		expect(frames.requests).toBe(1); // and one frame was asked for
		frames.tick();
		expect(flushes).toHaveLength(1);
		expect(flushes[0]!.items).toHaveLength(200);
		expect(flushes[0]!.items.slice(0, 4)).toEqual([0, 1000, 1, 1001]); // in arrival order
		b.push([7]);
		frames.tick();
		expect(flushes).toHaveLength(2);
	});

	it('asks for no frame when nothing is queued', () => {
		const { frames, b } = make();
		b.push([]);
		expect(frames.requests).toBe(0);
	});

	// Decision: a hidden tab gets no frames, so the queue is capped, oldest dropped, and
	// the drops are counted and reported at the next flush.
	it('caps the queue while the tab is hidden and reports what it dropped', () => {
		const { frames, flushes, b } = make({ hidden: true, hiddenCap: 3 });
		b.push([1, 2]);
		b.push([3, 4, 5]);
		expect(b.queued).toBe(3);
		expect(b.dropped).toBe(2);
		b.setHidden(false);
		frames.tick();
		expect(flushes).toEqual([{ items: [3, 4, 5], dropped: 2 }]);
		b.push([6]);
		frames.tick();
		expect(flushes[1]).toEqual({ items: [6], dropped: 0 }); // reported once
		expect(b.dropped).toBe(2);
	});

	// Decision: the cap is for the hidden tab only; a visible tab drains every frame and
	// never drops.
	it('does not cap the queue while the tab is visible', () => {
		const { frames, flushes, b } = make({ hiddenCap: 3 });
		b.push([1, 2, 3, 4, 5, 6, 7, 8, 9, 10]);
		frames.tick();
		expect(flushes[0]!.items).toHaveLength(10);
		expect(b.dropped).toBe(0);
	});

	it('caps what was already queued when the tab becomes hidden', () => {
		const { b } = make({ hiddenCap: 2 });
		b.push([1, 2, 3, 4]);
		expect(b.queued).toBe(4);
		b.setHidden(true);
		expect(b.queued).toBe(2);
		expect(b.dropped).toBe(2);
	});

	// Decision: clear forgets the queue, so cleared lines do not come back with the frame.
	it('forgets the queue and the pending frame on reset', () => {
		const { frames, flushes, b } = make();
		b.push([1, 2]);
		b.reset();
		expect(frames.pending).toBe(0);
		frames.tick();
		expect(flushes).toHaveLength(0);
		expect(b.queued).toBe(0);
	});
});
