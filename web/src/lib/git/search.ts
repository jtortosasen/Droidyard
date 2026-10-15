// The History tab's search over the rows already loaded (plan m2n step 6): a case-insensitive
// text found in the subject or the author (name or e-mail), or a prefix of the SHA. It does
// not read the daemon; commits not loaded yet are not found.
import type { Commit } from './history';

/** The indices, into `commits`, of the commits that match `query`; none for an empty query. */
export function findCommits(commits: readonly Pick<Commit, 'oid' | 'subject' | 'authorName' | 'authorEmail'>[], query: string): number[] {
	const q = query.trim().toLowerCase();
	if (q === '') return [];
	const out: number[] = [];
	for (let i = 0; i < commits.length; i++) {
		const c = commits[i];
		if (c.oid.startsWith(q) || c.subject.toLowerCase().includes(q) || c.authorName.toLowerCase().includes(q) || c.authorEmail.toLowerCase().includes(q)) {
			out.push(i);
		}
	}
	return out;
}

/**
 * The match to go to from the row `from` (an index into the commits, or -1 for none): the next
 * one after it in the direction, wrapping round; the first (or last) when `from` is -1.
 * Returns the position in `matches`, or -1 when there are none.
 */
export function stepMatch(matches: readonly number[], from: number, direction: 1 | -1): number {
	if (matches.length === 0) return -1;
	if (direction === 1) {
		const k = matches.findIndex((m) => m > from);
		return k < 0 ? 0 : k;
	}
	for (let k = matches.length - 1; k >= 0; k--) if (matches[k] < from) return k;
	return matches.length - 1;
}
