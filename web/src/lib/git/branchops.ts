// What the History tab's branch entries call (plan m2n P3; ADR-0071 §1, §5), pure:
//   - which enabled entry of the branch menu does what for a local or a remote-tracking branch:
//     Checkout is one `git.checkout`, New branch from opens the dialog at that branch's commit;
//     every other entry does nothing here (Compare with current waits for step P9, the rest for
//     a later milestone);
//   - the toolbar's Create Branch, from HEAD;
//   - the parameters of the two calls: a remote-tracking branch is checked out as
//     `{name, remote}` (the daemon creates the local branch tracking it, or switches to the
//     local one of that name), and proposes its name without the remote for a new branch;
//   - the dirty-buffer gate (ADR-0071 §5): the editor's unsaved files block a checkout, and a new
//     branch that is checked out, before anything is called. The daemon would rewrite the files
//     under the buffer; refusing first loses nothing. A new branch that is not checked out
//     touches no file, so it is not blocked.
// Every name here is git's text: the module builds parameters and messages, never markup.
import type { Params } from '../api/capabilities.gen';
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
}

/** What an enabled entry does: a call now, or a dialog first. */
export type EntryAction =
	| { kind: 'checkout'; target: BranchTarget; params: Params<'git.checkout'> }
	| { kind: 'new-branch'; source: NewBranchSource };

/** The text of a target as the list shows it: `feature/x`, `origin/feature/x`. */
export function targetLabel(t: BranchTarget): string {
	return t.kind === 'local' ? t.name : `${t.remote}/${t.name}`;
}

export function checkoutParams(t: BranchTarget): Params<'git.checkout'> {
	return t.kind === 'local' ? { name: t.name } : { name: t.name, remote: t.remote };
}

export function createParams(name: string, checkout: boolean, from: string): Params<'git.createBranch'> {
	return { name, checkout, from };
}

/** The start of a new branch made from a branch of the menu; null when the ref has no commit. */
export function sourceOf(t: BranchTarget): NewBranchSource | null {
	if (t.oid === undefined || t.oid === '') return null;
	return { label: targetLabel(t), oid: t.oid, proposed: t.kind === 'remote' ? t.name : '' };
}

/** The start of the toolbar's Create Branch: HEAD; null until the refs are read, or while HEAD has no commit. */
export function headSource(head: RefsHead | null): NewBranchSource | null {
	if (head === null || head.oid === undefined) return null;
	return { label: head.detached || head.branch === undefined ? 'HEAD (detached)' : `HEAD (${head.branch})`, oid: head.oid, proposed: '' };
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
		default:
			return null;
	}
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
