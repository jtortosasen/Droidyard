import { describe, expect, it } from 'vitest';
import { MAX_REQUEST, MAX_WRITE, detectSeparator, normalizeBreaks, saveRefusal, sha256Hex } from './text';

describe('detectSeparator', () => {
	// Decision (D5): all-LF, all-CRLF and no-break files are edited; mixed ones and a lone CR are not.
	it('names the one separator a file uses, and refuses mixtures', () => {
		expect(detectSeparator('a\nb\n')).toBe('\n');
		expect(detectSeparator('a\r\nb\r\n')).toBe('\r\n');
		expect(detectSeparator('no break')).toBe('\n');
		expect(detectSeparator('')).toBe('\n');
		expect(detectSeparator('a\r\nb\n')).toBeNull();
		expect(detectSeparator('a\nb\r\n')).toBeNull();
		expect(detectSeparator('a\rb')).toBeNull();
		expect(detectSeparator('a\r\nb\r')).toBeNull();
	});
});

describe('normalizeBreaks', () => {
	it('turns every break into the separator', () => {
		expect(normalizeBreaks('a\nb\r\nc\rd', '\r\n')).toBe('a\r\nb\r\nc\r\nd');
		expect(normalizeBreaks('a\nb\r\nc\rd', '\n')).toBe('a\nb\nc\nd');
	});
});

describe('sha256Hex', () => {
	// The daemon's revision is the SHA-256 of the file's bytes: known vectors.
	it('is the lowercase hex SHA-256 of the UTF-8 bytes', async () => {
		expect(await sha256Hex('')).toBe('e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855');
		expect(await sha256Hex('abc')).toBe('ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad');
		// Multi-byte text is hashed as its UTF-8 bytes (0xC3 0xA9), not as UTF-16 units.
		expect(await sha256Hex('é')).toBe('4a99557e4033c3539de2eb65472017cad5f9557f7a0625a09f1c3f6e2ba69c4c');
	});
});

describe('saveRefusal (D7)', () => {
	// Decision: nothing over 512 KiB or ~1,000,000 bytes of JSON is sent.
	it('lets a small save through and refuses one over 512 KiB', () => {
		expect(saveRefusal('a.kt', 'x'.repeat(1000), 'r')).toBeNull();
		expect(saveRefusal('a.kt', 'x'.repeat(MAX_WRITE), 'r')).toBeNull();
		expect(saveRefusal('a.kt', 'x'.repeat(MAX_WRITE + 1), 'r')).toMatch(/512 KiB/);
		// Bytes, not units: 200,000 three-byte characters are 600,000 bytes.
		expect(saveRefusal('a.kt', '€'.repeat(200_000), 'r')).toMatch(/512 KiB/);
	});

	it('refuses content whose escaped JSON is over the request limit although the text is not', () => {
		// A control character is one byte of content and six of JSON ("\u0001").
		const content = '\u0001'.repeat(200_000);
		expect(new TextEncoder().encode(content).length).toBeLessThan(MAX_WRITE);
		expect(saveRefusal('a.kt', content, 'r')).toMatch(/1,000,000/);
		const ok = '\u0001'.repeat(150_000);
		expect(new TextEncoder().encode(JSON.stringify({ path: 'a.kt', content: ok, ifRevision: 'r' })).length).toBeLessThan(MAX_REQUEST);
		expect(saveRefusal('a.kt', ok, 'r')).toBeNull();
	});
});
