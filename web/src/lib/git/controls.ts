// The History tab's write controls (plan m2n step 6; the owner's items 6, 7, 8, 10). In part 1
// every one of them is SHOWN and DISABLED, with a title that says which milestone part enables
// it; none calls a capability. `activate` is the one door a click goes through, so a control
// that is not enabled cannot reach a client: the markup also carries the `disabled` attribute.
// Part 2 (ADR-0071) enables Fetch, Create Branch, Checkout, New branch from and Compare with
// current by setting `enabled` and giving them an action; the rest wait for a later milestone.

export type ToolbarId = 'fetch' | 'pull' | 'push' | 'stash' | 'create-branch';
export type MenuId = 'checkout' | 'new-branch-from' | 'compare' | 'merge' | 'rebase' | 'push' | 'rename' | 'delete';

export interface Control<Id extends string> {
	id: Id;
	label: string;
	enabled: boolean;
	/** The title: why the control does nothing yet. */
	title: string;
}

const PART_2 = 'Enabled in milestone 2n, part 2';
const LATER = 'Enabled in a later milestone (not 2n)';

/** SourceGit's order: Fetch, Pull, Push, Stash, then (after a separator) Create Branch. */
export const TOOLBAR: readonly Control<ToolbarId>[] = [
	{ id: 'fetch', label: 'Fetch', enabled: false, title: `Fetch: ${PART_2}` },
	{ id: 'pull', label: 'Pull', enabled: false, title: `Pull: ${LATER}` },
	{ id: 'push', label: 'Push', enabled: false, title: `Push: ${LATER}` },
	{ id: 'stash', label: 'Stash', enabled: false, title: `Stash: ${LATER}` },
	{ id: 'create-branch', label: 'Create Branch', enabled: false, title: `Create Branch: ${PART_2}` },
];

/** IntelliJ's eight entries, in its order (item 7). Rename and Delete wait with the other writes (item 8). */
export const BRANCH_MENU: readonly Control<MenuId>[] = [
	{ id: 'checkout', label: 'Checkout', enabled: false, title: `Checkout: ${PART_2}` },
	{ id: 'new-branch-from', label: 'New branch from', enabled: false, title: `New branch from: ${PART_2}` },
	{ id: 'compare', label: 'Compare with current', enabled: false, title: `Compare with current: ${PART_2}` },
	{ id: 'merge', label: 'Merge into current', enabled: false, title: `Merge into current: ${LATER}` },
	{ id: 'rebase', label: 'Rebase onto', enabled: false, title: `Rebase onto: ${LATER}` },
	{ id: 'push', label: 'Push', enabled: false, title: `Push: ${LATER}` },
	{ id: 'rename', label: 'Rename', enabled: false, title: `Rename: ${LATER}` },
	{ id: 'delete', label: 'Delete', enabled: false, title: `Delete: ${LATER}` },
];

/** A click on a control: runs `run` only when the control is enabled. Returns whether it ran. */
export function activate(control: Control<string>, run: () => void): boolean {
	if (!control.enabled) return false;
	run();
	return true;
}
