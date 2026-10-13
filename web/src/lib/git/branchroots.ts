// Which roots have a branch (plan m2n R2; ADR-0074 §3), pure: the union of the branches of every loaded root, each
// once by name (a remote-tracking branch by `remote/name`) with the roots that hold it, in the order of the log's roots,
// which is also the order of their stripes. The side panel marks each branch with those roots; a checkout or a new
// branch from a branch is applied to them only. A root whose refs are not read yet holds nothing for now.
// Every name here is git's text.
import type { RefsResult } from './refs';
import { WORKSPACE_ROOT } from './mixed';

/** One root's copy of a branch. */
export interface Holder {
	root: string;
	/** The commit the branch is at in that root. */
	oid: string;
	/** The branch is checked out in that root (a local branch that is its HEAD). */
	current: boolean;
}

export interface UnionBranch {
	/** `feature/x`, or `origin/feature/x` for a remote-tracking branch. */
	name: string;
	/** Remote-tracking branches only: the remote and the branch's name on it. */
	remote?: string;
	branch?: string;
	/** The roots that have it, in the order of the log's roots (the workspace first). */
	holders: Holder[];
}

export interface BranchUnion {
	locals: Map<string, UnionBranch>;
	remotes: Map<string, UnionBranch>;
}

/**
 * The union of the branches of `roots` (the log's roots, workspace first): the workspace's from `workspace`, a
 * submodule's from `rootRefs`. A remote's `HEAD` pointer is not a branch. One pass over every root's refs, so it costs
 * the number of refs, not of refs times roots.
 */
export function branchUnion(roots: readonly string[], workspace: RefsResult | null, rootRefs: ReadonlyMap<string, RefsResult>): BranchUnion {
	const locals = new Map<string, UnionBranch>();
	const remotes = new Map<string, UnionBranch>();
	for (const root of roots) {
		const refs = root === WORKSPACE_ROOT ? workspace : rootRefs.get(root);
		if (refs === null || refs === undefined) continue;
		const current = refs.head.detached ? undefined : refs.head.branch;
		for (const b of refs.branches) {
			const entry = locals.get(b.name) ?? { name: b.name, holders: [] };
			entry.holders.push({ root, oid: b.oid, current: b.name === current });
			locals.set(b.name, entry);
		}
		for (const r of refs.remotes) {
			if (r.name === 'HEAD') continue;
			const name = `${r.remote}/${r.name}`;
			const entry = remotes.get(name) ?? { name, remote: r.remote, branch: r.name, holders: [] };
			entry.holders.push({ root, oid: r.oid, current: false });
			remotes.set(name, entry);
		}
	}
	return { locals, remotes };
}

/** The root whose commit a branch's row selects: the workspace when it has the branch, else the first root that does. */
export function firstHolder(entry: UnionBranch): Holder {
	return entry.holders.find((h) => h.root === WORKSPACE_ROOT) ?? entry.holders[0];
}

/** Whether the workspace has the branch. */
export const inWorkspace = (entry: UnionBranch): boolean => entry.holders.some((h) => h.root === WORKSPACE_ROOT);
