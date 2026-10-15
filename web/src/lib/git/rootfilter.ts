// The root filter's memory (plan m2n R1; the owner's first check): the roots the log is filtered to are kept per
// browser and project, a per-viewer convenience like the favourites (refs.ts): every read and write in try/catch,
// what is read back checked, and the page right without it. What is stored is the choice (a list of root paths, or
// null for every root); the roots present at the time narrow it, so a submodule that has gone is dropped from what
// is applied and the stored choice returns when it comes back, until the person chooses again.
import type { KeyValueStorage } from '../ui/layout';

export const ROOT_FILTER_PREFIX = 'droidyard.ui.gitroots.';
const MAX_ROOTS = 1000;
const MAX_PATH = 4096;

/** Where the store reads and writes the choice. */
export interface RootFilterMemory {
	load(): readonly string[] | null;
	save(filter: readonly string[] | null): void;
}

/** The roots stored for this project; anything that is not a list of paths is "every root" (null). Never throws. */
export function loadRootFilter(storage: KeyValueStorage | null, project: string): string[] | null {
	if (storage === null) return null;
	try {
		const raw = storage.getItem(ROOT_FILTER_PREFIX + project);
		if (raw === null) return null;
		const v: unknown = JSON.parse(raw);
		if (!Array.isArray(v)) return null;
		const out: string[] = [];
		for (const p of v.slice(0, MAX_ROOTS)) if (typeof p === 'string' && p !== '' && p.length <= MAX_PATH) out.push(p);
		return out.length === 0 ? null : out;
	} catch {
		return null;
	}
}

/** Stores the choice; a storage that refuses is ignored. */
export function saveRootFilter(storage: KeyValueStorage | null, project: string, filter: readonly string[] | null): void {
	if (storage === null) return;
	try {
		storage.setItem(ROOT_FILTER_PREFIX + project, JSON.stringify(filter === null ? null : filter.slice(0, MAX_ROOTS)));
	} catch {
		// Not remembered; the filter in memory is still right.
	}
}

export function rootFilterMemory(storage: KeyValueStorage | null, project: string): RootFilterMemory {
	return { load: () => loadRootFilter(storage, project), save: (f) => saveRootFilter(storage, project, f) };
}

/** `remembered` narrowed to `roots` (in their order); a result of no root, or of every root, is null ("all"). */
export function narrowFilter(remembered: readonly string[] | null, roots: readonly string[]): string[] | null {
	if (remembered === null) return null;
	const chosen = roots.filter((r) => remembered.includes(r));
	return chosen.length === 0 || chosen.length >= roots.length ? null : chosen;
}
