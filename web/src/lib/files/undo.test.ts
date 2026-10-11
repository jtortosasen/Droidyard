import { describe, expect, it } from 'vitest';
import { FakeCalls } from '../build/testing';
import { FileOps, type OpsClient } from './ops';
import { UNDO_LIMIT, UndoStack } from './undo';

function setup() {
	const calls = new FakeCalls();
	const moved: [string, string][] = [];
	const ops = new FileOps(calls as unknown as OpsClient, { root: '/proj/' }, { moved: (f, t) => moved.push([f, t]) });
	return { calls, ops, moved };
}

describe('undo', () => {
	// Decision (D55): a delete is undone by file.restore of exactly the trash ids of its done items.
	it('restores exactly the trash ids of a delete', async () => {
		const { calls, ops } = setup();
		calls.on('file.delete', () => ({
			items: [
				{ path: 'a', trashId: 't1' },
				{ path: 'b', trashId: 't2', code: 'FILE_NOT_FOUND', message: 'gone' },
				{ path: 'c', trashId: 't3' },
			],
			done: 2,
			failed: 1,
		}));
		calls.on('file.restore', () => ({ items: [{ trashId: 't1', path: 'a' }, { trashId: 't3', path: 'c' }], done: 2, failed: 0 }));
		await ops.delete(['a', 'b', 'c']);
		await ops.undo();
		expect(calls.calls[1]).toMatchObject({ name: 'file.restore', params: { trashIds: ['t1', 't3'] } });
		expect(ops.canUndo).toBe(false);
	});

	// Decision (D55): a move with item 2 failed is undone for items 1 and 3 only.
	it('moves back only the done items', async () => {
		const { calls, ops } = setup();
		calls.on('file.move', (p) => {
			const items = (p as { items: { from: string; to: string }[] }).items;
			return {
				items: items.map((i) => (i.from === 'b' ? { ...i, code: 'FILE_NOT_FOUND', message: 'gone' } : i)),
				done: 2,
				failed: 1,
			};
		});
		await ops.move([
			{ from: 'a', to: 'd/a' },
			{ from: 'b', to: 'd/b' },
			{ from: 'c', to: 'd/c' },
		]);
		await ops.undo();
		expect(calls.calls[1]!.params).toEqual({
			items: [
				{ from: 'd/c', to: 'c' },
				{ from: 'd/a', to: 'a' },
			],
		});
	});

	// Decision (D55): a copy is undone by deleting what it made (the free names, not the asked ones).
	it('deletes the paths a copy made', async () => {
		const { calls, ops } = setup();
		calls.on('file.copy', () => ({ items: [{ from: 'a.kt', to: 'a copy.kt' }], done: 1, failed: 0 }));
		calls.on('file.delete', () => ({ items: [{ path: 'a copy.kt', trashId: 't' }], done: 1, failed: 0 }));
		await ops.duplicate(['a.kt']);
		await ops.undo();
		expect(calls.calls[1]).toMatchObject({ name: 'file.delete', params: { paths: ['a copy.kt'] } });
	});

	// Decision (D55): the stack keeps the last 20; the 21st drops the oldest.
	it('drops the oldest past 20', () => {
		const s = new UndoStack();
		for (let i = 0; i < UNDO_LIMIT + 1; i++) s.push({ kind: 'made', paths: [`p${i}`] });
		expect(s.size).toBe(20);
		let oldest: unknown;
		for (let e = s.pop(); e !== undefined; e = s.pop()) oldest = e;
		expect(oldest).toEqual({ kind: 'made', paths: ['p1'] });
	});

	// Decision (D55): an inverse's own failure is reported, never forced (no keepBoth), and there is no redo.
	it('reports FILE_EXISTS of an undo without keepBoth', async () => {
		const { calls, ops } = setup();
		calls.on('file.move', (p) => {
			const items = (p as { items: { from: string; to: string }[] }).items;
			return items[0]!.from === 'a' ? { items, done: 1, failed: 0 } : { items: [{ ...items[0]!, code: 'FILE_EXISTS', message: 'x' }], done: 0, failed: 1 };
		});
		await ops.move([{ from: 'a', to: 'd/a' }]);
		await ops.undo();
		expect(calls.calls[1]!.params).toEqual({ items: [{ from: 'd/a', to: 'a' }] });
		expect(ops.notice?.text).toContain('a already exists');
		expect(ops.notice?.undoable).toBe(false);
		expect(ops.canUndo).toBe(false);
	});
});
