import { describe, expect, it } from 'vitest';
import { Merge, filterRows, laneInput, rowKey, toggleRoot, type MixedRow, type TimedCommit } from './mixed';

const c = (oid: string, commitTime: number): TimedCommit => ({ oid, parents: [], commitTime });

/** Pages of a root: `sizes` commits per page, in order. */
function pagesOf(commits: TimedCommit[], size: number): TimedCommit[][] {
	const out: TimedCommit[][] = [];
	for (let i = 0; i < commits.length; i += size) out.push(commits.slice(i, i + size));
	return out.length === 0 ? [[]] : out;
}

/** Everything loaded at once: the merge the incremental runs must reach. */
function mergeAll(streams: Record<string, TimedCommit[]>): MixedRow[] {
	const roots = Object.keys(streams);
	const m = new Merge(roots);
	const out: MixedRow[] = [];
	for (const r of roots) out.push(...m.push(r, streams[r], false));
	return out;
}

// A small seeded generator, so a failing property case is reproducible.
function rng(seed: number): () => number {
	let s = seed >>> 0;
	return () => {
		s = (Math.imul(s, 1664525) + 1013904223) >>> 0;
		return s / 2 ** 32;
	};
}

/** A root's commits in its own order, with clocks that jump back and forth (skew). */
function skewed(root: string, n: number, rand: () => number): TimedCommit[] {
	let t = 1_000_000 + Math.floor(rand() * 5000);
	return Array.from({ length: n }, (_, i) => {
		t -= Math.floor(rand() * 40) - 10; // mostly older, sometimes newer than the commit before it
		return c(`${root}${i}`, t);
	});
}

describe('Merge', () => {
	// Decision (ADR-0071 §7): the roots' pages are merged by commit time into one log.
	it('interleaves two roots by commit time', () => {
		const rows = mergeAll({ '.': [c('a1', 50), c('a2', 30), c('a3', 10)], lib: [c('b1', 40), c('b2', 20)] });
		expect(rows.map((r) => r.key)).toEqual(['a1', 'lib:b1', 'a2', 'lib:b2', 'a3']);
		expect(rows.map((r) => r.root)).toEqual(['.', 'lib', '.', 'lib', '.']);
	});

	// Decision (ADR-0071 §7): the merge takes a stream's HEAD, so each root keeps its own order even when
	// a commit's clock is older than its parent's.
	it('keeps every root in its own order under clock skew', () => {
		// a2 is a child of a1 in git's order but carries a LATER time than a1; b1 sits between them.
		const rows = mergeAll({ '.': [c('a1', 10), c('a2', 90)], lib: [c('b1', 50)] });
		expect(rows.map((r) => r.key)).toEqual(['lib:b1', 'a1', 'a2']);
		const only = (root: string) => rows.filter((r) => r.root === root).map((r) => r.commit.oid);
		expect(only('.')).toEqual(['a1', 'a2']);
	});

	// Decision (ADR-0071 §7): property: with skewed clocks and any page split, the merge restricted to one root
	// is that root's sequence, nothing lost and nothing repeated.
	it('restricted to one root equals that root, over random skewed sequences', () => {
		for (let seed = 1; seed <= 200; seed++) {
			const rand = rng(seed);
			const roots = ['.', 'lib', 'lib/nested'].slice(0, 1 + Math.floor(rand() * 3));
			const streams: Record<string, TimedCommit[]> = {};
			for (const r of roots) streams[r] = skewed(r === '.' ? 'w' : r.replace('/', '_'), Math.floor(rand() * 30), rand);
			const full = mergeAll(streams);
			for (const r of roots) expect(full.filter((x) => x.root === r).map((x) => x.commit)).toEqual(streams[r]);

			// The same streams delivered in random pages, one page at a time in a random root order.
			const m = new Merge(roots);
			const pages = new Map(roots.map((r) => [r, pagesOf(streams[r], 1 + Math.floor(rand() * 6))]));
			const next = new Map(roots.map((r) => [r, 0]));
			const got: MixedRow[] = [];
			while (m.blocked().length > 0 || roots.some((r) => next.get(r)! < pages.get(r)!.length)) {
				const open = roots.filter((r) => next.get(r)! < pages.get(r)!.length);
				if (open.length === 0) break;
				const r = open[Math.floor(rand() * open.length)];
				const i = next.get(r)!;
				next.set(r, i + 1);
				got.push(...m.push(r, pages.get(r)![i], i + 1 < pages.get(r)!.length));
			}
			// Decision: no shown row moves when a page arrives: what was emitted after the pages is the full merge, row for row.
			expect(got.map((x) => x.key)).toEqual(full.map((x) => x.key));
		}
	});

	// Decision (ADR-0071 §7): one root can page while another is exhausted; an exhausted root never blocks.
	it('pages one root while another is exhausted', () => {
		const m = new Merge(['.', 'lib']);
		expect(m.blocked()).toEqual(['.', 'lib']);
		expect(m.push('lib', [c('b1', 70), c('b2', 60)], false)).toEqual([]); // the workspace has not arrived
		expect(m.blocked()).toEqual(['.']);
		const first = m.push('.', [c('a1', 100), c('a2', 80)], true);
		expect(first.map((r) => r.key)).toEqual(['a1', 'a2']); // lib's rows wait: the workspace's next page could be newer
		expect(m.blocked()).toEqual(['.']); // lib is out of pages; only the workspace is waited for
		const second = m.push('.', [c('a3', 50)], false);
		expect(second.map((r) => r.key)).toEqual(['lib:b1', 'lib:b2', 'a3']);
		expect(m.blocked()).toEqual([]);
	});

	// Decision (ADR-0071 §7): a row is emitted only when every root with pages left has a loaded head behind it.
	// This test was seen failing against a merge that emitted before the other roots' heads had loaded
	// (the `blocks` gate removed): the workspace's a1 (time 100) came out first, then lib's page, whose b1 (time 200)
	// would have to sit ABOVE it, so the page inserted above a shown row.
	it('emits nothing until every root with pages left has a loaded head', () => {
		const m = new Merge(['.', 'lib']);
		expect(m.push('.', [c('a1', 100)], false)).toEqual([]); // lib's first page is still on its way
		expect(m.blocked()).toEqual(['lib']);
		const out = m.push('lib', [c('b1', 200)], false);
		expect(out.map((r) => r.key)).toEqual(['lib:b1', 'a1']);
	});

	// Decision (ADR-0071 §7): a root whose page is drained but which has more pages blocks the rows behind it
	// from the other roots, so a later page never lands above them.
	it('holds back a root whose queue is empty with more to read', () => {
		const m = new Merge(['.', 'lib']);
		m.push('.', [c('a1', 100), c('a2', 10)], false);
		const first = m.push('lib', [c('b1', 90)], true);
		expect(first.map((r) => r.key)).toEqual(['a1', 'lib:b1']); // a2 waits: lib's next could be newer than 10
		expect(m.blocked()).toEqual(['lib']);
		const second = m.push('lib', [c('b2', 50)], false);
		expect(second.map((r) => r.key)).toEqual(['lib:b2', 'a2']);
	});

	// Decision (ADR-0071 §7): rows are keyed root:oid since a submodule may share an oid with its superproject.
	it('treats one oid in two roots as two rows', () => {
		const rows = mergeAll({ '.': [c('abc', 20)], lib: [c('abc', 10)] });
		expect(rows.map((r) => r.key)).toEqual(['abc', 'lib:abc']);
		expect(new Set(rows.map((r) => r.key)).size).toBe(2);
		// The lane layout is fed keys, so the two commits never share a lane.
		expect(rows.map((r) => laneInput(r).oid)).toEqual(['abc', 'lib:abc']);
	});

	it('keys the workspace by the bare oid and a submodule by root:oid', () => {
		expect(rowKey('.', 'abc')).toBe('abc');
		expect(rowKey('lib/nested', 'abc')).toBe('lib/nested:abc');
	});

	it('gives the lane layout keyed parents, in the commit own root', () => {
		const row: MixedRow = { root: 'lib', key: 'lib:b', commit: { oid: 'b', parents: ['p', 'q'], commitTime: 1 } };
		expect(laneInput(row)).toEqual({ oid: 'lib:b', parents: ['lib:p', 'lib:q'] });
	});

	it('breaks a time tie by the order of the roots', () => {
		const rows = mergeAll({ '.': [c('a1', 5)], lib: [c('b1', 5)] });
		expect(rows.map((r) => r.key)).toEqual(['a1', 'lib:b1']);
	});

	it('a root with no commits blocks nothing once read', () => {
		const m = new Merge(['.', 'lib']);
		m.push('lib', [], false);
		expect(m.push('.', [c('a1', 1)], false).map((r) => r.key)).toEqual(['a1']);
	});
});

describe('the root filter', () => {
	const rows = mergeAll({ '.': [c('a1', 50), c('a2', 30)], lib: [c('b1', 40)], 'lib/nested': [c('n1', 45)] });

	// Decision (ADR-0071 §7): the filter keeps the chosen roots' rows in the merged order; null is every root.
	it('keeps the chosen roots in merged order, null keeps all', () => {
		expect(filterRows(rows, null).map((r) => r.key)).toEqual(['a1', 'lib/nested:n1', 'lib:b1', 'a2']);
		expect(filterRows(rows, new Set(['lib'])).map((r) => r.key)).toEqual(['lib:b1']);
		expect(filterRows(rows, new Set(['lib', '.'])).map((r) => r.key)).toEqual(['a1', 'lib:b1', 'a2']);
		expect(filterRows(rows, new Set<string>())).toEqual([]);
	});

	// Decision (ADR-0071 §7): a filtered log equals that root's own sequence (git -C lib log).
	it('filtered to one root equals that root sequence', () => {
		expect(filterRows(rows, new Set(['.'])).map((r) => r.commit.oid)).toEqual(['a1', 'a2']);
	});

	it('toggles: from all a click chooses one root; a full or empty choice is all', () => {
		const all = ['.', 'lib', 'lib/nested'];
		expect(toggleRoot(null, 'lib', all)).toEqual(['lib']);
		expect(toggleRoot(['lib'], '.', all)).toEqual(['.', 'lib']);
		expect(toggleRoot(['.', 'lib'], 'lib', all)).toEqual(['.']);
		expect(toggleRoot(['lib'], 'lib', all)).toBeNull();
		expect(toggleRoot(['.', 'lib'], 'lib/nested', all)).toBeNull();
	});
});
