// The smallest single replacement between two texts (m2i step 3 verifier): a re-read is
// applied as this span only, so CodeMirror keeps the scroll and the cursor outside it.

/** The one span to replace to turn `a` into `b`, by their common prefix and suffix; null when equal. */
export function minimalChange(a: string, b: string): { from: number; to: number; insert: string } | null {
	if (a === b) return null;
	const max = Math.min(a.length, b.length);
	let p = 0;
	while (p < max && a.charCodeAt(p) === b.charCodeAt(p)) p++;
	let q = 0;
	while (q < max - p && a.charCodeAt(a.length - 1 - q) === b.charCodeAt(b.length - 1 - q)) q++;
	return { from: p, to: a.length - q, insert: b.slice(p, b.length - q) };
}
