import { describe, expect, it } from 'vitest';
import { FakeCalls, capError } from '../build/testing';
import { FileOps, refuseName, type OpsClient } from './ops';

function setup() {
	const calls = new FakeCalls();
	const moved: [string, string][] = [];
	const ops = new FileOps(calls as unknown as OpsClient, { root: '/proj/' }, { moved: (f, t) => moved.push([f, t]) });
	return { calls, ops, moved };
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

type Pair = { from: string; to: string };
const itemsOf = (p: unknown) => (p as { items: Pair[] }).items;

describe('FileOps batches', () => {
	// Decision (D54): items that come back FILE_EXISTS open one dialog naming them; the rest are done.
	it('lists the two taken items in the dialog', async () => {
		const { calls, ops } = setup();
		calls.on('file.move', (p) => ({
			items: itemsOf(p).map((i) => (i.from === 'b' ? i : { ...i, code: 'FILE_EXISTS', message: 'x' })),
			done: 1,
			failed: 2,
		}));
		await ops.move([
			{ from: 'a', to: 'd/a' },
			{ from: 'b', to: 'd/b' },
			{ from: 'c', to: 'd/c' },
		]);
		expect(ops.pending?.names).toEqual(['a', 'c']);
		expect(ops.pending?.items).toEqual([
			{ from: 'a', to: 'd/a' },
			{ from: 'c', to: 'd/c' },
		]);
	});

	// Decision (D54): Keep both repeats the call for the taken items only, with keepBoth; Skip makes no call.
	it('keeps both with one call for the taken items, and skips with none', async () => {
		const { calls, ops } = setup();
		let first = true;
		calls.on('file.copy', (p) => {
			const items = itemsOf(p);
			if (first) {
				first = false;
				return { items: items.map((i) => (i.from === 'b' ? i : { ...i, code: 'FILE_EXISTS', message: 'x' })), done: 1, failed: 2 };
			}
			return { items: items.map((i) => ({ ...i, to: `${i.to} copy` })), done: items.length, failed: 0 };
		});
		const batch = [
			{ from: 'a', to: 'd/a' },
			{ from: 'b', to: 'd/b' },
			{ from: 'c', to: 'd/c' },
		];
		await ops.copy(batch);
		await ops.resolveCollision('keepBoth');
		expect(calls.calls).toHaveLength(2);
		expect(calls.calls[1]!.params).toEqual({
			items: [
				{ from: 'a', to: 'd/a' },
				{ from: 'c', to: 'd/c' },
			],
			onCollision: 'keepBoth',
		});
		expect(ops.pending).toBeNull();

		first = true;
		await ops.copy(batch);
		await ops.resolveCollision('skip');
		expect(calls.calls).toHaveLength(3);
		expect(ops.pending).toBeNull();
	});

	// Decision (D56): one line reports the operation: the count, then the failures as text.
	it('writes the notice line', async () => {
		const { calls, ops } = setup();
		calls.on('file.delete', () => ({
			items: [
				{ path: 'a', trashId: '1' },
				{ path: 'b', trashId: '2' },
				{ path: 'c', trashId: '3' },
				{ path: 'x/d.kt', code: 'FILE_NOT_FOUND', message: 'gone' },
			],
			done: 3,
			failed: 1,
		}));
		await ops.delete(['a', 'b', 'c', 'x/d.kt']);
		expect(ops.notice).toEqual({ text: 'Deleted 3 items · 1 failed: FILE_NOT_FOUND: gone', undoable: true });
	});

	// Decision (D57): a move with item 2 failed re-points the tabs of items 1 and 3 only, and so does its undo.
	it('re-points tabs for the done items, forward and back', async () => {
		const { calls, ops, moved } = setup();
		calls.on('file.move', (p) => ({
			items: itemsOf(p).map((i) => (i.from === 'b' ? { ...i, code: 'FILE_NOT_FOUND', message: 'gone' } : i)),
			done: 2,
			failed: 1,
		}));
		await ops.move([
			{ from: 'a', to: 'd/a' },
			{ from: 'b', to: 'd/b' },
			{ from: 'c', to: 'd/c' },
		]);
		expect(moved).toEqual([
			['a', 'd/a'],
			['c', 'd/c'],
		]);
		moved.length = 0;
		await ops.undo();
		expect(moved).toEqual([
			['d/c', 'c'],
			['d/a', 'a'],
		]);
	});

	// Decision (D52): a paste of duplicates is a copy with keepBoth, after the plain items.
	it('sends a paste: items as they are, then duplicates with keepBoth', async () => {
		const { calls, ops } = setup();
		calls.on('file.copy', (p) => ({ items: itemsOf(p), done: itemsOf(p).length, failed: 0 }));
		await ops.paste({ kind: 'copy', items: [{ from: 'l/b', to: 'a/b' }], duplicates: [{ from: 'a/c', to: 'a/c' }] });
		expect(calls.calls.map((c) => c.params)).toEqual([{ items: [{ from: 'l/b', to: 'a/b' }] }, { items: [{ from: 'a/c', to: 'a/c' }], onCollision: 'keepBoth' }]);
	});
});
