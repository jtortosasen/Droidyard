// Text rules of editing that have nothing to do with the editor library (plan m2i D5-D7):
// which line separator a file uses, the revision the daemon reports, and the limits a save
// is checked against before any call.

/** The two separators a file may be edited with (D5). */
export type Separator = '\n' | '\r\n';

/**
 * The one separator every break of `text` uses: LF, or CRLF; LF also for a text with no break.
 * Null when the breaks are mixed or a CR stands alone: such a file is shown, never edited, so
 * a save could not write back the bytes it read.
 */
export function detectSeparator(text: string): Separator | null {
	let crlf = 0;
	let lf = 0;
	for (let i = 0; i < text.length; i++) {
		const c = text.charCodeAt(i);
		if (c === 13) {
			if (text.charCodeAt(i + 1) !== 10) return null; // a CR alone
			crlf++;
			i++;
		} else if (c === 10) {
			lf++;
		}
	}
	if (crlf > 0 && lf > 0) return null;
	return crlf > 0 ? '\r\n' : '\n';
}

/** `text` with every break (CRLF, CR or LF) turned into `sep`: what a paste or a drop may insert. */
export function normalizeBreaks(text: string, sep: Separator): string {
	return text.replace(/\r\n?|\n/g, sep);
}

/** D7: `file.write`'s content limit in bytes (the capability's own). */
export const MAX_WRITE = 512 * 1024;
/** D7: the most bytes of one request's JSON, the transport's 1 MiB less its envelope. */
export const MAX_REQUEST = 1_000_000;

const encoder = new TextEncoder();

/** The lowercase hex SHA-256 of the UTF-8 bytes of `text`, as the daemon reports a file's revision (D6). */
export async function sha256Hex(text: string): Promise<string> {
	const subtle = globalThis.crypto?.subtle;
	if (subtle === undefined) throw new Error('WebCrypto is not available in this context');
	const digest = new Uint8Array(await subtle.digest('SHA-256', encoder.encode(text)));
	let out = '';
	for (const b of digest) out += b.toString(16).padStart(2, '0');
	return out;
}

/** UTF-8 length in bytes. */
export function byteLength(text: string): number {
	return encoder.encode(text).length;
}

/**
 * Why a save of `content` must not be sent (D7), or null: over `file.write`'s 512 KiB, or a
 * request whose JSON (escapes make it longer than the text) would pass the transport's limit.
 */
export function saveRefusal(path: string, content: string, ifRevision: string): string | null {
	// A UTF-16 unit is at least one byte, so this settles the common case without encoding.
	if (content.length > MAX_WRITE || byteLength(content) > MAX_WRITE) return 'The file is too large to save (over 512 KiB).';
	if (byteLength(JSON.stringify({ path, content, ifRevision })) > MAX_REQUEST) {
		return 'The file is too large to send as one request (its escaped text is over 1,000,000 bytes).';
	}
	return null;
}
