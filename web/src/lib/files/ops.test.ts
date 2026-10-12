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

	// Decision (D56, the close verifier's B1): the notice names the top of the Undo stack,
	// so a create or a rename after a delete replaces the delete's line, and the line's Undo
	// removes what it names. Seen failing with create and rename leaving the notice alone.
	it('names a create or a rename in the notice, so its Undo undoes what it says', async () => {
		const { calls, ops } = setup();
		calls.on('file.delete', (p) => ({ items: (p as { paths: string[] }).paths.map((path, i) => ({ path, trashId: String(i + 1) })), done: 1, failed: 0 }));
		calls.on('file.create', (p) => p);
		calls.on('file.move', (p) => ({ items: itemsOf(p), done: 1, failed: 0 }));
		await ops.delete(['a.kt']);
		await ops.create('src', 'x.kt', 'file');
		expect(ops.notice).toEqual({ text: 'Created x.kt', undoable: true });
		await ops.rename('src/y.kt', 'z.kt');
		expect(ops.notice).toEqual({ text: 'Renamed y.kt to z.kt', undoable: true });
		await ops.undo();
		expect(calls.calls.at(-1)).toMatchObject({ name: 'file.move', params: { items: [{ from: 'src/z.kt', to: 'src/y.kt' }] } });
		await ops.undo();
		expect(calls.calls.at(-1)).toMatchObject({ name: 'file.delete', params: { paths: ['src/x.kt'] } });
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

describe('FileOps and open buffers with unsaved changes (m2i D7)', () => {
	function guarded(dirty: string[]) {
		const calls = new FakeCalls();
		const moved: [string, string][] = [];
		const ops = new FileOps(
			calls as unknown as OpsClient,
			{ root: '/proj/' },
			{ moved: (f, t) => moved.push([f, t]), unsaved: (p) => dirty.some((d) => d === p || d.startsWith(`${p}/`)) },
		);
		return { calls, ops, moved };
	}

	// Decision (D7): a move, copy or rename whose target is, or holds, an open dirty tab is
	// refused before any call; nothing replaces, so the target's file is gone and the buffer is the user's.
	it('refuses a rename, move, copy and paste onto an open dirty tab with no call', async () => {
		const { calls, ops, moved } = guarded(['dir/b.kt']);
		const r = await ops.rename('dir/a.kt', 'b.kt');
		expect(r.ok).toBe(false);
		expect(r.ok === false && r.message).toMatch(/dir\/b\.kt.*unsaved/);
		await ops.move([{ from: 'x.kt', to: 'dir/b.kt' }]);
		expect(ops.notice?.text).toMatch(/Move refused.*unsaved/);
		await ops.copy([{ from: 'x.kt', to: 'dir/b.kt' }]);
		expect(ops.notice?.text).toMatch(/Copy refused.*unsaved/);
		await ops.paste({ kind: 'move', items: [{ from: 'y.kt', to: 'dir/b.kt' }], duplicates: [] });
		// A folder target that holds the dirty file is refused too.
		await ops.move([{ from: 'folder', to: 'dir' }]);
		expect(calls.calls).toEqual([]);
		expect(moved).toEqual([]);
	});

	it('lets an operation onto a free path through, and tells the tabs', async () => {
		const { calls, ops, moved } = guarded(['dir/b.kt']);
		calls.on('file.move', () => ({ items: [{ from: 'dir/a.kt', to: 'dir/c.kt' }], done: 1, failed: 0 }));
		expect(await ops.rename('dir/a.kt', 'c.kt')).toEqual({ ok: true, path: 'dir/c.kt' });
		expect(moved).toEqual([['dir/a.kt', 'dir/c.kt']]);
	});

	// Decision (D7): Undo of a move whose way back lands on an open dirty tab is refused, and
	// stays on the stack for after the save.
	it('refuses an Undo that would land on a dirty tab, and keeps it on the stack', async () => {
		const dirty: string[] = [];
		const calls = new FakeCalls();
		const ops = new FileOps(calls as unknown as OpsClient, { root: '/proj/' }, { unsaved: (p) => dirty.includes(p) });
		calls.on('file.move', () => ({ items: [{ from: 'a.kt', to: 'b.kt' }], done: 1, failed: 0 }));
		await ops.rename('a.kt', 'b.kt');
		dirty.push('a.kt'); // a new file was opened at the old name and edited
		const before = calls.calls.length;
		await ops.undo();
		expect(calls.calls.length).toBe(before);
		expect(ops.notice?.text).toMatch(/Undo refused.*unsaved/);
		expect(ops.canUndo).toBe(true);
		dirty.length = 0;
		calls.on('file.move', () => ({ items: [{ from: 'b.kt', to: 'a.kt' }], done: 1, failed: 0 }));
		await ops.undo();
		expect(calls.calls.length).toBe(before + 1);
	});

	// Decision: Keep both and a duplicate land on a free name the daemon picks: not asked.
	it('does not check a duplicate', async () => {
		const { calls, ops } = guarded(['a.kt']);
		calls.on('file.copy', () => ({ items: [{ from: 'a.kt', to: 'a copy.kt' }], done: 1, failed: 0 }));
		await ops.duplicate(['a.kt']);
		expect(calls.names()).toEqual(['file.copy']);
	});

	// Decision (D7, verifier): Keep both and a duplicate land on a free name the daemon picks in
	// the target's folder, and with a file gone that name can be the path of an open tab with
	// unsaved text. Refused before any call when such a tab lies directly in that folder.
	it('refuses a duplicate or a Keep both into a folder holding a deleted file’s unsaved tab', async () => {
		const calls = new FakeCalls();
		const ops = new FileOps(calls as unknown as OpsClient, { root: '/proj/' }, { goneUnsaved: (dir) => dir === 'dir' });
		await ops.duplicate(['dir/a.kt']);
		expect(ops.notice?.text).toMatch(/Copy refused: dir has an open file that was deleted/);
		await ops.paste({ kind: 'copy', items: [], duplicates: [{ from: 'x.kt', to: 'dir/x.kt' }] });
		expect(calls.calls).toEqual([]);

		// A folder without one is not asked, and neither is the root.
		calls.on('file.copy', () => ({ items: [{ from: 'other/a.kt', to: 'other/a copy.kt' }], done: 1, failed: 0 }));
		await ops.duplicate(['other/a.kt']);
		await ops.duplicate(['top.kt']);
		expect(calls.names()).toEqual(['file.copy', 'file.copy']);
	});

	it('refuses the Keep both answer into such a folder, and keeps nothing pending', async () => {
		const calls = new FakeCalls();
		let gone = false;
		const ops = new FileOps(calls as unknown as OpsClient, { root: '/proj/' }, { goneUnsaved: () => gone });
		calls.on('file.copy', () => ({ items: [{ from: 'a.kt', to: 'dir/a.kt', code: 'FILE_EXISTS', message: 'exists' }], done: 0, failed: 1 }));
		await ops.copy([{ from: 'a.kt', to: 'dir/a.kt' }]);
		expect(ops.pending).not.toBeNull();
		gone = true; // the tab appeared while the dialog was open
		await ops.resolveCollision('keepBoth');
		expect(calls.names()).toEqual(['file.copy']); // only the first, refused one retried: no second call
		expect(ops.notice?.text).toMatch(/refused/);
	});
});
