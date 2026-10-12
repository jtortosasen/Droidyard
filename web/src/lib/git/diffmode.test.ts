import { describe, expect, it } from 'vitest';
import type { KeyValueStorage } from '../ui/layout';
import { DEFAULT_MODE, DIFF_MODE_KEY, loadDiffMode, saveDiffMode } from './diffmode';

class Mem implements KeyValueStorage {
	data = new Map<string, string>();
	getItem(k: string) {
		return this.data.get(k) ?? null;
	}
	setItem(k: string, v: string) {
		this.data.set(k, v);
	}
}

describe('the remembered diff mode', () => {
	// Decision (ADR-0067 §2): the default is Review, and what was saved comes back.
	it('defaults to Review and returns what was saved', () => {
		const s = new Mem();
		expect(DEFAULT_MODE).toBe('review');
		expect(loadDiffMode(s)).toBe('review');
		saveDiffMode(s, 'classic');
		expect(loadDiffMode(s)).toBe('classic');
		saveDiffMode(s, 'review');
		expect(loadDiffMode(s)).toBe('review');
	});

	// Decision: the stored value is checked on the way in; a stored word that is not a mode is not one.
	it('refuses a stored value that is not a mode', () => {
		const s = new Mem();
		for (const bad of ['', 'Classic', 'unified', '{"mode":"classic"}', 'classic ', 'null']) {
			s.data.set(DIFF_MODE_KEY, bad);
			expect(loadDiffMode(s)).toBe('review');
		}
	});

	// Decision: a storage that throws (private mode, blocked) is no storage: nothing breaks.
	it('survives a storage that throws, or none', () => {
		const bad: KeyValueStorage = {
			getItem: () => {
				throw new Error('blocked');
			},
			setItem: () => {
				throw new Error('full');
			},
		};
		expect(loadDiffMode(bad)).toBe('review');
		expect(() => saveDiffMode(bad, 'classic')).not.toThrow();
		expect(loadDiffMode(null)).toBe('review');
		expect(() => saveDiffMode(null, 'classic')).not.toThrow();
	});
});
