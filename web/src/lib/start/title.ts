import { rootKey, type ProjectRow } from '../projects/projects';

/** Where a tab is: its project, its own tree, and whether that tree is the primary one. */
export interface TabWhere {
	project: string;
	tree: string;
	primary: boolean;
}

const baseName = (root: string): string => rootKey(root).split(/[\\/]/).pop() || root;

/**
 * The tab's place among the listed rows (ADR-0134 Q4): `project` is the name of the listed
 * row that is the worktree's main tree, else the base name of `mainRoot`, else (a main tree)
 * its own name. Null while no row matches, e.g. before `workspace.watch` first answers.
 */
export function whereTab(rows: readonly ProjectRow[], workspace: string | null): TabWhere | null {
	const row = workspace === null ? undefined : rows.find((r) => r.id === workspace);
	if (row === undefined) return null;
	if (row.mainRoot === undefined) return { project: row.name, tree: row.name, primary: true };
	const key = rootKey(row.mainRoot);
	const main = rows.find((r) => rootKey(r.root) === key);
	return { project: main?.name ?? baseName(row.mainRoot), tree: row.name, primary: false };
}

/** `Droidyard - <project>`; plain `Droidyard` when the tab's place is not known. */
export const tabTitle = (where: TabWhere | null): string => (where === null ? 'Droidyard' : `Droidyard - ${where.project}`);
