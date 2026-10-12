import { describe, expect, it } from 'vitest';
import { findCommits, stepMatch } from './search';

const c = (oid: string, subject: string, authorName = 'Ada Lovelace', authorEmail = 'ada@example.org') => ({ oid, subject, authorName, authorEmail });
const commits = [
	c('abc1230000', 'Fix the login crash'),
	c('def4560000', 'Add a settings screen', 'Grace Hopper', 'grace@navy.mil'),
	c('abd7890000', 'Bump dependencies'),
];

describe('findCommits', () => {
	// Decision (plan step 6): the search is over subject, author and a SHA prefix, case-insensitive, and not over what is not loaded.
	it('finds by subject, author name, e-mail and SHA prefix', () => {
		expect(findCommits(commits, 'LOGIN')).toEqual([0]);
		expect(findCommits(commits, 'hopper')).toEqual([1]);
		expect(findCommits(commits, 'navy.mil')).toEqual([1]);
		expect(findCommits(commits, 'abd78')).toEqual([2]);
		expect(findCommits(commits, 'ab')).toEqual([0, 2]); // the SHA prefix of two commits; "ab" is in no subject
		expect(findCommits(commits, '  ')).toEqual([]);
		expect(findCommits(commits, 'c123')).toEqual([]); // a SHA match is a prefix, not a substring
	});
});

describe('stepMatch', () => {
	// Decision: next and previous go round the matches from the selected row.
	it('goes to the next and previous match, wrapping round', () => {
		const m = [1, 4, 9];
		expect(stepMatch(m, -1, 1)).toBe(0);
		expect(stepMatch(m, -1, -1)).toBe(2);
		expect(stepMatch(m, 1, 1)).toBe(1);
		expect(stepMatch(m, 9, 1)).toBe(0);
		expect(stepMatch(m, 4, -1)).toBe(0);
		expect(stepMatch(m, 1, -1)).toBe(2);
		expect(stepMatch(m, 5, 1)).toBe(2);
		expect(stepMatch([], 3, 1)).toBe(-1);
	});
});
