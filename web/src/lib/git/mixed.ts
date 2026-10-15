// One log for every root (plan m2n P7; ADR-0071 §7, ADR-0070 §1): pure, no DOM, no Svelte.
//
// Each root (the workspace `.` and its initialised submodules) is its own repository with its own
// newest-first page stream. The browser merges the streams by commit time:
//   - the merge always takes a stream's HEAD (its next commit in git's own order), never a later
//     commit of it, so each root keeps its own order (children before parents), whatever the clocks
//     of the commits say; among the heads, the newest commit time goes first, and a tie goes to the
//     root listed first (the workspace is listed first), so the result is deterministic;
//   - a row is emitted only when EVERY root that has pages left has a loaded head behind it. A root
//     whose queue is empty while it has more pages (or whose first page has not arrived) BLOCKS the
//     merge: its next commit could be newer than any head loaded, and emitting before it arrives
//     would let that page insert above rows already shown. So a shown row never moves; the rows
//     emitted after any sequence of pages are a prefix of the rows emitted after all of them.
// `blocked()` names the roots whose next page the merge waits for: those are the pages to read.
//
// Rows, lanes, selection and expansion are keyed by `rowKey(root, oid)`, since a submodule may share
// an oid with its superproject (one oid in two roots is two rows). The workspace's key is the bare
// oid, so a repository without submodules keeps part 1's keys; a submodule's is `<root>:<oid>`.
// The two forms cannot collide: an oid has no colon.
import type { LaneInput } from './lanes';

/** The workspace root, as `git.submodules` and the `root` parameter say it. */
export const WORKSPACE_ROOT = '.';

/** The key of a commit of a root. */
export const rowKey = (root: string, oid: string): string => (root === WORKSPACE_ROOT ? oid : `${root}:${oid}`);

export interface TimedCommit {
	oid: string;
	parents: readonly string[];
	commitTime: number;
}

export interface MixedRow<C extends TimedCommit = TimedCommit> {
	root: string;
	/** `rowKey(root, commit.oid)`. */
	key: string;
	commit: C;
}

/** What the lane layout takes: the commit's key and its parents' keys (a parent is in the commit's own root). */
export function laneInput(row: MixedRow): LaneInput {
	return { oid: row.key, parents: row.commit.parents.map((p) => rowKey(row.root, p)) };
}

interface Stream<C> {
	queue: C[];
	/** The queue's next commit. */
	at: number;
	more: boolean;
	loaded: boolean;
}

export class Merge<C extends TimedCommit = TimedCommit> {
	readonly #order: string[];
	readonly #streams = new Map<string, Stream<C>>();

	/** `roots` in tie-break order. A root is blocking until its first page is pushed. */
	constructor(roots: readonly string[]) {
		this.#order = [...roots];
		for (const r of roots) this.#streams.set(r, { queue: [], at: 0, more: true, loaded: false });
	}

	/** A page of `root` (its commits in git's order); `more`: the root has pages after it. Returns the rows this page let out. */
	push(root: string, commits: readonly C[], more: boolean): MixedRow<C>[] {
		const s = this.#streams.get(root);
		if (!s) throw new Error(`mixed: unknown root ${root}`);
		if (s.at > 0 && s.at === s.queue.length) {
			s.queue = [];
			s.at = 0;
		}
		for (const c of commits) s.queue.push(c);
		s.more = more;
		s.loaded = true;
		return this.#drain();
	}

	/** The roots the merge waits for: no commit loaded behind the rows out, and more to read (or never read). */
	blocked(): string[] {
		return this.#order.filter((r) => this.#blocks(this.#streams.get(r)!));
	}

	#blocks(s: Stream<C>): boolean {
		return s.at >= s.queue.length && (s.more || !s.loaded);
	}

	#drain(): MixedRow<C>[] {
		const out: MixedRow<C>[] = [];
		for (;;) {
			let best: string | null = null;
			let bestTime = 0;
			for (const r of this.#order) {
				const s = this.#streams.get(r)!;
				if (this.#blocks(s)) return out;
				if (s.at >= s.queue.length) continue; // exhausted
				const t = s.queue[s.at].commitTime;
				if (best === null || t > bestTime) {
					best = r;
					bestTime = t;
				}
			}
			if (best === null) return out;
			const s = this.#streams.get(best)!;
			const commit = s.queue[s.at++];
			out.push({ root: best, key: rowKey(best, commit.oid), commit });
		}
	}
}

/** The rows of the roots chosen; null: every root. */
export function filterRows<C extends TimedCommit>(rows: readonly MixedRow<C>[], roots: ReadonlySet<string> | null): MixedRow<C>[] {
	return roots === null ? [...rows] : rows.filter((r) => roots.has(r.root));
}

/**
 * The filter after a click on `root`: from "all", the click chooses that root alone; from a choice it adds
 * the root or takes it out. A choice that comes to no root, or to every root, is "all" (null).
 */
export function toggleRoot(current: readonly string[] | null, root: string, all: readonly string[]): string[] | null {
	const base = current === null ? [] : [...current];
	const next = base.includes(root) ? base.filter((r) => r !== root) : [...base, root];
	const chosen = all.filter((r) => next.includes(r));
	return chosen.length === 0 || chosen.length >= all.length ? null : chosen;
}
