import { describe, expect, it } from 'vitest';
import type { Params, Result } from '../api/capabilities.gen';
import { CapabilityError } from '../api/client';
import { canRestore, EXPIRED_TEXT, RestoreModel, type RestoreClient } from './restore';

type Item = Result<'file.delete'>['items'][number];

function fake(opts: { items?: Item[]; waitErr?: unknown; restore?: Result<'file.restore'>['items'] }) {
	const calls: { name: string; params: unknown; ws?: string }[] = [];
	const client = {
		call(name: string, params: unknown, o?: { workspace: string }) {
			calls.push({ name, params, ws: o?.workspace });
			if (name === 'operation.wait') {
				if (opts.waitErr) return Promise.reject(opts.waitErr);
				return Promise.resolve({ operationId: 'o1', capability: 'file.delete', state: 'completed', result: { items: opts.items ?? [] } });
			}
			return Promise.resolve({ items: opts.restore ?? [], done: 0, failed: 0 });
		},
	} as unknown as RestoreClient;
	return { client, calls };
}

const op = (over: object = {}) => ({ capability: 'file.delete', tone: 'success' as const, workspace: 'w1', ...over });

describe('canRestore', () => {
	it('offers Restore on a completed delete whatever its client (the CLI is not filtered to ui)', () => {
		// Decision: every completed file.delete of this workspace, from any client. Passing only ui would fail this.
		expect(canRestore(op(), 'w1')).toBe(true);
		expect(canRestore(op(), 'w1')).toBe(true);
		expect(canRestore(op({ tone: 'failure' }), 'w1')).toBe(false);
		expect(canRestore(op({ capability: 'file.move' }), 'w1')).toBe(false);
		expect(canRestore(op({ workspace: 'w2' }), 'w1')).toBe(false);
	});
});

describe('RestoreModel', () => {
	it('reads the result only on click and restores exactly the done ids', async () => {
		const f = fake({
			items: [
				{ path: 'a', trashId: 't1' },
				{ path: 'b', code: 'FILE_NOT_FOUND' },
				{ path: 'c', trashId: 't3' },
			],
			restore: [
				{ trashId: 't1', path: 'a' },
				{ trashId: 't3', path: 'c' },
			],
		});
		const m = new RestoreModel(f.client, () => ' /p ');
		expect(f.calls).toEqual([]);
		await m.restore('o1');
		expect(f.calls[0]).toMatchObject({ name: 'operation.wait', params: { operationId: 'o1', timeoutMs: 0 } });
		expect(f.calls[1]).toMatchObject({ name: 'file.restore', params: { trashIds: ['t1', 't3'] } as Params<'file.restore'>, ws: '/p' });
		expect(m.stateOf('o1')).toMatchObject({ kind: 'done' });
	});

	it('shows a FILE_NOT_IN_TRASH item instead of swallowing it', async () => {
		const f = fake({
			items: [{ path: 'a', trashId: 't1' }],
			restore: [{ trashId: 't1', code: 'FILE_NOT_IN_TRASH', message: 'gone' }],
		});
		const m = new RestoreModel(f.client, () => '/p');
		await m.restore('o1');
		const s = m.stateOf('o1');
		expect(s).toMatchObject({ kind: 'done', items: [{ path: 'a', code: 'FILE_NOT_IN_TRASH', message: 'gone' }] });
	});

	it('says nothing to restore when no item was done, without calling file.restore', async () => {
		const f = fake({ items: [{ path: 'a', code: 'FILE_NOT_FOUND' }] });
		const m = new RestoreModel(f.client, () => '/p');
		await m.restore('o1');
		expect(m.stateOf('o1')).toEqual({ kind: 'nothing' });
		expect(f.calls.map((c) => c.name)).toEqual(['operation.wait']);
	});

	it('answers OPERATION_NOT_FOUND with the past-retention state', async () => {
		const f = fake({ waitErr: new CapabilityError({ code: 'OPERATION_NOT_FOUND', message: 'x' }, false) });
		const m = new RestoreModel(f.client, () => '/p');
		await m.restore('o1');
		expect(m.stateOf('o1')).toEqual({ kind: 'expired' });
		expect(EXPIRED_TEXT).toContain('droidyard call file.restore');
	});
});
