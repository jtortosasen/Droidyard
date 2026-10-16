// What the toolbar's Gradle root control shows (ADR-0075 §7), from `workspace.describe`'s
// `gradle` alone: the file's `gradleRoot` is never read back for display, the daemon says
// which root it resolved.
import type { GradleInfo } from '../build/workspace';

export type GradleChoice =
	/** One root and nothing else to choose: shown, not editable. */
	| { kind: 'fixed'; root: string }
	/** More than one candidate, or no root yet: a select. `root` is chosen when there is one. */
	| { kind: 'select'; root: string | undefined; options: string[]; message: string | undefined }
	/** No root and nothing to choose from: the daemon's message. */
	| { kind: 'none'; message: string };

export function gradleChoice(gradle: GradleInfo | null): GradleChoice | null {
	if (gradle === null) return null;
	const { root, candidates, error } = gradle;
	if (root !== undefined && candidates.length <= 1) return { kind: 'fixed', root };
	if (root === undefined && candidates.length === 0) {
		return { kind: 'none', message: error?.message ?? 'No Gradle build found in this project.' };
	}
	// A root named by hand, deeper than the listing goes, is still the chosen one.
	const options = root !== undefined && !candidates.includes(root) ? [root, ...candidates] : [...candidates];
	return { kind: 'select', root, options, message: root === undefined ? error?.message : undefined };
}
