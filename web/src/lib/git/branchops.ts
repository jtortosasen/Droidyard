// What the History tab's branch entries call (plan m2n P3; ADR-0071 §1, §5), pure:
//   - which enabled entry of the branch menu does what for a local or a remote-tracking branch:
//     Checkout is one `git.checkout`, New branch from opens the dialog at that branch's commit;
//     Compare with current (P9, ADR-0072) opens the comparison view, and every other entry does
//     nothing here (the rest wait for a later milestone);
//   - the toolbar's Create Branch, from HEAD;
//   - the parameters of the two calls: a remote-tracking branch is checked out as
//     `{name, remote}` (the daemon creates the local branch tracking it, or switches to the
//     local one of that name), and proposes its name without the remote for a new branch;
//   - the synchronised branches (P8; ADR-0071 §6): with initialised submodules a checkout and a new branch can be
//     applied to them too. `checkoutOffer` and `newBranchOffer` say which roots have the branch, what each root's
//     start is (that branch's oid there) and whether the toggle starts on: when every initialised root has it;
//   - the dirty-buffer gate (ADR-0071 §5): the editor's unsaved files block a checkout, and a new
//     branch that is checked out, before anything is called. The daemon would rewrite the files
//     under the buffer; refusing first loses nothing. A new branch that is not checked out
//     touches no file, so it is not blocked.
// Every name here is git's text: the module builds parameters and messages, never markup.
import type { Params, Result } from '../api/capabilities.gen';
import type { MenuId } from './controls';
import type { RefsHead } from './refs';

/** The branch a menu was opened on. A remote-tracking branch is the remote's name and its own. */
export type BranchTarget =
	| { kind: 'local'; name: string; oid?: string }
	| { kind: 'remote'; remote: string; name: string; oid?: string };

/** Where a new branch starts: a branch of the menu, or HEAD. */
export interface NewBranchSource {
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

/** `.` and then the submodule roots to apply a call to, as the daemon takes them; absent for the workspace alone. */
function rootsOf(sync: readonly string[]): { roots?: string[] } {
	return sync.length === 0 ? {} : { roots: [SELF, ...sync] };
}

export function checkoutParams(t: BranchTarget, sync: readonly string[] = []): Params<'git.checkout'> {
	return { ...(t.kind === 'local' ? { name: t.name } : { name: t.name, remote: t.remote }), ...rootsOf(sync) };
}

/** The roots a new branch is also made in (P8): every one with the oid it starts at there (Q6 (1)'s `fromIn`). */
export interface NewBranchSync {
	roots: readonly string[];
	starts: ReadonlyMap<string, string>;
}

export function createParams(name: string, checkout: boolean, from: string, sync?: NewBranchSync): Params<'git.createBranch'> {
	if (sync === undefined || sync.roots.length === 0) return { name, checkout, from };
	const fromIn: Record<string, string> = {};
	for (const root of sync.roots) {
		const oid = sync.starts.get(root);
		if (oid !== undefined) fromIn[root] = oid;
	}
	return { name, checkout, from, ...rootsOf(sync.roots), fromIn };
}

type RootRefs = ReadonlyMap<string, Result<'git.refs'>>;

/** The commit of a branch in a root's refs, if the root has it; a local branch by name, a remote-tracking one by remote and name. */
function branchOidIn(refs: Result<'git.refs'> | undefined, t: BranchTarget): string | undefined {
	if (refs === undefined) return undefined;
	const found = t.kind === 'local' ? refs.branches.find((b) => b.name === t.name) : refs.remotes.find((r) => r.remote === t.remote && r.name === t.name);
	return found?.oid === '' ? undefined : found?.oid;
}

/** What a synchronised call could apply to: the initialised submodule roots, which have the branch, and the toggle's default. */
export interface SyncOffer {
	/** The initialised submodule roots, as the list gives them. */
	roots: readonly string[];
	/** Per root that has the branch, the oid that branch has there: a new branch's start in that root. */
	starts: ReadonlyMap<string, string>;
	/** The roots that lack the branch, or whose refs are not read yet. */
	missing: readonly string[];
	/** The toggle starts on when the branch is known and every root has it (ADR-0071 §6). */
	defaultOn: boolean;
	/** The branch looked for, as the list shows it; empty when there is none (a detached HEAD). */
	branch: string;
}

function offerOf(branch: BranchTarget | undefined, roots: readonly string[], rootRefs: RootRefs, has: (refs: Result<'git.refs'> | undefined, t: BranchTarget) => string | undefined): SyncOffer | null {
	if (roots.length === 0) return null;
	const starts = new Map<string, string>();
	const missing: string[] = [];
	for (const root of roots) {
		const oid = branch === undefined ? undefined : has(rootRefs.get(root), branch);
		if (oid === undefined) missing.push(root);
		else starts.set(root, oid);
	}
	return { roots, starts, missing, defaultOn: branch !== undefined && missing.length === 0, branch: branch === undefined ? '' : targetLabel(branch) };
}

/**
 * The offer for a checkout of `target`: a root has the branch when it has the local branch of that name or, for a
 * remote-tracking target, that remote's branch (the daemon makes the local one there). A remote-tracking target also
 * needs the remote in the root, since the daemon refuses a root whose remotes lack it: seen from the refs, a remote
 * is there when the root has any branch of it, so a remote with none counts as absent (the toggle starts off; the
 * daemon would have accepted it). Null with no submodule.
 */
export function checkoutOffer(target: BranchTarget, roots: readonly string[], rootRefs: RootRefs): SyncOffer | null {
	const local: BranchTarget = { kind: 'local', name: target.name };
	return offerOf(target, roots, rootRefs, (refs, t) => {
		if (t.kind === 'remote' && refs?.remotes.some((r) => r.remote === t.remote) !== true) return undefined;
		return branchOidIn(refs, local) ?? (t.kind === 'remote' ? branchOidIn(refs, t) : undefined);
	});
}

/**
 * Whether the dialog applies the call to the submodules: what the person chose, and until they touch the toggle
 * (`choice` null) the offer's default, which follows the refs of the submodules as they arrive.
 */
export function syncOn(choice: boolean | null, offer: SyncOffer | null): boolean {
	return offer !== null && (choice ?? offer.defaultOn);
}

/**
 * The offer for a new branch from `source`: a root has it when it has the branch the start is (the menu's, or HEAD's
 * branch for the toolbar), and that branch's oid there is the root's start. Null with no submodule.
 */
export function newBranchOffer(source: NewBranchSource, roots: readonly string[], rootRefs: RootRefs): SyncOffer | null {
	return offerOf(source.branch, roots, rootRefs, branchOidIn);
}

/** The start of a new branch made from a branch of the menu; null when the ref has no commit. */
export function sourceOf(t: BranchTarget): NewBranchSource | null {
	if (t.oid === undefined || t.oid === '') return null;
	return { label: targetLabel(t), oid: t.oid, proposed: t.kind === 'remote' ? t.name : '', branch: t };
}

/** The start of the toolbar's Create Branch: HEAD; null until the refs are read, or while HEAD has no commit. */
export function headSource(head: RefsHead | null): NewBranchSource | null {
	if (head === null || head.oid === undefined) return null;
	if (head.detached || head.branch === undefined) return { label: 'HEAD (detached)', oid: head.oid, proposed: '' };
	return { label: `HEAD (${head.branch})`, oid: head.oid, proposed: '', branch: { kind: 'local', name: head.branch } };
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
	return `Save or close ${paths.length === 1 ? 'this file' : 'these files'} first: ${paths.length} ${paths.length === 1 ? 'has' : 'have'} unsaved changes that the checkout would overwrite.`;
}

/** Why the dialog's name cannot be used, from what the list holds already; null when it can be tried. */
export function nameProblem(name: string, locals: ReadonlySet<string>): string | null {
	const n = name.trim();
	if (n === '') return 'Enter a name for the branch.';
	if (locals.has(n)) return `A branch named ${n} already exists.`;
	return null;
}
