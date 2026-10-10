import { describe, expect, it } from 'vitest';
import { failureShort, failureText } from './failure';

describe('failureShort', () => {
	// Decision (m2d step 2): the toolbar shows `Failed: CODE` and never the daemon's long
	// message; the full text stays available through failureText.
	it('is the code only, and keeps the message out of the toolbar', () => {
		const f = { code: 'WORKSPACE_UNTRUSTED', message: 'a very long explanation', nextStep: 'trust it' };
		expect(failureShort(f)).toBe('Failed: WORKSPACE_UNTRUSTED');
		expect(failureShort(f)).not.toContain('explanation');
		expect(failureText(f)).toContain('a very long explanation');
		expect(failureShort({ message: 'connection lost' })).toBe('Failed');
	});
});
