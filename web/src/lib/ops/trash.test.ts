import { describe, expect, it } from 'vitest';
import type { Result } from '../api/capabilities.gen';
import { CapabilityError } from '../api/client';
import type { Operation } from './activity';
import { deletedBy, deletedBySession, GONE_TEXT, TrashModel, unaccounted, type TrashClient, type TrashEntry } from './trash';

const T0 = Date.parse('2026-10-18T10:00:00Z');
const at = (ms: number) => new Date(T0 + ms).toISOString();

const entry = (id: string, ms: number, over: Partial<TrashEntry> = {}): TrashEntry => ({
	id,
	path: `${id}.kt`,
	type: 'file',
	deletedAt: at(ms),
	size: 1,
	...over,
});

const del = (over: Partial<Operation> = {}): Operation => ({
	id: 'o1',
	capability: 'file.delete',
	workspace: 'w1',
	client: 'cli',
	state: 'completed',
	startedAt: at(0),
	durationMs: 50,
	...over,
});

describe('unaccounted', () => {
	it('shows an entry from before the tab opened and hides the one an operation of the store made', () => {
		// Decision (plan m2s step 8): an entry inside a file.delete operation's span has that
		// operation's own row with Restore; showing it again would be a second row for one delete.
		const before = entry('before', -60_000);
		const mine = entry('mine', 20);
		expect(unaccounted([mine, before], [del()], 'w1')).toEqual([before]);
		// With no operation at all, every entry is from before.
		expect(unaccounted([mine, before], [], 'w1')).toEqual([mine, before]);
	});

	it('shows an entry deleted while the connection was lost (after the last operation)', () => {
		// A cutoff of "the first delete seen" would hide it; the span of each operation does not.
		const lost = entry('lost', 600_000);
		expect(unaccounted([lost], [del()], 'w1')).toEqual([lost]);
	});

	it('hides what a running delete is writing, and ignores other workspaces and capabilities', () => {
		const e = entry('e', 5_000);
		expect(unaccounted([e], [del({ state: 'running', durationMs: undefined })], 'w1')).toEqual([]);
		expect(unaccounted([e], [del({ workspace: 'w2', state: 'running', durationMs: undefined })], 'w1')).toEqual([e]);
		expect(unaccounted([e], [del({ capability: 'file.move', state: 'running', durationMs: undefined })], 'w1')).toEqual([e]);
	});

	it('shows the entries of a delete that was cancelled or failed: its row cannot restore', () => {
		const e = entry('e', 20);
		expect(unaccounted([e], [del({ state: 'cancelled' })], 'w1')).toEqual([e]);
		expect(unaccounted([e], [del({ state: 'failed' })], 'w1')).toEqual([e]);
		expect(unaccounted([e], [del({ state: 'unseen', durationMs: undefined })], 'w1')).toEqual([e]);
	});
});

describe('deletedBy', () => {
	it('puts an entry with a session name under the agent, and the rest under You', () => {
		// Decision (ADR-0096 §3): an entry written before `by` existed, a UI delete and a
		// nameless CLI delete are You; a CLI delete joined to its chat carries the name.
		expect(deletedBy({ by: { client: 'cli', name: 'Claude Code' } })).toEqual({ label: 'Claude Code', you: false });
		expect(deletedBy({})).toEqual({ label: 'You', you: true });
		expect(deletedBy({ by: { client: 'ui' } })).toEqual({ label: 'You', you: true });
		expect(deletedBy({ by: { client: 'cli' } })).toEqual({ label: 'You', you: true });
	});
});

describe('deletedBySession', () => {
	// Decision (ADR-0127 §4): `by` carries only the session's own name, so the selected session
	// is matched by that raw name. Comparing the numbered label ("claude-code 2") would match no
	// entry of its own; matching on anything looser would show a Cursor session's deletions.
	it('matches the selected session by its own name, not its numbered label', () => {
		const byClaude = { by: { client: 'mcp' as const, name: 'claude-code' } };
		const byCursor = { by: { client: 'mcp' as const, name: 'cursor-vscode' } };
		expect(deletedBySession(byClaude, 'claude-code')).toBe(true);
		expect(deletedBySession(byCursor, 'claude-code')).toBe(false);
		expect(deletedBySession(byClaude, 'claude-code 2')).toBe(false);
		expect(deletedBySession({}, 'claude-code')).toBe(false); // the user's
		expect(deletedBySession({ by: { client: 'mcp' as const } }, undefined)).toBe(true); // nameless agent
	});
});

function fake(opts: { entries?: TrashEntry[]; restore?: Result<'file.restore'>['items']; restoreErr?: unknown }) {
	const calls: { name: string; params: unknown; ws: string }[] = [];
	const client = {
		call(name: string, params: unknown, o: { workspace: string }) {
			calls.push({ name, params, ws: o.workspace });
			if (name === 'file.trash') return Promise.resolve({ entries: opts.entries ?? [] });
			if (opts.restoreErr) return Promise.reject(opts.restoreErr);
			return Promise.resolve({ items: opts.restore ?? [], done: 0, failed: 0 });
		},
	} as unknown as TrashClient;
	return { client, calls };
}

describe('TrashModel', () => {
	it('loads the workspace trash by its root, and asks nothing before the root is known', async () => {
		const e = entry('a', -1000);
		const f = fake({ entries: [e] });
		let root = '';
		const m = new TrashModel(f.client, () => root);
		await m.load();
		expect(f.calls).toEqual([]);
		root = ' /p ';
		await m.load();
		expect(f.calls).toEqual([{ name: 'file.trash', params: {}, ws: '/p' }]);
		expect(m.entries).toEqual([e]);
	});

	it('keeps the last list and says why when a load fails', async () => {
		const e = entry('a', -1000);
		const f = fake({ entries: [e] });
		const m = new TrashModel(f.client, () => '/p');
		await m.load();
		expect(m.entries).toEqual([e]);
		let fail = false;
		const flaky = new TrashModel(
			{
				call: () =>
					fail
						? Promise.reject(new CapabilityError({ code: 'FILE_IO_FAILED', message: 'no trash' }, false))
						: Promise.resolve({ entries: [e] }),
			} as unknown as TrashClient,
			() => '/p',
		);
		await flaky.load();
		fail = true;
		await flaky.load();
		expect(flaky.error).toBe('FILE_IO_FAILED: no trash');
		expect(flaky.entries).toEqual([e]); // the last good list stays
		fail = false;
		await flaky.load();
		expect(flaky.error).toBeNull();
	});

	it('restores one entry by its id', async () => {
		const f = fake({ restore: [{ trashId: 'a', path: 'a.kt' }] });
		const m = new TrashModel(f.client, () => '/p');
		await m.restore('a');
		expect(f.calls[0]).toEqual({ name: 'file.restore', params: { trashIds: ['a'] }, ws: '/p' });
		expect(m.stateOf('a')).toEqual({ kind: 'restored', path: 'a.kt' });
		await m.restore('a'); // already back: not asked again
		expect(f.calls).toHaveLength(1);
	});

	it('says an entry already gone is gone, and shows any other refusal with its code', async () => {
		const gone = new TrashModel(fake({ restore: [{ trashId: 'a', code: 'FILE_NOT_IN_TRASH', message: 'x' }] }).client, () => '/p');
		await gone.restore('a');
		expect(gone.stateOf('a')).toEqual({ kind: 'gone' });
		expect(GONE_TEXT).toContain('Already gone');

		const taken = new TrashModel(fake({ restore: [{ trashId: 'a', code: 'FILE_EXISTS', message: 'taken' }] }).client, () => '/p');
		await taken.restore('a');
		expect(taken.stateOf('a')).toEqual({ kind: 'failed', code: 'FILE_EXISTS', message: 'taken' });

		const down = new TrashModel(fake({ restoreErr: new CapabilityError({ code: 'WORKSPACE_NOT_OPEN', message: 'closed' }, false) }).client, () => '/p');
		await down.restore('a');
		expect(down.stateOf('a')).toEqual({ kind: 'failed', code: 'WORKSPACE_NOT_OPEN', message: 'closed' });
	});
});
