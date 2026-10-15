// What the History tab's branch entries call (plan m2n P3; ADR-0071 §1, §5), pure:
//   - which enabled entry of the branch menu does what for a local or a remote-tracking branch:
//     Checkout is one `git.checkout`, New branch from opens the dialog at that branch's commit;
//     Compare with current (P9, ADR-0072) opens the comparison view, and every other entry does
//     nothing here (the rest wait for a later milestone);
//   - the toolbar's Create Branch, from HEAD;
//   - the parameters of the two calls: a remote-tracking branch is checked out as
//     `{name, remote}` (the daemon creates the local branch tracking it, or switches to the
//     local one of that name), and proposes its name without the remote for a new branch;
//   - the synchronised branches (P8, R2; ADR-0071 §6 as ADR-0074 §3 changed it): with initialised submodules a checkout
//     and a new branch can be applied to them too, to the roots that have the branch only. `checkoutOffer` and
//     `newBranchOffer` say which roots have it (they move) and which do not (they keep theirs), what each root's
//     start is (that branch's oid there) and whether the toggle starts on: when a submodule has it. `callRoots` is
//     the `roots` of the call: the workspace only when it has the branch;
//   - the dirty-buffer gate (ADR-0071 §5): the editor's unsaved files block a checkout, and a new
//     branch that is checked out, before anything is called. The daemon would rewrite the files
//     under the buffer; refusing first loses nothing. A new branch that is not checked out
//     touches no file, so it is not blocked.
// Every name here is git's text: the module builds parameters and messages, never markup.
import type { Params, Result } from '../api/capabilities.gen';
import type { MenuId } from './controls';
import type { RefsHead } from './refs';

/**
 * The branch a menu was opened on. A remote-tracking branch is the remote's name and its own. `onlyIn` is set when the
 * workspace lacks the branch (plan m2n R2, ADR-0074 §3): the submodule root whose tip `oid` is.
 */
export type BranchTarget =
	| { kind: 'local'; name: string; oid?: string; onlyIn?: string }
	| { kind: 'remote'; remote: string; name: string; oid?: string; onlyIn?: string };

/** Where a new branch starts: a branch of the menu, or HEAD. */
export interface NewBranchSource {
	/** The toolbar's Create Branch: made in every initialised root, a root without the branch starting at its own HEAD. */
	fromHead?: boolean;
	/** The workspace lacks the branch the start is; this submodule root has it (the oid is its tip). */
	onlyIn?: string;
	/** What the dialog shows: the branch (origin/feature/x), or HEAD with its branch. */
	label: string;
	/** The full object id sent as `from`. */
	oid: string;
	/** The name the dialog starts with: a remote-tracking branch's own name, else empty. */
	proposed: string;
	/** The branch the start is, to look for in each submodule; absent when HEAD is detached (nothing to look for). */
	branch?: BranchTarget;
}

/** What an enabled entry does: a call now, or a dialog first. */
export type EntryAction =
	| { kind: 'checkout'; target: BranchTarget; params: Params<'git.checkout'> }
	| { kind: 'new-branch'; source: NewBranchSource }
	| { kind: 'compare'; target: BranchTarget };

/** The text of a target as the list shows it: `feature/x`, `origin/feature/x`. */
export function targetLabel(t: BranchTarget): string {
	return t.kind === 'local' ? t.name : `${t.remote}/${t.name}`;
}

/** The workspace's own repository, the first root of a call. */
const SELF = '.';

/** The `roots` of a call as the daemon takes them; absent for the workspace alone, which is what an absent `roots` means. */
function rootsArg(roots: readonly string[] | undefined): { roots?: string[] } {
	if (roots === undefined || roots.length === 0 || (roots.length === 1 && roots[0] === SELF)) return {};
	return { roots: [...roots] };
}

/** `roots` is every root the checkout is applied to (see `callRoots`): the workspace only when it has the branch. */
export function checkoutParams(t: BranchTarget, roots?: readonly string[]): Params<'git.checkout'> {
	return { ...(t.kind === 'local' ? { name: t.name } : { name: t.name, remote: t.remote }), ...rootsArg(roots) };
}

/** The roots a new branch is made in (P8; R2) and, for each submodule among them, the oid it starts at there (Q6 (1)'s `fromIn`). */
export interface NewBranchSync {
	/** Every root of the call, as `callRoots` gives them. */
	roots: readonly string[];
	starts: ReadonlyMap<string, string>;
}

export function createParams(name: string, checkout: boolean, from: string, sync?: NewBranchSync): Params<'git.createBranch'> {
	const roots = rootsArg(sync?.roots);
	if (sync === undefined || roots.roots === undefined) return { name, checkout, from };
	const fromIn: Record<string, string> = {};
	for (const root of roots.roots) {
		const oid = root === SELF ? undefined : sync.starts.get(root);
		if (oid !== undefined) fromIn[root] = oid;
	}
	return { name, checkout, from, ...roots, fromIn };
}

type RootRefs = ReadonlyMap<string, Result<'git.refs'>>;
type Refs = Result<'git.refs'>;

/** The tip of a branch in a root's refs, if the root has it; a local branch by name, a remote-tracking one by remote and name. */
function branchOidIn(refs: Refs | undefined | null, t: BranchTarget): string | undefined {
	if (refs === undefined || refs === null) return undefined;
	const found = t.kind === 'local' ? refs.branches.find((b) => b.name === t.name) : refs.remotes.find((r) => r.remote === t.remote && r.name === t.name);
	return found?.oid === '' ? undefined : found?.oid;
}

/**
 * What a synchronised call could apply to (ADR-0074 §3): the initialised submodules split into those that have the
 * branch (the call moves them) and those that do not (they keep their branch), and whether the workspace has it.
 */
export interface SyncOffer {
	/** The initialised submodule roots, as the list gives them. */
	roots: readonly string[];
	/** The submodule roots that have the branch, in the order of `roots`: what Synchronise adds to the call. */
	moves: readonly string[];
	/** The submodule roots whose refs are read and lack the branch: they keep theirs. */
	stays: readonly string[];
	/** The submodule roots whose refs are not read yet: they keep theirs until they are. */
	pending: readonly string[];
	/** The roots of `moves` that lack the branch and start at their own HEAD instead (the toolbar's Create Branch only). */
	atHead: readonly string[];
	/** Per root in `moves`, the oid the branch has there: a new branch's start in that root. */
	starts: ReadonlyMap<string, string>;
	/** The workspace has the branch, so it moves whatever Synchronise says. */
	workspaceHas: boolean;
	/** The toggle starts on when a submodule has the branch (ADR-0074 §3). */
	defaultOn: boolean;
	/** The workspace lacks the branch: only submodules can move, so the toggle is on and cannot be turned off. */
	forced: boolean;
	/** The branch looked for, as the list shows it; empty when there is none (a detached HEAD). */
	branch: string;
}

function offerOf(
	branch: BranchTarget | undefined,
	workspaceHas: boolean,
	roots: readonly string[],
	rootRefs: RootRefs,
	startIn: (refs: Refs) => string | undefined,
	headIn?: (refs: Refs) => string | undefined,
): SyncOffer | null {
	if (roots.length === 0) return null;
	const atHead: string[] = [];
	const starts = new Map<string, string>();
	const moves: string[] = [];
	const stays: string[] = [];
	const pending: string[] = [];
	for (const root of roots) {
		const refs = rootRefs.get(root);
		if (refs === undefined) {
			pending.push(root);
			continue;
		}
		let oid = startIn(refs);
		if (oid === undefined && headIn !== undefined) {
			oid = headIn(refs);
			if (oid !== undefined) atHead.push(root);
		}
		if (oid === undefined) stays.push(root);
		else {
			moves.push(root);
			starts.set(root, oid);
		}
	}
	return { roots, moves, stays, pending, atHead, starts, workspaceHas, defaultOn: moves.length > 0, forced: !workspaceHas, branch: branch === undefined ? '' : targetLabel(branch) };
}

/**
 * The offer for a checkout of `target`: a root has the branch when it has that local branch or, for a remote-tracking
 * target, that remote-tracking branch (the daemon makes the local one there). `workspace` is the project's own refs
 * (null: not read, taken to have it). Null with no submodule.
 */
export function checkoutOffer(target: BranchTarget, roots: readonly string[], rootRefs: RootRefs, workspace: Refs | null = null): SyncOffer | null {
	const has = (refs: Refs | null | undefined) => branchOidIn(refs, target);
	return offerOf(target, workspace === null || has(workspace) !== undefined, roots, rootRefs, has);
}

/**
 * Whether the call is applied to the submodules that have the branch: what the person chose (`choice`, null until they
 * touch the toggle) or the offer's default, which follows the refs of the submodules as they arrive. A workspace without
 * the branch forces it on.
 */
export function syncOn(choice: boolean | null, offer: SyncOffer | null): boolean {
	return offer !== null && (offer.forced || (choice ?? offer.defaultOn));
}

/** Every root a call is made in: the workspace when it has the branch, then (Synchronise on) the submodules that have it. */
export function callRoots(offer: SyncOffer | null, sync: boolean): string[] {
	if (offer === null) return [SELF];
	return [...(offer.workspaceHas ? [SELF] : []), ...(sync ? offer.moves : [])];
}

/**
 * The offer for a new branch from `source`: a root has it when it has the branch the start is (the menu's), and that
 * branch's oid there is the root's start. The toolbar's Create Branch (from HEAD, ADR-0074 §3) is made in every
 * initialised root: at that branch's oid where the root has it, else at the root's own HEAD. Null with no submodule.
 */
export function newBranchOffer(source: NewBranchSource, roots: readonly string[], rootRefs: RootRefs, workspace: Refs | null = null): SyncOffer | null {
	if (source.fromHead === true) {
		return offerOf(
			source.branch,
			true,
			roots,
			rootRefs,
			(refs) => (source.branch === undefined ? undefined : branchOidIn(refs, source.branch)),
			(refs) => (refs.head.oid === undefined || refs.head.oid === '' ? undefined : refs.head.oid),
		);
	}
	const has = (refs: Refs | null | undefined) => (source.branch === undefined ? undefined : branchOidIn(refs, source.branch));
	return offerOf(source.branch, workspace === null ? source.onlyIn === undefined : has(workspace) !== undefined, roots, rootRefs, has);
}

/** The start of a new branch made from a branch of the menu; null when the ref has no commit. */
export function sourceOf(t: BranchTarget): NewBranchSource | null {
	if (t.oid === undefined || t.oid === '') return null;
	return { label: targetLabel(t), oid: t.oid, proposed: t.kind === 'remote' ? t.name : '', branch: t, ...(t.onlyIn !== undefined ? { onlyIn: t.onlyIn } : {}) };
}

/** The start of the toolbar's Create Branch: HEAD; null until the refs are read, or while HEAD has no commit. */
export function headSource(head: RefsHead | null): NewBranchSource | null {
	if (head === null || head.oid === undefined) return null;
	if (head.detached || head.branch === undefined) return { label: 'HEAD (detached)', oid: head.oid, proposed: '', fromHead: true };
	return { label: `HEAD (${head.branch})`, oid: head.oid, proposed: '', branch: { kind: 'local', name: head.branch }, fromHead: true };
}

/** A remote's `HEAD` is a pointer to its default branch, not a branch: nothing is checked out or branched from it. */
export function isRemoteHead(t: BranchTarget): boolean {
	return t.kind === 'remote' && t.name === 'HEAD';
}

/** What an entry of the branch menu does for `target`; null for the entries that do nothing here. */
export function entryAction(id: MenuId, target: BranchTarget): EntryAction | null {
	if (isRemoteHead(target)) return null;
	switch (id) {
		case 'checkout':
			return { kind: 'checkout', target, params: checkoutParams(target) };
		case 'new-branch-from': {
			const source = sourceOf(target);
			return source === null ? null : { kind: 'new-branch', source };
		}
		case 'compare':
			return canCompare(target, null) ? { kind: 'compare', target } : null;
		default:
			return null;
	}
}

/**
 * Whether Compare with current is offered for `target` (ADR-0072): a local or remote-tracking branch that has a commit and is
 * not the current branch. `head` is HEAD as the refs read it; null when they are not read (the target alone is judged).
 */
export function canCompare(target: BranchTarget, head: RefsHead | null): boolean {
	if (isRemoteHead(target) || target.oid === undefined || target.oid === '') return false;
	// The comparison is of the workspace's commits: a branch only a submodule has is not in it (ADR-0074 §3).
	if (target.onlyIn !== undefined) return false;
	if (head === null) return true;
	if (head.oid === undefined || head.oid === '') return false;
	return !(target.kind === 'local' && !head.detached && head.branch === target.name);
}

/** What Compare with current opens with (`CompareModel.open`). */
export interface CompareOpening {
	/** The branch compared, as the list shows it. */
	branch: string;
	/** The current branch, or "HEAD" when it is detached. */
	current: string;
	/** The branch's tip: the oid of the ref the menu was opened on. */
	tip: string;
	/** HEAD's commit. */
	head: string;
}

/** The comparison of `target` with the current branch (ADR-0072); null when it is not offered or the refs are not read. */
export function compareOpening(target: BranchTarget, head: RefsHead | null): CompareOpening | null {
	if (head === null || !canCompare(target, head) || head.oid === undefined || target.oid === undefined) return null;
	return { branch: targetLabel(target), current: head.detached || head.branch === undefined ? 'HEAD' : head.branch, tip: target.oid, head: head.oid };
}

/** The editor's source of unsaved files (editor/unsaved.ts's UnsavedSource, what the gate reads). */
export interface DirtySource {
	dirtyPaths(): string[];
}

/**
 * The files whose unsaved text would meet the call: every dirty buffer when the call rewrites the
 * work tree (a checkout, or a new branch that is checked out), none when it does not.
 */
export function dirtyBlock(source: DirtySource, rewritesWorkTree: boolean): string[] {
	return rewritesWorkTree ? [...source.dirtyPaths()] : [];
}

/** The refusal the block shows, as text. */
export function blockText(paths: readonly string[]): string {
	return `Save or close ${paths.length === 1 ? 'this file' : 'these files'} first: ${paths.length === 1 ? 'it has' : `${paths.length} files have`} unsaved changes that the checkout would overwrite.`;
}

/** Why the dialog's name cannot be used, from what the list holds already; null when it can be tried. */
export function nameProblem(name: string, locals: ReadonlySet<string>): string | null {
	const n = name.trim();
	if (n === '') return 'Enter a name for the branch.';
	if (locals.has(n)) return `A branch named ${n} already exists.`;
	return null;
}
