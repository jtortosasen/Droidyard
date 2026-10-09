// The panel's filters, applied on the client (plan m2a step 5; ADR-0041 §2: the daemon
// filters by application only, and restarts the stream for it). Markers always pass:
// a filter must not hide that lines were lost or that the stream ended.
import { isMarker, PRIORITIES, type Entry, type Priority } from './entry';

export interface Filter {
	/** The lowest priority shown. */
	minPriority: Priority;
	/** Exact tags (case-sensitive, as logcat tags are); empty shows every tag. */
	tags: readonly string[];
	/** A substring of the message, case-insensitive; empty shows every message. */
	text: string;
}

export const NO_FILTER: Filter = { minPriority: 'verbose', tags: [], text: '' };

/** Reads the tags field: tags separated by commas or whitespace. */
export function parseTags(input: string): string[] {
	return [...new Set(input.split(/[\s,]+/).filter((t) => t !== ''))];
}

/** True when the filter shows every line, so no per-entry test is needed. */
export function isOpen(f: Filter): boolean {
	return f.minPriority === 'verbose' && f.tags.length === 0 && f.text === '';
}

/** Builds the predicate for a filter; the work that does not depend on the entry is done once. */
export function compileFilter(f: Filter): (e: Entry) => boolean {
	const min = PRIORITIES.indexOf(f.minPriority);
	const tags = f.tags.length > 0 ? new Set(f.tags) : null;
	const text = f.text.toLowerCase();
	return (e) => {
		if (isMarker(e)) return true;
		if (PRIORITIES.indexOf(e.priority) < min) return false;
		if (tags !== null && !tags.has(e.tag)) return false;
		if (text !== '' && !e.message.toLowerCase().includes(text)) return false;
		return true;
	};
}
