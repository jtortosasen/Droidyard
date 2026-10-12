import { describe, expect, it } from 'vitest';
import { isSaveKey, type KeyLike } from './savekey';

const key = (over: Partial<KeyLike>): KeyLike => ({ key: 's', metaKey: false, ctrlKey: false, altKey: false, shiftKey: false, defaultPrevented: false, ...over });

describe('isSaveKey', () => {
	// Decision (D7): Cmd+S and Ctrl+S save from the search panel too; nothing else does, and a
	// chord the content's keymap already handled is not handled twice.
	it('is Cmd+S or Ctrl+S, plain and not yet handled', () => {
		expect(isSaveKey(key({ metaKey: true }))).toBe(true);
		expect(isSaveKey(key({ ctrlKey: true }))).toBe(true);
		expect(isSaveKey(key({ ctrlKey: true, key: 'S' }))).toBe(true);
		expect(isSaveKey(key({}))).toBe(false);
		expect(isSaveKey(key({ metaKey: true, key: 'f' }))).toBe(false);
		expect(isSaveKey(key({ metaKey: true, shiftKey: true }))).toBe(false);
		expect(isSaveKey(key({ metaKey: true, altKey: true }))).toBe(false);
		expect(isSaveKey(key({ metaKey: true, defaultPrevented: true }))).toBe(false);
	});
});
