// The toolbar's task selector state (plan m2d step 8): whether the selector can be used and
// why not. The reason is shown, never just a dead control.
import type { TasksView } from '../build/tasks';

export interface SelectorState {
	/** The list cannot be opened or chosen from. */
	disabled: boolean;
	/** Why, as text; null when enabled. */
	reason: string | null;
	/** Run is not available (disabled, nothing chosen, a build runs or is starting). */
	runDisabled: boolean;
	runReason: string | null;
}

export function selectorState(view: TasksView | null): SelectorState {
	if (view === null || !view.open) {
		return { disabled: true, reason: 'Open a project to list its tasks', runDisabled: true, runReason: 'Open a project first' };
	}
	if (!view.trusted) {
		const reason = 'Trust this project to list and run its tasks';
		return { disabled: true, reason, runDisabled: true, runReason: reason };
	}
	let runReason: string | null = null;
	if (view.mirror.building !== null) runReason = 'A build is running';
	else if (view.starting) runReason = 'Starting the run';
	else if (view.selected.length === 0) runReason = 'Choose a task first';
	return { disabled: false, reason: null, runDisabled: runReason !== null, runReason };
}

/** The selector button's label: the one chosen task's name, a count, or the prompt. */
export function selectionLabel(selected: readonly string[]): string {
	if (selected.length === 0) return 'Tasks';
	if (selected.length === 1) return selected[0]!.slice(selected[0]!.lastIndexOf(':') + 1) || selected[0]!;
	return `${selected.length} tasks`;
}
