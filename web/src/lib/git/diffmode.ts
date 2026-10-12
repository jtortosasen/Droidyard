// The Diff tab's mode, remembered per browser (plan m2l step 7; ADR-0067 §2): Classic or Review,
// default Review. Every storage access is in try/catch and the stored value is checked on the
// way in: anything but one of the two words is the default.
import type { KeyValueStorage } from '../ui/layout';

export type DiffMode = 'classic' | 'review';

export const DIFF_MODE_KEY = 'droidyard.ui.diffmode';
export const DEFAULT_MODE: DiffMode = 'review';

export function isDiffMode(v: unknown): v is DiffMode {
	return v === 'classic' || v === 'review';
}

export function loadDiffMode(storage: KeyValueStorage | null): DiffMode {
	if (storage === null) return DEFAULT_MODE;
	try {
		const raw = storage.getItem(DIFF_MODE_KEY);
		return isDiffMode(raw) ? raw : DEFAULT_MODE;
	} catch {
		return DEFAULT_MODE;
	}
}

export function saveDiffMode(storage: KeyValueStorage | null, mode: DiffMode): void {
	if (storage === null) return;
	try {
		storage.setItem(DIFF_MODE_KEY, mode);
	} catch {
		// Not remembered; the mode in memory is still right.
	}
}
