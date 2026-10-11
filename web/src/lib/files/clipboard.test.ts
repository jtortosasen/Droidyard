import { describe, expect, it } from 'vitest';
import { Clipboard } from './clipboard';

describe('Clipboard', () => {
	// Decision (D52): a cut pasted into another folder is a move to <dir>/<name>, and empties the clipboard.
	it('pastes a cut into res/ as moves and empties itself', () => {
		const c = new Clipboard();
		c.put('cut', 'w1', ['app/a.kt', 'app/b.kt']);
		expect(c.isCut('app/a.kt')).toBe(true);
		const plan = c.paste('w1', 'res');
		expect(plan).toEqual({
			kind: 'move',
			items: [
				{ from: 'app/a.kt', to: 'res/a.kt' },
				{ from: 'app/b.kt', to: 'res/b.kt' },
			],
			duplicates: [],
		});
		expect(c.mode).toBeNull();
		expect(c.paste('w1', 'res')).toBeNull();
	});

	// Decision (D52): a cut pasted back into its own folder makes no call (no items).
	it('pastes a cut into its own folder as nothing', () => {
		const c = new Clipboard();
		c.put('cut', 'w1', ['app/a.kt']);
		const plan = c.paste('w1', 'app');
		expect(plan?.items).toEqual([]);
		expect(plan?.duplicates).toEqual([]);
	});

	// Decision (D52): a copy pasted into its own folder is a duplicate (keepBoth); elsewhere it is a plain copy,
	// and the clipboard stays for another paste.
	it('pastes a copy into its own folder as a duplicate', () => {
		const c = new Clipboard();
		c.put('copy', 'w1', ['app/a.kt', 'lib/b.kt']);
		const plan = c.paste('w1', 'app');
		expect(plan).toEqual({ kind: 'copy', items: [{ from: 'lib/b.kt', to: 'app/b.kt' }], duplicates: [{ from: 'app/a.kt', to: 'app/a.kt' }] });
		expect(c.mode).toBe('copy');
	});

	// Decision (D52): Paste is disabled in another project (paths belong to one root).
	it('refuses another workspace', () => {
		const c = new Clipboard();
		c.put('copy', 'w1', ['a.kt']);
		expect(c.canPaste('w1')).toBe(true);
		expect(c.canPaste('w2')).toBe(false);
		expect(c.paste('w2', '')).toBeNull();
	});

	// Decision (D52): paste goes into the selected folder, or a selected file's parent.
	it('targets a folder or a file\'s parent', () => {
		expect(Clipboard.targetFor('app/res', true)).toBe('app/res');
		expect(Clipboard.targetFor('app/a.kt', false)).toBe('app');
		expect(Clipboard.targetFor(null, false)).toBe('');
	});
});
