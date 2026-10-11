import { describe, expect, it } from 'vitest';
import { formatUptime, liveUptime } from './uptime';

describe('uptime', () => {
	// Decision: the shown uptime advances with the local clock from the moment the health
	// answer arrived; a value that stayed at the answered seconds would be the old bug.
	it('adds the whole seconds elapsed since the answer', () => {
		expect(liveUptime(100, 10_000, 10_000)).toBe(100);
		expect(liveUptime(100, 10_000, 13_999)).toBe(103);
		expect(liveUptime(100, 10_000, 70_000)).toBe(160);
	});

	// Decision: a clock set back does not make the daemon younger.
	it('never goes backwards', () => {
		expect(liveUptime(100, 10_000, 5_000)).toBe(100);
	});

	it('formats seconds, minutes and hours', () => {
		expect(formatUptime(59)).toBe('59 s');
		expect(formatUptime(61)).toBe('1 min 1 s');
		expect(formatUptime(3720)).toBe('1 h 2 min');
	});
});
