import { describe, expect, it } from 'vitest';
import { gradleChoice } from './gradleroot';

const err = { code: 'PROJECT_NO_GRADLE_ROOT', message: 'No Gradle build found under this project' };

describe('gradleChoice (ADR-0075 §7)', () => {
	// Decision: nothing is shown before the daemon has said anything.
	it('shows nothing before the first describe', () => {
		expect(gradleChoice(null)).toBeNull();
	});

	// Decision: one root with nothing else to choose is text, not a select.
	it('shows a lone root as fixed', () => {
		expect(gradleChoice({ root: '.', candidates: ['.'] })).toEqual({ kind: 'fixed', root: '.' });
		expect(gradleChoice({ root: 'app', candidates: [] })).toEqual({ kind: 'fixed', root: 'app' });
	});

	// Decision: several candidates and none chosen is a select with nothing chosen, and
	// the daemon's error travels with it.
	it('offers a select with no root chosen when there are several candidates and none resolved', () => {
		expect(gradleChoice({ candidates: ['dev', 'tools/x'], error: err })).toEqual({
			kind: 'select',
			root: undefined,
			options: ['dev', 'tools/x'],
			message: err.message,
		});
	});

	// Decision: a root with two candidates is still a select, with the root chosen; a root
	// named by hand that the listing lacks is an option too, so the select can show it.
	it('offers a select with the root chosen when there is more than one candidate', () => {
		expect(gradleChoice({ root: 'dev', candidates: ['dev', 'tools/x'] })).toEqual({
			kind: 'select',
			root: 'dev',
			options: ['dev', 'tools/x'],
			message: undefined,
		});
		expect(gradleChoice({ root: 'a/b/c', candidates: ['dev', 'tools/x'] })).toMatchObject({ kind: 'select', root: 'a/b/c', options: ['a/b/c', 'dev', 'tools/x'] });
	});

	// Decision: with no root and nothing to choose, the control is the error's message.
	it('shows the error message when there is no root and no candidate', () => {
		expect(gradleChoice({ candidates: [], error: err })).toEqual({ kind: 'none', message: err.message });
		expect(gradleChoice({ candidates: [] })).toMatchObject({ kind: 'none' });
	});
});
