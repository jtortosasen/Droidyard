import { describe, expect, it } from 'vitest';
import { FakeCalls, capError } from '../build/testing';
import { FileOps, refuseName, type OpsClient } from './ops';

function setup() {
	const calls = new FakeCalls();
	const ops = new FileOps(calls as unknown as OpsClient, { root: '/proj/' });
	return { calls, ops };
}

describe('FileOps', () => {
	// Decision (D58): a rename is a one-item file.move, from and to in the same folder.
	it('sends a rename as a one-item file.move', async () => {
		const { calls, ops } = setup();
		calls.on('file.move', () => ({ items: [{ from: 'a/b.kt', to: 'a/c.kt' }], done: 1, failed: 0 }));
		const r = await ops.rename('a/b.kt', 'c.kt');
		expect(calls.calls).toHaveLength(1);
		expect(calls.calls[0]).toMatchObject({ name: 'file.move', params: { items: [{ from: 'a/b.kt', to: 'a/c.kt' }] }, workspace: '/proj/' });
		expect(r).toEqual({ ok: true, path: 'a/c.kt' });
	});

	// Decision (D58): an empty name, ., .., a name holding / or NUL is refused with no call.
	it('refuses bad names without a call', async () => {
		const { calls, ops } = setup();
		for (const bad of ['', '.', '..', 'x/y', 'a\0b']) {
			const r = await ops.rename('a/b.kt', bad);
			expect(r.ok).toBe(false);
			expect((await ops.create('a', bad, 'file')).ok).toBe(false);
			expect(refuseName(bad)).not.toBeNull();
		}
		expect(calls.calls).toHaveLength(0);
	});

	// Decision (D58): a failed item's code becomes the message; FILE_EXISTS names the file.
	it('turns a FILE_EXISTS item into "c.kt already exists"', async () => {
		const { calls, ops } = setup();
		calls.on('file.move', () => ({
			items: [{ from: 'a/b.kt', to: 'a/c.kt', code: 'FILE_EXISTS', message: 'target exists' }],
			done: 0,
			failed: 1,
		}));
		expect(await ops.rename('a/b.kt', 'c.kt')).toEqual({ ok: false, message: 'c.kt already exists' });
	});

	it('shows another failed item with its code', async () => {
		const { calls, ops } = setup();
		calls.on('file.move', () => ({ items: [{ from: 'b', to: 'c', code: 'FILE_NOT_FOUND', message: 'gone' }], done: 0, failed: 1 }));
		expect(await ops.rename('b', 'c')).toEqual({ ok: false, message: 'FILE_NOT_FOUND: gone' });
	});

	it('creates under a folder, and reports FILE_EXISTS thrown by file.create', async () => {
		const { calls, ops } = setup();
		calls.on('file.create', (p) => {
			const q = p as { path: string; type: string };
			if (q.path === 'notes/dup.md') throw capError('FILE_EXISTS', 'exists');
			return q;
		});
		expect(await ops.create('notes', 'a.md', 'file')).toEqual({ ok: true, path: 'notes/a.md' });
		expect(calls.calls[0]!.params).toEqual({ path: 'notes/a.md', type: 'file' });
		expect(await ops.create('notes', 'dup.md', 'file')).toEqual({ ok: false, message: 'dup.md already exists' });
		expect((await ops.create('', 'top', 'dir')).ok).toBe(true);
		expect(calls.calls[2]!.params).toEqual({ path: 'top', type: 'dir' });
	});

	it('does not call when the name is unchanged', async () => {
		const { calls, ops } = setup();
		expect(await ops.rename('a/b.kt', 'b.kt')).toEqual({ ok: true, path: 'a/b.kt' });
		expect(calls.calls).toHaveLength(0);
	});
});
