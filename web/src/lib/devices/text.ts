// Device text is untrusted (http-websocket.md §Security baseline rule 8): it reaches the
// page only as a text node, which the sink test enforces. `inert` is the second line, for
// what a text node can still do to a reader: control characters and the bidirectional
// overrides that make one string read as another. They are shown as their code points.
// The raw value is kept where a call needs it (a selector must match the device's text).
const hidden = /[\u0000-\u001f\u007f-\u009f؜‎‏‪-‮⁦-⁩]/gu;

export function inert(s: string): string {
	return s.replace(hidden, (c) => `\\u{${(c.codePointAt(0) ?? 0).toString(16)}}`);
}

/** A device string as a quoted token, for a sentence: `"Submit"`. */
export function quoted(s: string): string {
	return `"${inert(s)}"`;
}
