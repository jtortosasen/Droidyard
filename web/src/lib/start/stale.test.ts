import { describe, expect, it } from 'vitest';
import { StaleSession } from './stale';

describe('StaleSession', () => {
	// Decision (D7, verifier): a restarted daemon must not take unsaved buffers with it. Seen
	// failing when the decision ignores the guard (always 'solo', today's behaviour).
	it('keeps the project under a banner when the token is refused with unsaved text', () => {
		const state = { dirty: true };
		const s = new StaleSession({ hasDirty: () => state.dirty });
		expect(s.decide('open')).toBe('none');
		expect(s.decide('unauthorized')).toBe('banner');
		// The project's models went away or were emptied: the mode is not re-decided on the way.
		state.dirty = false;
		expect(s.decide('unauthorized')).toBe('banner');
	});

	it('replaces the page with the notice when nothing is unsaved, as before', () => {
		const state = { dirty: false };
		const s = new StaleSession({ hasDirty: () => state.dirty });
		expect(s.decide('unauthorized')).toBe('solo');
		// Once it is the notice alone, the project is gone and stays gone.
		state.dirty = true;
		expect(s.decide('unauthorized')).toBe('solo');
	});

	it('forgets the decision when the connection recovers', () => {
		const state = { dirty: true };
		const s = new StaleSession({ hasDirty: () => state.dirty });
		expect(s.decide('unauthorized')).toBe('banner');
		expect(s.decide('connecting')).toBe('none');
		state.dirty = false;
		expect(s.decide('unauthorized')).toBe('solo');
	});
});
